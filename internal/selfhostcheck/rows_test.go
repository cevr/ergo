package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// goRows is the Go checker's S101 and S102 of a checked package, as
// exprdump writes the row pass's: "FILE OFFSET LENGTH CODE MESSAGE".
func goRows(file string, ds []check.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Code == check.CodeUndeclared || d.Code == check.CodeMissingCap {
			out = append(out, fmt.Sprintf("%s %d %d %s %s", file, d.Offset, d.Length, d.Code, d.Message))
		}
	}
	return out
}

// TestCompilerRowsLawsRun runs the laws of compiler/check/rows through `ergo test`.
func TestCompilerRowsLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/rows")
}

// TestCompilerRowsAgreeWithChecker is S3-ROWS's agreement: the S101s and
// S102s compiler/check/rows reports (exprdump -corpus, its S lines) are
// the Go checker's, text, position and order, over testdata/rows: a
// fixture per form the pass walks (a try, an argument under try, a
// return, a tail and a branch tail; a call whose callee needs a
// capability, an operation of a capability named as the receiver; the
// clause to write after a written one or as a new one). Each file's
// "//want: rows" lines are the Go checker's, so a fixture that drifts
// from the rule fails too.
//
// It builds Go because exprdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerRowsAgreeWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	fixtures, err := filepath.Glob(filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "rows", "*.ergo"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no testdata/rows fixture: %v", err)
	}
	var cases []ownCase
	for _, p := range fixtures {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("%s: does not parse: %v", rel(p), ds)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		want := goRows("p.ergo", ds)
		var lines []int
		for _, w := range want {
			off, _ := strconv.Atoi(strings.Fields(w)[1])
			l, _ := lineCol(src, off)
			if len(lines) == 0 || lines[len(lines)-1] != l {
				lines = append(lines, l)
			}
		}
		var marked []int
		for i, l := range strings.Split(src, "\n") {
			if strings.Contains(l, "//want: rows") {
				marked = append(marked, i+1)
			}
		}
		if fmt.Sprint(lines) != fmt.Sprint(marked) {
			t.Errorf("%s: the Go checker's S101s and S102s are on lines %v, the fixture marks %v\n%s", rel(p), lines, marked, strings.Join(want, "\n"))
		}
		cases = append(cases, ownCase{name: rel(p), src: src, want: want, ext: ext, foreign: foreign})
	}
	tmp := t.TempDir()
	var manifest []string
	for i, c := range cases {
		dir := filepath.Join(tmp, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f, e, fo := filepath.Join(dir, "p.ergo"), filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, f, []string{strings.TrimSuffix(c.src, "\n")})
		writeLines(t, e, c.ext)
		writeLines(t, fo, c.foreign)
		manifest = append(manifest, strings.Join([]string{"main", e, fo, f}, " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, exprDump(t, ctx), "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(cases) && strings.HasPrefix(l, "S "):
			got[at] = append(got[at], l[2:])
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	reported := 0
	for i, c := range cases {
		reported += len(c.want)
		if strings.Join(c.want, "\n") != strings.Join(got[i], "\n") {
			t.Errorf("%s: row diagnostics differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		}
	}
	t.Logf("%d fixtures (%d S101 and S102) compared", len(cases), reported)
}
