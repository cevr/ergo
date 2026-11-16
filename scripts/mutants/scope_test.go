package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// TestCoverageScopeIsWhatCompilesTheMutants: the coverage pass measures a
// suite when its test binary, or a tool it runs, compiles the package of a
// selected mutant's file, or when a row names one of its tests (MQ-tell: a
// pass over every suite outran its 90 minute bound). A mutant -run does not
// select adds nothing; a file that is not Go counts as the package that
// embeds it, and one no package embeds adds only the tests its row names.
// Mutants scope-select-ignored, scope-tools-ignored, scope-anchor-dropped
// and gtb-embedder-none (this package's ledger); TestRunnerMatchesModel
// covers the go list reading (deps), since a suite it misread would leave
// its mutants uncovered.
func TestCoverageScopeIsWhatCompilesTheMutants(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "e", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "e", "embed.go"), []byte("package e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger := &Ledger{Mutants: []*Mutant{
		{Name: "x-value", File: "x/f.go", From: "1", To: "2"},
		{Name: "y-value", File: "y/g.go", From: "1", To: "2"},
		{Name: "x-std", File: "std/s.ergo", From: "a", To: "b", Anchor: &Anchor{Tests: []string{"b:TestZ"}}},
		{Name: "x-embedded", File: "e/data/d.ergo", From: "a", To: "b"},
	}}
	pkgs, anchors := targets(root, "m", []Job{{Ledger: ledger, Select: regexp.MustCompile("^x-")}})
	if !slices.Equal(pkgs, []string{"m/e", "m/x"}) {
		t.Fatalf("targets %v; want m/e (embeds d.ergo) and m/x: y-value is not selected and no package embeds s.ergo", pkgs)
	}
	suites := []Suite{
		{Name: "a", Package: "./a"},
		{Name: "b", Package: "./b"},
		{Name: "c", Command: []string{"{bin:tool}", "{test}"}},
		{Name: "d", Package: "./d"},
	}
	scope := planScope(suites,
		map[string][]string{"a": {"m/a", "m/x"}, "b": {"m/b", "m/y"}, "d": {"m/d"}},
		map[string][]string{"tool": {"m/tool", "m/x"}},
		pkgs, anchors)
	if !scope.suites["a"] || !scope.suites["c"] || !scope.suites["b"] || scope.suites["d"] || !slices.Equal(scope.skipped, []string{"d"}) {
		t.Fatalf("suites %v, skipped %v; want a, c (its tool compiles m/x) and b (x-std names b:TestZ), not d", scope.suites, scope.skipped)
	}
}
