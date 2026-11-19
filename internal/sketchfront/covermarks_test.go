package sketchfront

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestCoverMarksOnlyAddComments: over the determinism domain (generated
// context programs and every example package), the build and test
// lowerings with lower.Unit.CoverMarks are the lowerings without it plus
// cover-mark lines and nothing else: with the marks taken out the Go has
// the same tokens (a comment in a body keeps go/format from writing that
// body on one line; nothing else moves) and every other file is the same
// byte for byte, so a coverage build runs the program the plain build
// runs (GATE2 GT7b). The plain lowering itself is the one before GT7b:
// CoverMarks off writes no mark and takes no other path. Each mark names a std file (std/…, the
// embedded tree internal/sketch/std) and a line inside it, and the domain
// writes marks at all, in std bodies only. Mutants
// (internal/sketch/lower/testdata/mutants.json): gt7b-marks-off,
// gt7b-marks-in-user-bodies, gt7b-join-broken, gt7b-mark-moves-code.
func TestCoverMarksOnlyAddComments(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: lowers every example four times")
	}
	programs := determinismDomain(t, t.TempDir())
	stdRoot := filepath.Join("..", "sketch", "std")
	lines := map[string]int{}
	marks := 0
	for _, p := range programs {
		a, err := CheckDir(p.dir)
		if err != nil || len(a.Diagnostics) > 0 || a.program == nil {
			continue
		}
		for _, lowering := range []struct {
			name string
			run  func(lower.Unit) ([]lower.File, error)
		}{{"Lower", lower.Lower}, {"LowerTests", lower.LowerTests}} {
			plainUnit, markedUnit := a.program.unit(), a.program.unit()
			markedUnit.CoverMarks = true
			plain, errPlain := lowering.run(plainUnit)
			marked, errMarked := lowering.run(markedUnit)
			if (errPlain == nil) != (errMarked == nil) {
				t.Errorf("%s %s: without marks %v, with marks %v", p.describe(), lowering.name, errPlain, errMarked)
				continue
			}
			if len(plain) != len(marked) {
				t.Errorf("%s %s: %d files without marks, %d with", p.describe(), lowering.name, len(plain), len(marked))
				continue
			}
			for i := range plain {
				text, found := stripMarks(string(marked[i].Data))
				same := text == string(plain[i].Data)
				if strings.HasSuffix(plain[i].Path, ".go") {
					same = goTokens(text) == goTokens(string(plain[i].Data))
				}
				if marked[i].Path != plain[i].Path || !same {
					t.Errorf("%s %s: %s with its cover marks taken out differs from the plain lowering:\n%s", p.describe(), lowering.name, marked[i].Path, firstDiff(goTokens(string(plain[i].Data)), goTokens(text)))
					return
				}
				for _, m := range found {
					marks++
					file, line, ok := markedLine(m)
					rest, isStd := strings.CutPrefix(file, "std/")
					if !ok || !isStd {
						t.Errorf("%s %s: mark %q names no std file and line", p.describe(), lowering.name, m)
						return
					}
					n, seen := lines[rest]
					if !seen {
						data, err := os.ReadFile(filepath.Join(stdRoot, filepath.FromSlash(rest)))
						if err != nil {
							t.Errorf("%s %s: mark %q: %v", p.describe(), lowering.name, m, err)
							return
						}
						n = strings.Count(string(data), "\n") + 1
						lines[rest] = n
					}
					if line < 1 || line > n {
						t.Errorf("%s %s: mark %q names line %d of a file of %d lines", p.describe(), lowering.name, m, line, n)
						return
					}
				}
			}
		}
	}
	if marks == 0 {
		t.Error("the domain writes no cover mark: the property checks nothing")
	}
}

// goTokens is the Go text's tokens, one per line, without its comments
// (cover marks, and the inline /*line*/ directives go/format's layout
// decides) and its layout: a comment in a body keeps go/format from
// writing the body on one line, which moves no token.
func goTokens(text string) string {
	fset := token.NewFileSet()
	src := []byte(text)
	var s scanner.Scanner
	s.Init(fset.AddFile("", -1, len(src)), src, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return b.String()
		}
		if tok == token.SEMICOLON {
			lit = ";"
		}
		b.WriteString(tok.String() + " " + lit + "\n")
	}
}

// stripMarks is text without its cover-mark lines, and the marks.
func stripMarks(text string) (string, []string) {
	var kept, marks []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if m, ok := strings.CutPrefix(strings.TrimLeft(line, "\t "), "//ergo:cover "); ok {
			marks = append(marks, strings.TrimSuffix(m, "\n"))
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, ""), marks
}

// markedLine reads a mark's FILE:LINE:COL.
func markedLine(m string) (string, int, bool) {
	rest, col, ok := cutNumber(m)
	if !ok || col < 1 {
		return "", 0, false
	}
	file, line, ok := cutNumber(rest)
	return file, line, ok
}

func cutNumber(s string) (string, int, bool) {
	at := strings.LastIndexByte(s, ':')
	if at < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(s[at+1:])
	return s[:at], n, err == nil
}
