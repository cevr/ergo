package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A port of a Foldkit example holds origin/ as a folder (FK0; corpus plan
// §18.3), where a CLI port holds an origin file beside record.sh. The
// folder holds:
//
//   - source: one line, the path from the repository root of the vendored
//     upstream folder whose story and scene tests the port restates;
//   - tests.txt: one line per upstream test, "FILE: TITLE -> RESTATEMENT",
//     FILE being the test file's path under source, TITLE the test's
//     title as written (an .each table is one test), and RESTATEMENT the
//     Ergo test, law or script that restates it, or "not restated: …"
//     with the reason; lines that start with # are comments;
//   - README.md: the upstream path, the commit and the license;
//
// and whatever else the port keeps of its original (measurements, a
// recorded wire). The check reads the titles from the vendored files, so
// nothing is pinned: a title that tests.txt lacks, one it has twice, one
// that the upstream does not have, and a line with no restatement are
// layout errors.

// upstreamTest is the name of a test call: test, it, it.effect, and their
// .each, .only and .skip forms.
var upstreamTest = regexp.MustCompile(`^(?:test|it)(?:\.effect)?(\.each|\.only|\.skip)?$`)

// testCall is a test call of a source: its name's offset, and the offset
// of its "(".
type testCall struct {
	at, paren int
	each      bool
}

// testCalls finds the test calls of a TypeScript source, outside string
// literals and comments: a whole dotted name that upstreamTest matches
// (so init( and x.it( are not calls), then "(".
func testCalls(src string) []testCall {
	var calls []testCall
	ident := func(c byte) bool {
		return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i = endOfString(src, i) + 1
		case strings.HasPrefix(src[i:], "//"):
			if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
				i += n
			} else {
				i = len(src)
			}
		case strings.HasPrefix(src[i:], "/*"):
			if n := strings.Index(src[i+2:], "*/"); n >= 0 {
				i += n + 4
			} else {
				i = len(src)
			}
		case ident(c):
			// A whole dotted name: x.it and init are read as one name each.
			j := i
			for j < len(src) && (ident(src[j]) || src[j] == '.') {
				j++
			}
			if m := upstreamTest.FindStringSubmatch(src[i:j]); m != nil {
				k := j
				for k < len(src) && isSpace(src[k]) {
					k++
				}
				if k < len(src) && src[k] == '(' {
					calls = append(calls, testCall{at: i, paren: k, each: m[1] == ".each"})
				}
			}
			i = j
		default:
			i++
		}
	}
	return calls
}

// upstreamTitles gives the title of each test call of a TypeScript test
// source, in order, with spaces collapsed. An .each call's title is the
// first string literal after its table. A call whose title is not a string
// literal is a problem.
func upstreamTitles(src string) (titles []string, problems []string) {
	for _, call := range testCalls(src) {
		i := call.paren
		if call.each {
			i = skipBracket(src, i)
			for i < len(src) && isSpace(src[i]) {
				i++
			}
			if i >= len(src) || src[i] != '(' {
				problems = append(problems, fmt.Sprintf("line %d: an .each with no call after its table", lineOf(src, call.at)))
				continue
			}
		}
		j := i + 1
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if j >= len(src) || !strings.ContainsRune(`'"`+"`", rune(src[j])) {
			problems = append(problems, fmt.Sprintf("line %d: a test whose title is not a string literal", lineOf(src, call.at)))
			continue
		}
		end := endOfString(src, j)
		titles = append(titles, strings.Join(strings.Fields(src[j+1:end]), " "))
	}
	return titles, problems
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func lineOf(src string, at int) int { return strings.Count(src[:at], "\n") + 1 }

// endOfString is the index of the quote that closes the string literal
// that starts at i.
func endOfString(src string, i int) int {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		if src[j] == '\\' {
			j++
			continue
		}
		if src[j] == q {
			return j
		}
	}
	return len(src)
}

// skipBracket is the index after the bracket that closes the one at i,
// skipping string literals.
func skipBracket(src string, i int) int {
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '\'', '"', '`':
			j = endOfString(src, j)
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(src)
}

// countKeys is the keys of m, sorted.
func countKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// isUpstreamTestFile reports whether name is a story or a scene test file.
func isUpstreamTestFile(name string) bool {
	return strings.HasSuffix(name, "story.test.ts") || strings.HasSuffix(name, "scene.test.ts")
}

// checkOrigin checks the origin folder dir of a port; root is the
// repository root.
func checkOrigin(dir, root string) []string {
	var problems []string
	problem := func(format string, args ...any) {
		problems = append(problems, "origin/"+fmt.Sprintf(format, args...))
	}
	for _, required := range []string{"README.md", "source", "tests.txt"} {
		if info, err := os.Stat(filepath.Join(dir, required)); err != nil || !info.Mode().IsRegular() {
			problem("%s: missing; an origin folder holds README.md, source and tests.txt", required)
		}
	}
	if len(problems) > 0 {
		return problems
	}
	raw, err := os.ReadFile(filepath.Join(dir, "source"))
	if err != nil {
		return []string{err.Error()}
	}
	rel := strings.TrimSpace(string(raw))
	if rel == "" || strings.Contains(rel, "\n") || filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		problem("source: one line, a path inside the repository to the vendored upstream folder")
		return problems
	}
	upstream := filepath.Join(root, rel)
	want := map[string]int{}
	err = filepath.WalkDir(upstream, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if d.IsDir() || !isUpstreamTestFile(d.Name()) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, _ := filepath.Rel(upstream, path)
		file = filepath.ToSlash(file)
		titles, found := upstreamTitles(string(src))
		for _, f := range found {
			problem("source: %s %s", file, f)
		}
		for _, t := range titles {
			want[file+": "+t]++
		}
		return nil
	})
	if err != nil {
		problem("source: %v", err)
		return problems
	}
	if len(want) == 0 {
		problem("source: %s holds no story or scene test", rel)
		return problems
	}
	text, err := os.ReadFile(filepath.Join(dir, "tests.txt"))
	if err != nil {
		return []string{err.Error()}
	}
	have := map[string]int{}
	for n, line := range strings.Split(string(text), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		at := strings.LastIndex(line, " -> ")
		if at < 0 || strings.TrimSpace(line[at+4:]) == "" {
			problem("tests.txt:%d: no restatement; a line is FILE: TITLE -> RESTATEMENT, or -> not restated: REASON", n+1)
			if at < 0 {
				continue
			}
		}
		have[line[:at]]++
	}
	for _, key := range countKeys(want) {
		switch {
		case have[key] < want[key]:
			problem("tests.txt: no line for %s", key)
		case have[key] > want[key]:
			problem("tests.txt: %d lines for %s, which upstream has %d of", have[key], key, want[key])
		}
	}
	for _, key := range countKeys(have) {
		if want[key] == 0 {
			problem("tests.txt: %s is not a test of %s", key, rel)
		}
	}
	slices.Sort(problems)
	return problems
}
