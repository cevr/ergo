package stdlib_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/lawledger/parsed"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ledgerProblems are the disagreements of the runner file's rows with the
// laws of std source (laws, by key) and with the Go tests of the runner
// packages (tests: each test's name, with whether it calls lawledger.Run or
// parsed.Run): a law with no line or with two, a line that names no law, a
// runner that is no such test or runs no laws, and a test that runs laws
// that no line names.
func ledgerProblems(laws map[string]bool, rows []lawledger.Row, tests map[string]bool) []string {
	var out []string
	listed, named := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		key := r.Key()
		named[r.Runner] = true
		runsLaws, declared := tests[r.Runner]
		switch {
		case listed[key]:
			out = append(out, fmt.Sprintf("runners.txt:%d: law %s has a second line", r.Line, key))
		case !laws[key]:
			out = append(out, fmt.Sprintf("runners.txt:%d: %s names no law of std source", r.Line, key))
		case r.Runner == lawledger.NoRunner:
		case !declared:
			out = append(out, fmt.Sprintf("runners.txt:%d: the runner %s of %s is no func Test… of the runner packages %v", r.Line, r.Runner, key, runnerPackages))
		case !runsLaws:
			out = append(out, fmt.Sprintf("runners.txt:%d: the runner %s of %s does not call lawledger.Run, so nothing holds that it runs the law", r.Line, r.Runner, key))
		}
		listed[key] = true
	}
	for _, key := range slices.Sorted(maps.Keys(laws)) {
		if !listed[key] {
			out = append(out, fmt.Sprintf("std law %s has no line in %s: add `%s<TAB>%s`, or the Go test that runs it", key, lawledger.File, key, lawledger.NoRunner))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(tests)) {
		if tests[name] && !named[name] {
			out = append(out, fmt.Sprintf("%s runs laws through lawledger.Run, but no line of %s names it", name, lawledger.File))
		}
	}
	return out
}

// TestLedgerProblemsFixtures: ledgerProblems finds each way a runner file
// can disagree with std source and the runner tests, over a fixture.
// Mutants it kills (testdata/mutants.json): ledger-unlisted-law-passes,
// ledger-second-line-passes, ledger-line-of-no-law-passes,
// ledger-runner-not-a-test-passes, ledger-runner-without-run-passes,
// ledger-runner-without-line-passes.
func TestLedgerProblemsFixtures(t *testing.T) {
	rows, err := lawledger.Parse("# fixture\n" +
		"a X\tTestRuns\n" +
		"a X\tTestRuns\n" +
		"c W\tTestRuns\n" +
		"b Z\tTestGone\n" +
		"a Y\tTestPlain\n" +
		"b R\t" + lawledger.NoRunner + "\n")
	if err != nil {
		t.Fatal(err)
	}
	laws := map[string]bool{"a X": true, "a Y": true, "b Z": true, "b R": true, "b Q": true}
	tests := map[string]bool{"TestRuns": true, "TestPlain": false, "TestOrphan": true}
	got := ledgerProblems(laws, rows, tests)
	want := []string{
		"runners.txt:3: law a X has a second line",
		"runners.txt:4: c W names no law of std source",
		fmt.Sprintf("runners.txt:5: the runner TestGone of b Z is no func Test… of the runner packages %v", runnerPackages),
		"runners.txt:6: the runner TestPlain of a Y does not call lawledger.Run, so nothing holds that it runs the law",
		"std law b Q has no line in " + lawledger.File + ": add `b Q<TAB>" + lawledger.NoRunner + "`, or the Go test that runs it",
		"TestOrphan runs laws through lawledger.Run, but no line of " + lawledger.File + " names it",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the problems are\n%q\nwant\n%q", got, want)
	}
}

var word = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// TestLedgerStatementIsTheParsers is lawledger.Statement's licence: for
// every std law with source text, the text Statement cuts from its file is
// the parser's statement (parsed.Statement), and a seeded one-word edit
// inside the law changes it; for every law the parser derives, Statement
// finds no text in any file of its package. A law it cannot find fails.
// Mutants it kills: statement-stops-at-first-brace (internal/lawledger).
func TestLedgerStatementIsTheParsers(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 22))
	cut, derived := 0, 0
	for _, p := range stdLawPackages(t) {
		for _, f := range p.files {
			check := func(law string, d *syntax.LawDecl) {
				if d.Derived != nil || d.Pair != nil {
					derived++
					for _, g := range p.files {
						if s, ok := lawledger.Statement(g.Src, law); ok {
							t.Errorf("std %s: the derived law %s has source text %q", p.path, law, s)
						}
					}
					return
				}
				cut++
				want := parsed.Statement(f, d)
				got, ok := lawledger.Statement(f.Src, law)
				if !ok || got != want {
					t.Errorf("std %s law %s: Statement gives %q, %v; the parser says %q", p.path, law, got, ok, want)
					return
				}
				from, to := d.Name.End(), d.Body.End()
				words := word.FindAllStringIndex(f.Src[from:to], -1)
				if len(words) == 0 {
					return
				}
				at := from + words[r.IntN(len(words))][1]
				edited := f.Src[:at] + "q" + f.Src[at:]
				if s, ok := lawledger.Statement(edited, law); !ok || s == want {
					t.Errorf("std %s law %s: an edit at byte %d leaves Statement %q, %v", p.path, law, at, s, ok)
				}
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *syntax.LawDecl:
					check(d.Name.Name, d)
				case *syntax.TypeDecl:
					if it, ok := d.Spec.(*syntax.InterfaceType); ok {
						for _, l := range it.Laws {
							check(d.Name.Name+"."+l.Name.Name, l)
						}
					}
				}
			}
		}
	}
	t.Logf("%d laws cut from source, %d derived laws with no text", cut, derived)
	if cut == 0 || derived == 0 {
		t.Errorf("the walk found %d laws with source text and %d derived ones; it proves nothing", cut, derived)
	}
}
