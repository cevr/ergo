package main

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// .ergo coverage (GATE2 GT7, GT7b). In the coverage pass the runner sets
// ERGO_COVER_GO (internal/sketch/build, cover.go) for every child, so each
// program the ergo CLI builds while a test runs is a coverage build of a
// copy of its lowered module, at DIR/KEY with import path
// ergo.local/cover/KEY, and writes its counters to the test's GOCOVERDIR.
// The cover tool of go1.27 names such a block by the file of its start's
// Go position (the base name of the last //line or /*line*/ directive
// before it, or the Go file itself), go1.26's by the Go file; both number
// it by the Go file's own lines, so the block alone names no .ergo line.
// ergoMapper reads the copy's Go files, finds the one whose line holds a
// start of that name, and maps each Go line of the block to the .ergo
// line it came from: after a //line or /*line*/ directive, or after a
// cover mark (//ergo:cover, which a coverage build writes in std bodies,
// lowered without directives: lower.CoverMark), each line is one on. A
// line of a checkout file is recorded as a block of that file, which
// coverage.covering reads as it reads a Go block. A top-level func starts
// with no .ergo position (the runtime, a body with neither), though its Go
// position goes on.

// coverGoVariable is internal/sketch/build's CoverVariable.
const coverGoVariable = "ERGO_COVER_GO"

// coverGoModule is the module path prefix of a coverage copy.
const coverGoModule = "ergo.local/cover/"

// coverMark starts a cover mark (lower.CoverMark).
const coverMark = "//ergo:cover "

// stdTree is where a cover mark's std/… path lives in the checkout.
const stdTree = "internal/sketch/std/"

// ergoPos is a position of .ergo source: a path (absolute, or std/… of a
// cover mark) and a line.
type ergoPos struct {
	file string
	line int
}

// goLine is one Go line: its .ergo positions, several when it holds an
// inline directive, and the names the cover tool of go1.27 may give a
// block that starts on it (the file of its Go position at its start and
// after each inline directive).
type goLine struct {
	at    []ergoPos
	names []string
}

type ergoMapper struct {
	dir  string // ERGO_COVER_GO of the coverage pass
	root string // the checkout copy the programs were lowered from
	read map[string][]string
	maps map[string][]goLine
}

func newErgoMapper(dir, root string) *ergoMapper {
	return &ergoMapper{dir: dir, root: root, read: map[string][]string{}, maps: map[string][]goLine{}}
}

// blocks maps a profile block of a coverage copy (name is
// ergo.local/cover/KEY/NAME, lines lo to hi) to blocks of checkout files,
// or nil when name is no coverage copy's.
func (m *ergoMapper) blocks(name string, lo, hi int) []blockKey {
	rest, ok := strings.CutPrefix(name, coverGoModule)
	if !ok || m == nil {
		return nil
	}
	// rest is KEY/BASE, or KEY/DIR/BASE for a package below the copy's
	// root (a program of internal/sketch/lower's dispatcher, p/NAME);
	// key is the package's directory in the copy.
	at := strings.LastIndexByte(rest, '/')
	if at <= 0 || strings.Contains(rest, "..") {
		return nil
	}
	key, base := rest[:at], untag(rest[at+1:])
	seen := map[blockKey]bool{}
	var out []blockKey
	for _, goFile := range m.goFiles(key) {
		lines := m.lines(key, goFile)
		if hi > len(lines) {
			continue
		}
		// The block's name is its start's Go position, or the Go file's
		// own (the cover tool of go1.26 names every block so).
		if base != goFile && !slices.Contains(lines[lo-1].names, base) {
			continue
		}
		for g := lo; g <= hi; g++ {
			for _, p := range lines[g-1].at {
				rel, inside := m.relative(p.file)
				if !inside {
					continue
				}
				b := blockKey{file: rel, start: p.line, end: p.line}
				if !seen[b] {
					seen[b] = true
					out = append(out, b)
				}
			}
		}
	}
	return out
}

// relative is file's path in the checkout: a std/… path of a cover mark
// is in stdTree; an absolute path must be inside the copy root.
func (m *ergoMapper) relative(file string) (string, bool) {
	if rest, ok := strings.CutPrefix(file, "std/"); ok {
		if strings.Contains(rest, "..") {
			return "", false
		}
		return stdTree + rest, true
	}
	rel, err := filepath.Rel(m.root, filepath.FromSlash(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// goFiles are the Go files of the package directory key (KEY or
// KEY/DIR) of a copy.
func (m *ergoMapper) goFiles(key string) []string {
	if files, ok := m.read[key]; ok {
		return files
	}
	var files []string
	entries, _ := os.ReadDir(filepath.Join(m.dir, key))
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	m.read[key] = files
	return files
}

func (m *ergoMapper) lines(key, goFile string) []goLine {
	id := key + "/" + goFile
	if l, ok := m.maps[id]; ok {
		return l
	}
	data, _ := os.ReadFile(filepath.Join(m.dir, key, goFile))
	l := mapGoLines(goFile, string(data))
	m.maps[id] = l
	return l
}

// mapGoLines maps each line of the Go file name. The Go position's file
// is name until a //line directive or an inline /*line*/ one, then that
// directive's base name. The .ergo position: a //line directive or a cover
// mark gives the next line its position, an inline directive the text
// after it, and each line after that is one more; a line that starts a
// top-level func has none, nor have the lines after it until the next
// directive or mark.
func mapGoLines(name, text string) []goLine {
	lines := strings.Split(text, "\n")
	out := make([]goLine, len(lines))
	var cur *ergoPos
	goName := name
	for g, line := range lines {
		if rest, ok := strings.CutPrefix(line, "//line "); ok {
			if p, ok := parseDirective(rest); ok {
				cur = &p
				goName = path.Base(p.file)
			} else {
				cur = nil
			}
			continue
		}
		if rest, ok := strings.CutPrefix(strings.TrimLeft(line, "\t "), coverMark); ok {
			if p, ok := parseDirective(rest); ok {
				cur = &p
			} else {
				cur = nil
			}
			continue
		}
		if strings.HasPrefix(line, "func ") {
			cur = nil
		}
		out[g].names = []string{goName}
		if cur != nil {
			out[g].at = append(out[g].at, *cur)
		}
		var last *ergoPos
		for rest := line; ; {
			at := strings.Index(rest, "/*line ")
			if at < 0 {
				break
			}
			end := strings.Index(rest[at:], "*/")
			if end < 0 {
				break
			}
			if p, ok := parseDirective(rest[at+len("/*line ") : at+end]); ok {
				out[g].at = append(out[g].at, p)
				goName = path.Base(p.file)
				if !slices.Contains(out[g].names, goName) {
					out[g].names = append(out[g].names, goName)
				}
				last = &p
			}
			rest = rest[at+end:]
		}
		switch {
		case last != nil:
			next := ergoPos{last.file, last.line + 1}
			cur = &next
		case cur != nil:
			next := ergoPos{cur.file, cur.line + 1}
			cur = &next
		}
	}
	return out
}

// parseDirective reads FILE:LINE or FILE:LINE:COL, the file without its
// inline tag (#ergo-inline-N).
func parseDirective(s string) (ergoPos, bool) {
	s = strings.TrimSpace(s)
	file, num, ok := cutLastNumber(s)
	if !ok {
		return ergoPos{}, false
	}
	if f, n, ok := cutLastNumber(file); ok {
		file, num = f, n
	}
	if file == "" || num < 1 {
		return ergoPos{}, false
	}
	return ergoPos{untag(file), num}, true
}

// cutLastNumber splits "X:N" into X and N.
func cutLastNumber(s string) (string, int, bool) {
	colon := strings.LastIndexByte(s, ':')
	if colon < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(s[colon+1:])
	if err != nil {
		return "", 0, false
	}
	return s[:colon], n, true
}

// untag drops an inline tag (#ergo-inline-N) from a directive's file.
func untag(file string) string {
	if at := strings.Index(file, "#"); at >= 0 {
		return file[:at]
	}
	return file
}
