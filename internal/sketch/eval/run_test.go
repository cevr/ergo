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
// (runtime.md, Tests 3); a loop that never ends exhausts its step budget
// and is incomplete, never passed, and the obligations after it run with
// a whole budget each. Mutants: defect-continues, fuel-ignored (the loop
// never ends: the test's deadline kills it), budget-shared.
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
	want = []Status{Incomplete, Passed, Passed}
	for i, res := range rep.Results {
		if i >= len(want) || res.Status != want[i] {
			t.Fatalf("results %+v, want statuses %v", rep.Results, want)
		}
		if res.Steps > rep.Limit {
			t.Fatalf("%s used %d steps of %d", res.Name, res.Steps, rep.Limit)
		}
	}

}

// TestPreflightAdmitsWhatRuns is the smallest repro of a fixed bug: the
// preflight skipped an obligation that reaches encode or decode, or a
// program's extern of a Go function the evaluator calls, though the run
// itself evaluates both (O18, O20), so 42 example obligations were
// skipped. Mutants: oracle-o23-preflight-codec,
// oracle-o23-preflight-go-extern.
func TestPreflightAdmitsWhatRuns(t *testing.T) {
	pkg := checked(t, `import json

extern go math.Log func log(x float64) float64

type P { X int, Name string }

test Codec {
	let c = json.Codec[P]{MaxBytes: 0, MaxDepth: 0}
	let p = P{X: 3, Name: "a"}
	let back = json.Decode(c, json.Encode(c, p)) catch {
	case _:
		return
	}
	assert back == p
}

test GoLog {
	assert log(1.0) == 0.0
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range rep.Results {
		if res.Status != Passed {
			t.Errorf("%s is %s (%s)", res.Name, res.Status, res.Reason)
		}
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
