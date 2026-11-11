package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func checked(t *testing.T, src string) *check.Package {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	return pkg
}

// TestRunStopsTheRun is the smallest repro of the run's stop rules: a
// defect stops the run and every later obligation is reported not run
// (runtime.md, Tests 3); a loop that never ends exhausts the step budget
// and is incomplete, never passed, and the rest is not run. Mutants:
// defect-continues, fuel-ignored (the loop never ends: the test's
// deadline kills it), incomplete-continues.
func TestRunStopsTheRun(t *testing.T) {
	pkg := checked(t, `func half(x int) int { x / x }

test Defects {
	assert half(0) == 1
}

test After {
	assert true
}

law Sampled(n int) { n == n }
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := []Status{Defect, NotRun, NotRun}
	for i, res := range rep.Results {
		if i >= len(want) || res.Status != want[i] {
			t.Fatalf("results %+v, want statuses %v", rep.Results, want)
		}
	}

	pkg = checked(t, `test Forever {
	let mut i = 0
	for i >= 0 {
		i = i + 1
	}
}

test After {
	assert true
}

law Sampled(n int) { n == n }
`)
	rep, err = Run(pkg, Budget{Steps: 10_000, Depth: 8})
	if err != nil {
		t.Fatal(err)
	}
	want = []Status{Incomplete, NotRun, NotRun}
	for i, res := range rep.Results {
		if i >= len(want) || res.Status != want[i] {
			t.Fatalf("results %+v, want statuses %v", rep.Results, want)
		}
	}
	if rep.Steps > rep.Limit {
		t.Fatalf("used %d steps of %d", rep.Steps, rep.Limit)
	}

}

// TestRunRefusesAnAbsentFootprint: a frame with no footprint is an
// internal error, never a pure frame (rows.md, Footprints 2; the plan's
// mutant absent-fact-pure).
func TestRunRefusesAnAbsentFootprint(t *testing.T) {
	pkg := checked(t, "test T {\n\tassert true\n}\n")
	delete(pkg.Info.Footprints, pkg.Tests[0].Decl)
	if _, err := Run(pkg, DefaultBudget()); err == nil {
		t.Fatal("ran a test whose footprint is absent")
	}
}
