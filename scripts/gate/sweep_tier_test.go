package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The closure sweep runs in a tier (closure-tier, batch 9; gate-tier round
// 1: "in the fast tier whenever any package's closure table changes, and
// always in the full tier"). The full tier has one step that sweeps every
// package. In the fast tier an edit of the closure table reaches the sweep
// of every package, an edit of one package's test file reaches that
// package's sweep alone, and an edit of a non-test Go file reaches none,
// so a checker edit's fast set keeps its CPU (TestFastTierReachOfAnEdit).
// A sweep step is found by what it runs, so the test reads the steps as
// the gate runs them.
//
// The sweep runs only on Linux (sweepSupported): on macOS internal/closure
// fails it as unsupported, so there neither tier has a sweep step. The test
// reads both OSes' steps on any host; it sets sweepOS, so it does not run
// in parallel.
func TestClosureSweepTier(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	root := checkoutRoot(t)
	sweepOS = "darwin"
	for tier, declare := range map[string]func(string) ([]*Step, error){fullTier: gateSteps, fastTier: fastSteps} {
		steps, err := declare(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := sweepsOf(steps); len(got) != 0 {
			t.Errorf("darwin, %s tier: %d closure sweep steps; want none, as the sweep cannot run off Linux", tier, len(got))
		}
	}
	sweepOS = "linux"
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := sweepsOf(full); len(got) != 1 || got[0].selection != "all" {
		t.Errorf("full tier: closure sweep steps %v; want one that sweeps every package (all)", got)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	packages, _, err := testPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]*Step{}
	for _, s := range sweepsOf(fast) {
		if s.selection == "all" || strings.Contains(s.selection, ",") || s.step.Package == nil {
			t.Errorf("fast tier: %s is not a keyed sweep of one package (%s)", s.step.Name, s.selection)
			continue
		}
		byDir[s.selection] = s.step
	}
	for _, p := range packages {
		if byDir[p.dir] == nil {
			t.Errorf("fast tier: no closure sweep of %s", p.dir)
		}
	}
	reached := func(file string) []string {
		var dirs []string
		for dir, step := range byDir {
			if step.Package.Covers(file) {
				dirs = append(dirs, dir)
			}
		}
		slices.Sort(dirs)
		return dirs
	}
	table := "internal/closure/closure_test.go"
	if got := reached(table); len(got) != len(packages) || len(got) < 30 {
		t.Errorf("an edit of %s reaches the sweeps of %d of %d packages; want every one", table, len(got), len(packages))
	}
	// A test file of each of two packages, and the non-test files beside
	// them.
	for _, dir := range []string{"internal/residue", "internal/sketch/check"} {
		tests, err := filepath.Glob(filepath.Join(root, dir, "*_test.go"))
		if err != nil || len(tests) == 0 {
			t.Fatalf("%s: no test file (%v)", dir, err)
		}
		test := dir + "/" + filepath.Base(tests[0])
		if got := reached(test); !slices.Equal(got, []string{dir}) {
			t.Errorf("an edit of %s reaches the sweeps of %v; want only %s", test, got, dir)
		}
		sources, _ := filepath.Glob(filepath.Join(root, dir, "*.go"))
		for _, source := range sources {
			if strings.HasSuffix(source, "_test.go") {
				continue
			}
			name := dir + "/" + filepath.Base(source)
			if got := reached(name); len(got) > 0 {
				t.Errorf("an edit of %s, not a test file, reaches the sweeps of %v; want none", name, got)
			}
		}
	}
}

// sweep is a step that runs the closure sweep, with what it sweeps.
type sweep struct {
	step      *Step
	selection string
}

// sweepsOf are the steps that run the closure sweep: go test of
// internal/closure's TestEveryPackageRecordsOnlyItsClosure with
// ERGO_CLOSURE_SWEEP set.
func sweepsOf(steps []*Step) []sweep {
	var out []sweep
	for _, step := range steps {
		if !runsGoTest(step) || !slices.Contains(step.Argv, "./internal/closure") || !slices.Contains(step.Argv, "^TestEveryPackageRecordsOnlyItsClosure$") {
			continue
		}
		for _, v := range step.Env {
			if selection, ok := strings.CutPrefix(v, "ERGO_CLOSURE_SWEEP="); ok {
				out = append(out, sweep{step, selection})
			}
		}
	}
	return out
}
