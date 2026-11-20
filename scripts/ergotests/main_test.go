package main

import (
	"fmt"
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
// ergotests-testdata-walked, lawkiller-skipped-law-kills,
// lawkiller-law-name-ignored, lawkiller-test-matches-law,
// lawkiller-undecided-trusted, lawkiller-ergo-skip-trusted.
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
	// Shouts reaches a Go body of the fixture's own module, which the
	// evaluator has no native of and never will (licence.go: natives are
	// std's), so check time leaves it undecided; a std function such as
	// strings.ToUpper gained a native (ST4) and turned Shouts decided.
	write("go.mod", "module example.com/app\n\ngo 1.27\n")
	write("ergo.mod", "module app\ntrust go app\n")
	write("kern/kern.go", "package kern\n\n//ergo:wait none\nfunc Upper(s string) string { return s }\n")
	write("a/a.ergo", "extern go example.com/app/kern.Upper func upper(s string) string\n\nfunc twice(n int) int { n + n }\n\n"+
		"test Good {\n\tassert twice(2) == 4\n}\n\ntest Bad {\n\tassert twice(2) == 5\n}\n\n"+
		"test Skips {\n\tskip \"not on this host\"\n}\n\nlaw Doubles(n int) { twice(n) - n == n }\n\nlaw Shouts(s string) { upper(s) == upper(s) }\n")
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
	// -killer NAME is the mutant runner's: one test, or law:L for a law
	// (LAW-KILLER), which must pass outright: a law skipped at check time
	// kills nothing, so it is no baseline, though -run lets it by.
	for _, c := range []struct {
		name string
		code int
	}{{"Good", 0}, {"Bad", 1}, {"law:Doubles", 0}, {"law:Shouts", 1}, {"law:Missing", 1}, {"Doubles", 1}, {"law:Good", 1}} {
		var out strings.Builder
		if code := runKiller(&out, []string{dir}, c.name, ""); code != c.code {
			t.Errorf("ergotests -killer %s: exit %d, want %d\n%s", c.name, code, c.code, out.String())
		}
	}
	if code, out := run(dir, "^law Shouts$"); code != 0 {
		t.Errorf("-run of a law skipped at check time: exit %d, want 0 (as ergo check)\n%s", code, out)
	}
	// With -ergo, a law check time leaves undecided is decided by ergo
	// test --law in its package's directory (a host module's law kills,
	// LAW-KILLER); one check time decides runs no ergo. The stand-in ergo
	// records its directory and words, and answers as told.
	ergo := func(answer string, code int) (string, string) {
		log := filepath.Join(root, "ergo-"+strings.ReplaceAll(answer, " ", "-")+".log")
		bin := log + ".sh"
		write(strings.TrimPrefix(bin, root+"/"), fmt.Sprintf("#!/bin/sh\necho \"$PWD $*\" > %s\necho %q\nexit %d\n", log, answer, code))
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		return bin, log
	}
	for _, c := range []struct {
		law, answer string
		exit, code  int
		ran         bool
	}{
		{"law:Shouts", "ok law Shouts", 0, 0, true},
		{"law:Shouts", "refuted law Shouts", 1, 1, true},
		{"law:Shouts", "skipped law Shouts", 0, 1, true},
		{"law:Doubles", "never asked", 1, 0, false},
	} {
		bin, log := ergo(c.answer, c.exit)
		var out strings.Builder
		if code := runKiller(&out, []string{dir}, c.law, bin); code != c.code {
			t.Errorf("-ergo, -killer %s, ergo says %q: exit %d, want %d\n%s", c.law, c.answer, code, c.code, out.String())
		}
		data, err := os.ReadFile(log)
		if !c.ran {
			if err == nil {
				t.Errorf("-killer %s, decided at check time, ran ergo: %s", c.law, data)
			}
			continue
		}
		if want := dir + " test . --law Shouts\n"; string(data) != want {
			t.Errorf("ergo ran as %q; want %q", data, want)
		}
	}
}
