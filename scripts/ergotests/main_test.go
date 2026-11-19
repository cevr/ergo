package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestErgoTestsJudgeEachObligation: ergotests is the gate's and the mutant
// runner's way to run a package's Ergo tests (ST-0). A refuted test fails
// the run, and a skipped one too (a skip is not a pass); a law that holds
// passes; -run judges only what it selects, and a -run that selects
// nothing fails; DIR/... reaches a nested package and skips testdata.
//
// Mutants (testdata/mutants.json): ergotests-skip-is-pass,
// ergotests-run-ignored, ergotests-empty-run-passes,
// ergotests-testdata-walked.
func TestErgoTestsJudgeEachObligation(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/a.ergo", "func twice(n int) int { n + n }\n\n"+
		"test Good {\n\tassert twice(2) == 4\n}\n\ntest Bad {\n\tassert twice(2) == 5\n}\n\n"+
		"test Skips {\n\tskip \"not on this host\"\n}\n\nlaw Doubles(n int) { twice(n) - n == n }\n")
	write("a/b/b.ergo", "test Nested {\n\tassert 1 + 1 == 2\n}\n")
	write("a/testdata/bad/bad.ergo", "this does not parse\n")
	dir := filepath.Join(root, "a")
	run := func(pattern, sel string) (int, string) {
		var re *regexp.Regexp
		if sel != "" {
			re = regexp.MustCompile(sel)
		}
		var out strings.Builder
		code := runAll(&out, []string{pattern}, re)
		return code, out.String()
	}
	for _, c := range []struct {
		pattern, sel string
		code         int
	}{
		{dir, "^Good$", 0},
		{dir, "^Bad$", 1},
		{dir, "^Skips$", 1},
		{dir, "^law Doubles$", 0},
		{dir, "^Missing$", 1},
		{dir + "/...", "^(Good|Nested)$", 0},
		{dir + "/...", "", 1},
	} {
		if code, out := run(c.pattern, c.sel); code != c.code {
			t.Errorf("ergotests -run %q %s: exit %d, want %d\n%s", c.sel, c.pattern, code, c.code, out)
		}
	}
	if _, out := run(dir+"/...", "^Nested$"); !strings.Contains(out, "passed") || !strings.Contains(out, "Nested") {
		t.Errorf("DIR/... does not reach the nested package:\n%s", out)
	}
}
