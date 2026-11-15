package main

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// .ergo coverage (GATE2 GT7). In the coverage pass the runner sets
// ERGO_COVER_GO (internal/sketch/build, cover.go) for every child, so each
// program the ergo CLI builds while a test runs is a coverage build of a
// copy of its lowered module, at DIR/KEY with import path
// ergo.local/cover/KEY, and writes its counters to the test's GOCOVERDIR.
// The cover tool names such a block by the base name of the //line
// directive that governs it (main.ergo) but numbers it by the Go file's
// own lines, so the block alone names no .ergo line. ergoMapper reads the
// copy's Go files and maps each Go line of the block, through the //line
// and /*line*/ directives the lowering writes, to the .ergo file and line
// it came from; a line of a checkout file is recorded as a block of that
// file, which coverage.covering reads as it reads a Go block. A Go line
// that no directive governs (the runtime, a std body, which the lowering
// writes without directives) maps to nothing: a top-level func starts
// with no position.

// coverGoVariable is internal/sketch/build's CoverVariable.
const coverGoVariable = "ERGO_COVER_GO"

// coverGoModule is the module path prefix of a coverage copy.
const coverGoModule = "ergo.local/cover/"

// ergoPos is a position of .ergo source: an absolute path and a line.
type ergoPos struct {
	file string
	line int
}

// goLines are the positions of one Go file's lines (index 0 is line 1),
// several when a line holds inline directives.
type goLines [][]ergoPos

type ergoMapper struct {
	dir  string // ERGO_COVER_GO of the coverage pass
	root string // the checkout copy the programs were lowered from
	read map[string][]string
	maps map[string]goLines
}

func newErgoMapper(dir, root string) *ergoMapper {
	return &ergoMapper{dir: dir, root: root, read: map[string][]string{}, maps: map[string]goLines{}}
}

// blocks maps a profile block of a coverage copy (name is
// ergo.local/cover/KEY/NAME, lines lo to hi) to blocks of checkout files,
// or nil when name is no coverage copy's.
func (m *ergoMapper) blocks(name string, lo, hi int) []blockKey {
	rest, ok := strings.CutPrefix(name, coverGoModule)
	if !ok || m == nil {
		return nil
	}
	key, base, ok := strings.Cut(rest, "/")
	if !ok || key == "" || strings.Contains(key, "..") {
		return nil
	}
	base = untag(base)
	seen := map[blockKey]bool{}
	var out []blockKey
	for _, goFile := range m.goFiles(key) {
		lines := m.lines(key, goFile)
		if hi > len(lines) {
			continue
		}
		own := base == goFile
		for g := lo; g <= hi; g++ {
			for _, p := range lines[g-1] {
				if !own && path.Base(p.file) != base {
					continue
				}
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

func (m *ergoMapper) relative(file string) (string, bool) {
	rel, err := filepath.Rel(m.root, filepath.FromSlash(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// goFiles are the Go files at the top of the copy KEY.
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

func (m *ergoMapper) lines(key, goFile string) goLines {
	id := key + "/" + goFile
	if l, ok := m.maps[id]; ok {
		return l
	}
	data, _ := os.ReadFile(filepath.Join(m.dir, key, goFile))
	l := mapGoLines(string(data))
	m.maps[id] = l
	return l
}

// mapGoLines is the .ergo position of each line of a Go file, by its
// //line and /*line*/ directives: a //line directive gives the next line
// its position, an inline one the text after it, and each line after
// that is one more; a line that starts a top-level func has none, and
// neither have the lines after it until the next directive.
func mapGoLines(text string) goLines {
	lines := strings.Split(text, "\n")
	out := make(goLines, len(lines))
	var cur *ergoPos
	for g, line := range lines {
		if rest, ok := strings.CutPrefix(line, "//line "); ok {
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
		if cur != nil {
			out[g] = append(out[g], *cur)
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
				out[g] = append(out[g], p)
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
