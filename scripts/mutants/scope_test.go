package main

import (
	"regexp"
	"slices"
	"testing"
)

// TestCoverageScopeIsWhatCompilesTheMutants: the coverage pass measures a
// suite when its test binary, or a tool it runs, compiles the package of a
// selected mutant's Go file, and an anchor's test alone otherwise (MQ-tell:
// a pass over every suite outran its 90 minute bound). A mutant -run does
// not select adds nothing; an .ergo mutant adds only its anchor's tests.
// Mutants scope-select-ignored, scope-tools-ignored and
// scope-anchor-dropped (this package's ledger); TestRunnerMatchesModel
// covers the go list reading (deps), since a suite it misread would leave
// its mutants uncovered.
func TestCoverageScopeIsWhatCompilesTheMutants(t *testing.T) {
	ledger := &Ledger{Mutants: []*Mutant{
		{Name: "x-value", File: "x/f.go", From: "1", To: "2"},
		{Name: "y-value", File: "y/g.go", From: "1", To: "2"},
		{Name: "x-std", File: "std/s.ergo", From: "a", To: "b", Anchor: &Anchor{Tests: []string{"b:TestZ"}}},
	}}
	pkgs, anchors := targets("m", []Job{{Ledger: ledger, Select: regexp.MustCompile("^x-")}})
	if !slices.Equal(pkgs, []string{"m/x"}) {
		t.Fatalf("targets %v; want only m/x: y-value is not selected and s.ergo has no block", pkgs)
	}
	suites := []Suite{
		{Name: "a", Package: "./a"},
		{Name: "b", Package: "./b"},
		{Name: "c", Command: []string{"{bin:tool}", "{test}"}},
	}
	scope := planScope(suites,
		map[string][]string{"a": {"m/a", "m/x"}, "b": {"m/b", "m/y"}},
		map[string][]string{"tool": {"m/tool", "m/x"}},
		pkgs, anchors)
	if !scope.suites["a"] || !scope.suites["c"] || scope.suites["b"] || !slices.Equal(scope.skipped, []string{"b"}) {
		t.Fatalf("suites %v, skipped %v; want a and c (its tool compiles m/x), not b", scope.suites, scope.skipped)
	}
	if !scope.measures("b", "b:TestZ") || scope.measures("b", "b:TestW") {
		t.Fatal("the anchor's test b:TestZ is measured alone, and no other test of b")
	}
}
