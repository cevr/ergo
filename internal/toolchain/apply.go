package toolchain

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Applied is a unified diff applied to a tree: the new content of every
// file it touches, by slash path relative to the tree, and the hunks that
// did not apply.
type Applied struct {
	Files    map[string][]byte
	Hunks    int
	Rejected []string
}

// diffFile is one file of a git diff: its path, whether it is new, and
// its hunks.
type diffFile struct {
	path  string
	isNew bool
	hunks []hunk
}

// hunk is one @@ section: the line its old text starts at (1-based), the
// lines it expects and the lines it writes.
type hunk struct {
	header   string
	oldStart int
	old, new []string
}

// ApplyPatch applies a git diff (git diff's format: modified and new
// files, no renames, deletions or binary files) to the files under root,
// which it only reads. A hunk applies where its old lines stand exactly,
// at its stated line or the nearest line from it, as patch(1) does with no
// fuzz; a hunk whose old lines stand nowhere is rejected, never applied
// in part. scripts/irport and ergo's patched compile (Patch) both apply
// the IR patch with this one function, so a port's row says what ergo
// does.
func ApplyPatch(root string, patch []byte) (Applied, error) {
	files, err := parseDiff(patch)
	if err != nil {
		return Applied{}, err
	}
	out := Applied{Files: map[string][]byte{}}
	for _, f := range files {
		out.Hunks += len(f.hunks)
		var lines []string
		if !f.isNew {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.path)))
			if err != nil {
				return Applied{}, err
			}
			lines = splitLines(string(data))
		} else if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.path))); err == nil {
			out.Rejected = append(out.Rejected, f.path+": the new file exists")
			continue
		}
		shift := 0
		for _, h := range f.hunks {
			at := find(lines, h.old, h.oldStart-1+shift)
			if at < 0 {
				out.Rejected = append(out.Rejected, f.path+": "+h.header)
				continue
			}
			lines = append(lines[:at:at], append(append([]string(nil), h.new...), lines[at+len(h.old):]...)...)
			shift = at - (h.oldStart - 1) + len(h.new) - len(h.old)
		}
		out.Files[f.path] = []byte(strings.Join(lines, ""))
	}
	return out, nil
}

// find is the line nearest to want where old stands, or -1.
func find(lines, old []string, want int) int {
	matches := func(at int) bool {
		if at < 0 || at+len(old) > len(lines) {
			return false
		}
		for i, l := range old {
			if lines[at+i] != l {
				return false
			}
		}
		return true
	}
	for d := 0; d <= len(lines); d++ {
		if matches(want - d) {
			return want - d
		}
		if matches(want + d) {
			return want + d
		}
	}
	return -1
}

// splitLines splits text into lines that keep their newlines.
func splitLines(text string) []string {
	var lines []string
	for text != "" {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:i+1])
		text = text[i+1:]
	}
	return lines
}

// parseDiff reads the files and hunks of a git diff.
func parseDiff(patch []byte) ([]diffFile, error) {
	var files []diffFile
	lines := splitLines(string(patch))
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSuffix(lines[i], "\n")
		switch {
		case strings.HasPrefix(l, "diff --git "):
			files = append(files, diffFile{})
		case strings.HasPrefix(l, "new file mode "):
			if len(files) == 0 {
				return nil, fmt.Errorf("patch line %d: %q before a diff header", i+1, l)
			}
			files[len(files)-1].isNew = true
		case strings.HasPrefix(l, "deleted file mode "), strings.HasPrefix(l, "rename "), strings.HasPrefix(l, "Binary files "), strings.HasPrefix(l, "GIT binary patch"):
			return nil, fmt.Errorf("patch line %d: %q is not supported", i+1, l)
		case strings.HasPrefix(l, "+++ "):
			if len(files) == 0 {
				return nil, fmt.Errorf("patch line %d: %q before a diff header", i+1, l)
			}
			name, ok := strings.CutPrefix(l, "+++ b/")
			if !ok || name == "" || !path.IsAbs("/"+name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
				return nil, fmt.Errorf("patch line %d: bad file name %q", i+1, l)
			}
			files[len(files)-1].path = name
		case strings.HasPrefix(l, "@@ "):
			if len(files) == 0 || files[len(files)-1].path == "" {
				return nil, fmt.Errorf("patch line %d: a hunk before its file", i+1)
			}
			h, n, err := parseHunk(lines[i:])
			if err != nil {
				return nil, fmt.Errorf("patch line %d: %w", i+1, err)
			}
			files[len(files)-1].hunks = append(files[len(files)-1].hunks, h)
			i += n
		}
	}
	for _, f := range files {
		if f.path == "" || len(f.hunks) == 0 {
			return nil, fmt.Errorf("patch: a file without a name or hunks")
		}
	}
	return files, nil
}

// parseHunk reads one hunk, its header first, and returns it and the
// number of body lines it read.
func parseHunk(lines []string) (hunk, int, error) {
	h := hunk{header: strings.TrimSuffix(lines[0], "\n")}
	var oldStart, oldCount, newCount int
	var err error
	fields := strings.Fields(h.header)
	if len(fields) < 4 || fields[3] != "@@" {
		return h, 0, fmt.Errorf("bad hunk header %q", h.header)
	}
	if oldStart, oldCount, err = hunkRange(fields[1], "-"); err == nil {
		_, newCount, err = hunkRange(fields[2], "+")
	}
	if err != nil {
		return h, 0, fmt.Errorf("hunk header %q: %w", h.header, err)
	}
	h.oldStart = oldStart
	if oldCount == 0 {
		h.oldStart++ // an insertion's old range names the line before it
	}
	n := 0
	for len(h.old) < oldCount || len(h.new) < newCount {
		n++
		if n >= len(lines) {
			return h, 0, fmt.Errorf("hunk %q ends early", h.header)
		}
		l := lines[n]
		if len(lines) > n+1 && strings.HasPrefix(lines[n+1], `\ No newline at end of file`) {
			return h, 0, fmt.Errorf("hunk %q: a file without a final newline is not supported", h.header)
		}
		if l == "" {
			return h, 0, fmt.Errorf("hunk %q: empty line", h.header)
		}
		text := l[1:]
		switch l[0] {
		case ' ':
			h.old, h.new = append(h.old, text), append(h.new, text)
		case '-':
			h.old = append(h.old, text)
		case '+':
			h.new = append(h.new, text)
		default:
			return h, 0, fmt.Errorf("hunk %q: line %q", h.header, strings.TrimSuffix(l, "\n"))
		}
	}
	if len(h.old) != oldCount || len(h.new) != newCount {
		return h, 0, fmt.Errorf("hunk %q: counts do not match its lines", h.header)
	}
	return h, n, nil
}

// hunkRange parses "-l,s" (or "-l", one line) with the given sign.
func hunkRange(field, sign string) (int, int, error) {
	r, ok := strings.CutPrefix(field, sign)
	if !ok {
		return 0, 0, fmt.Errorf("range %q", field)
	}
	start, count, found := strings.Cut(r, ",")
	if !found {
		count = "1"
	}
	s, err1 := strconv.Atoi(start)
	c, err2 := strconv.Atoi(count)
	if err1 != nil || err2 != nil || s < 0 || c < 0 {
		return 0, 0, fmt.Errorf("range %q", field)
	}
	return s, c, nil
}
