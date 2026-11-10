package main

import (
	"slices"
	"strings"
	"testing"
)

// The closure sweep runs in the full tier only (closure-tier, batch 9:
// "always in the full tier"; test-speed gate-minute: a sweep is heavy,
// classHeavy, and the fast tier sweeps nothing). The full tier sweeps
// every test package, one package per step, so no step holds the whole
// sweep (b9-fix-gate). A sweep step is found by what it runs, so the test
// reads the steps as the gate runs them.
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
	packages, _, err := testPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	// The full tier sweeps every test package, one package per step
	// (b9-fix-gate: one step over all of them passed the 15 minute test
	// timeout under load).
	var fullDirs, wantDirs []string
	for _, s := range sweepsOf(full) {
		if s.selection == "all" || strings.Contains(s.selection, ",") {
			t.Errorf("full tier: %s sweeps %q, not one package", s.step.Name, s.selection)
		}
		fullDirs = append(fullDirs, s.selection)
	}
	for _, p := range packages {
		wantDirs = append(wantDirs, p.dir)
	}
	slices.Sort(fullDirs)
	slices.Sort(wantDirs)
	if !slices.Equal(fullDirs, wantDirs) || len(fullDirs) < 30 {
		t.Errorf("full tier: closure sweep steps of %v; want one of each test package %v", fullDirs, wantDirs)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := sweepsOf(fast); len(got) != 0 {
		t.Errorf("fast tier: %d closure sweep steps; want none, as a sweep is heavy (classHeavy)", len(got))
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
