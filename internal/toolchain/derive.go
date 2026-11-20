package toolchain

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The IR compiler patch is derived (PATCH-MERGE): its source is the tree
// toolchain/ir/_go, which merges as text, line by line. Every file under
// _go/src is a file the patch adds to Go's tree at the same path (the
// -ergoir reader, package cmd/compile/internal/ergoir, and noder's
// adapter), and _go/gofiles.patch holds the patch's hunks in Go's own
// files (the hooks, toolchain/ir/hooks). DerivePatch makes the patch from
// them, in path order; `go run ./scripts/irport -write` writes it and its
// digest into toolchain/ir/lock, and TestIRPatchIsCurrent refuses a patch
// or lock that is not what the tree makes. ExtractPatch is the inverse,
// for a branch that still carries an edited patch.

// GoFilesPatch is the name of the hooks' diff under the source tree.
const GoFilesPatch = "gofiles.patch"

// DerivePatch is the patch the source tree src (toolchain/ir/_go) makes:
// a new-file diff of each file under src/src, as git diff writes one,
// and the diffs of gofiles.patch, all in the order of their paths.
func DerivePatch(src string) ([]byte, error) {
	chunks := map[string][]byte{}
	root := filepath.Join(src, "src")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			// ApplyPatch takes neither (a file without hunks, or without a
			// final newline).
			return fmt.Errorf("%s: empty, or without a final newline", p)
		}
		chunks[rel] = newFileDiff(rel, data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	hooks, err := os.ReadFile(filepath.Join(src, GoFilesPatch))
	if err != nil {
		return nil, err
	}
	for _, c := range splitDiff(hooks) {
		if _, dup := chunks[c.path]; dup {
			return nil, fmt.Errorf("%s: both a file under _go/src and a diff in %s", c.path, GoFilesPatch)
		}
		if c.isNew {
			return nil, fmt.Errorf("%s: %s adds the file; put it under _go/src instead", c.path, GoFilesPatch)
		}
		chunks[c.path] = c.text
	}
	paths := make([]string, 0, len(chunks))
	for p := range chunks {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var out bytes.Buffer
	for _, p := range paths {
		out.Write(chunks[p])
	}
	return out.Bytes(), nil
}

// ExtractPatch is the source tree of patch: the content of each file it
// adds (by slash path, src/...) and gofiles.patch, the diffs of the files
// it changes, in their order. DerivePatch of the result is patch again
// when patch is in DerivePatch's form.
func ExtractPatch(patch []byte) (map[string][]byte, []byte, error) {
	files := map[string][]byte{}
	var hooks bytes.Buffer
	for _, c := range splitDiff(patch) {
		if !c.isNew {
			hooks.Write(c.text)
			continue
		}
		parsed, err := parseDiff(c.text)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", c.path, err)
		}
		var data strings.Builder
		for _, h := range parsed[0].hunks {
			for _, l := range h.new {
				data.WriteString(l)
			}
		}
		files[c.path] = []byte(data.String())
	}
	return files, hooks.Bytes(), nil
}

// LockDigest is lock with the digest of its patch line for name set to
// patch's sha256 (the line `patch NAME sha256:HEX`).
func LockDigest(lock []byte, name string, patch []byte) ([]byte, error) {
	sum := sha256.Sum256(patch)
	re := regexp.MustCompile(`(?m)^patch ` + regexp.QuoteMeta(name) + ` sha256:[0-9a-f]+$`)
	if len(re.FindAll(lock, -1)) != 1 {
		return nil, fmt.Errorf("the lock has no one line `patch %s sha256:…`", name)
	}
	return re.ReplaceAll(lock, []byte("patch "+name+" sha256:"+hex.EncodeToString(sum[:]))), nil
}

// chunk is one file's diff of a patch: its path (b/ side), whether it
// adds the file, and its text from its "diff --git" line.
type chunk struct {
	path  string
	isNew bool
	text  []byte
}

// splitDiff splits a git diff into its files' diffs.
func splitDiff(patch []byte) []chunk {
	var out []chunk
	lines := splitLines(string(patch))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(lines[j], "diff --git ") {
			j++
		}
		head := strings.TrimSuffix(lines[i], "\n")
		c := chunk{text: []byte(strings.Join(lines[i:j], ""))}
		if k := strings.LastIndex(head, " b/"); strings.HasPrefix(head, "diff --git ") && k >= 0 {
			c.path = head[k+3:]
		}
		c.isNew = j > i+1 && strings.HasPrefix(lines[i+1], "new file mode ")
		out = append(out, c)
		i = j
	}
	return out
}

// newFileDiff is git diff's text for a new file p (mode 100644) of
// content data, which ends in a newline: its blob id abbreviated to seven
// digits, and one hunk of every line.
func newFileDiff(p string, data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n", p, p)
	fmt.Fprintf(&b, "index 0000000..%s\n--- /dev/null\n+++ b/%s\n", blobID(data)[:7], p)
	lines := splitLines(string(data))
	if len(lines) == 1 {
		b.WriteString("@@ -0,0 +1 @@\n")
	} else {
		fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	}
	for _, l := range lines {
		b.WriteString("+" + l)
	}
	return b.Bytes()
}

// blobID is git's object id of a blob of data.
func blobID(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
