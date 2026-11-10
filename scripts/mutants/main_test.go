package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestHeaderLineIsCoveredByWhatReachesIt is the smallest repro of M1's
// uncovered runtime/v1 mutant: the cover tool gives a case line of a
// tagless switch no block (measured: blocks 180 and 182 around the case at
// 181 of http_client.go), so a mutant there had no covering test. The
// block that ends last before the line stands for it.
func TestHeaderLineIsCoveredByWhatReachesIt(t *testing.T) {
	c := &coverage{
		Tests: []*covTest{{ID: "s:Early"}, {ID: "s:Reaches"}, {ID: "s:Body"}},
		Files: map[string][]covBlock{"f.go": {{Start: 1, End: 3, Tests: []int{0}}, {Start: 5, End: 6, Tests: []int{1}}, {Start: 8, End: 8, Tests: []int{2}}}},
	}
	c.reindex()
	header := headerLine("a\nb\n\t\tcase errors.Is(err, x):\n", len("a\nb\n\t\tcase "))
	got := c.covering("f.go", 7, 7, header, nil)
	if !header || len(got) != 1 || !got["s:Reaches"] {
		t.Errorf("the tests of a header line at 7 (header %v): %v, want s:Reaches", header, got)
	}
	if other := c.covering("f.go", 7, 7, headerLine("var x = \"lit\"\n", 9), nil); len(other) != 0 {
		t.Errorf("a line in no block that is no header is covered by %v", other)
	}
}

// TestScopeIsTheLedgersPackage: a ledger outside check and lower is judged
// by its own package's tests alone, so the coverage pass records that
// package's blocks and runs only its tests (M1: under ergoSuites a
// runtime/v1 mutant was uncovered, after a pass over every check, lower and
// std test and every example). The pass's work is the suites and tools it
// runs, counted here; check and lower keep ergoSuites, and two ledgers
// take the union, each suite once.
func TestScopeIsTheLedgersPackage(t *testing.T) {
	root := t.TempDir()
	var own Config
	addScope(&own, scopeOf(root, ledgerPath(root, filepath.Join(root, "runtime", "v1"))))
	if !slices.Equal(own.Cover, []string{"./runtime/v1"}) || len(own.Tools) != 0 || len(own.Suites) != 1 || own.Suites[0].Name != "runtime/v1" || own.Suites[0].Package != "./runtime/v1" {
		t.Errorf("runtime/v1's scope: cover %q, %d tools, suites %+v", own.Cover, len(own.Tools), own.Suites)
	}
	var both Config
	addScope(&both, scopeOf(root, ledgerPath(root, "lower")))
	addScope(&both, scopeOf(root, ledgerPath(root, "check")))
	addScope(&both, scopeOf(root, ledgerPath(root, filepath.Join(root, "runtime", "v1"))))
	cover, tools, suites := ergoSuites()
	var names []string
	for _, s := range both.Suites {
		names = append(names, s.Name)
	}
	want := []string{}
	for _, s := range suites {
		want = append(want, s.Name)
	}
	want = append(want, "runtime/v1")
	if !slices.Equal(both.Cover, append(cover, "./runtime/v1")) || len(both.Tools) != len(tools) || !slices.Equal(names, want) {
		t.Errorf("lower, check and runtime/v1: cover %q, %d tools, suites %q; want suites %q", both.Cover, len(both.Tools), names, want)
	}
}
