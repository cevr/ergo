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

// TestRunReportsTheSkipsReason: skip "reason" ends a test, a property at
// the case that reaches it, and a law run, each skipped with the test's
// own reason and the statement's line (runtime.md, Tests 4), and the
// other obligations run (the tests in source order, then the laws).
// Mutant: tskip-eval-reason (the skip's reason is not the test's).
func TestRunReportsTheSkipsReason(t *testing.T) {
	pkg := checked(t, `

test Host {
	skip "git is not on this host"
}

test Drawn(n int) {
	if n > 3 {
		skip "past three"
	}
	assert n + n == 2 * n
}

law Sum(n int) {
	if n < 0 {
		skip "not for negatives"
	}
	n + n == 2 * n
}

test After {
	assert true
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		status Status
		reason string
		line   int
	}{{Skipped, "git is not on this host", 4}, {Skipped, "past three", 9}, {Passed, "", 0}, {Skipped, "not for negatives", 16}}
	if len(rep.Results) != len(want) {
		t.Fatalf("results %+v, want %d", rep.Results, len(want))
	}
	for i, w := range want {
		r := rep.Results[i]
		if r.Status != w.status || r.Reason != w.reason || (w.line != 0 && r.Site.Line != w.line) {
			t.Errorf("%s: %s %q at line %d, want %s %q at line %d", r.Name, r.Status, r.Reason, r.Site.Line, w.status, w.reason, w.line)
		}
	}
}

// TestPreflightAdmitsWhatRuns is the smallest repro of a fixed bug: the
// preflight skipped an obligation that reaches encode or decode, or a
// program's extern of a Go function the evaluator calls, though the run
// itself evaluates both (O18, O20), so 42 example obligations were
// skipped. GoStd: a pure function of Go's standard library through
// `import go` runs as the same Go function; compiler/syntax's scanner
// calls these, and its tests were skipped (wf10 TP-DIAG). Mutants:
// oracle-o23-preflight-codec, oracle-o23-preflight-go-extern,
// tpdiag-import-go-skipped, tpdiag-isletter-is-isdigit.
func TestPreflightAdmitsWhatRuns(t *testing.T) {
	pkg := checked(t, `import json
import go strconv
import go unicode

extern go math.Log func log(x float64) float64

test GoStd {
	assert unicode.IsLetter('é') && !unicode.IsLetter('1') && unicode.IsDigit('7') && !unicode.IsDigit('a')
	assert strconv.QuoteRune('\u00a0') == "'\\u00a0'" && strconv.Quote("a\"") == "\"a\\\"\""
	assert strconv.Itoa(-3) == "-3" && strconv.FormatInt(255, 16) == "ff"
}

type P { X int, Name string }

test Codec {
	let c = json.Codec[P]{MaxBytes: 0, MaxDepth: 0}
	let p = P{X: 3, Name: "a"}
	let text = json.Encode(c, p)
	assert text != ""
	let back = json.Decode(c, text) catch {
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
