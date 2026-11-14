package eval

import "testing"

// TestCatchPassesCoveredFailure is the smallest repro of the TRY-eval bug
// (batch-17 integrator finding): in `try h(g()) catch { case X: 0 }` the
// try covers g(), so g's failure propagates past the catch, which handles
// h's row only (rows.md, Catch 5). The evaluator let the catch take it
// and r() returned 0; the lowered Go returns g's failure.
//
// Mutant it kills (testdata/mutants.json): try-eval-catch-takes-covered.
func TestCatchPassesCoveredFailure(t *testing.T) {
	pkg := checked(t, `type X { At string }

func g(fail bool) int fails X {
	if fail {
		return X{At: "g"}
	}
	return 1
}

func h(v int) int fails X {
	if v < 0 {
		return X{At: "h"}
	}
	return v
}

func r(fail bool) int fails X {
	try h(g(fail)) catch {
	case X:
		0
	}
}

func shown(fail bool) string {
	let v = r(fail) catch {
	case X as e:
		return e.At
	}
	return "\(v)"
}

test Covered {
	assert shown(true) == "g"
	assert shown(false) == "1"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != Passed {
		t.Fatalf("results %+v, want Covered passed", rep.Results)
	}
}

// TestCoveredFailurePassesCatch is the smallest repro of a fixed bug
// (found by batch 17 fix-try r2's probe, ORACLE O2): under `try X catch`,
// the catch takes X's own failure, and a failure covered in a value
// position inside X (an argument) passes it and leaves the frame (rows.md,
// Catch 5). The evaluator caught the argument's failure, so check time
// refuted the tests ergo test passes; the built program gives the values
// asserted here (ergo test: ok Arg, ok With).
//
// One rule decides it, ownFailure (expr.go; batch-19 merge of ORACLE O2
// with batch 17's fix-try): this repro adds the with block's tail, where
// the caught expression's own call is inside a with.
func TestCoveredFailurePassesCatch(t *testing.T) {
	pkg := checked(t, `type Bad { At string }

type Probe {
	Ping() int,
}

type Live {}

func Ping(_ Live) int { 1 }

let Wired = context { Probe: Live }

func f(tag string, fail bool) int fails Bad {
	if fail {
		return Bad{At: tag}
	}
	return 3
}

func pass(v int) int fails Bad {
	if v == 7 {
		return Bad{At: "pass"}
	}
	return v
}

func arg() int fails Bad {
	let r = try pass(f("w", true)) catch {
	case Bad:
		-1
	}
	return r
}

func inWith() int fails Bad {
	let r = try with Wired { pass(f("b", true)) } catch {
	case Bad:
		-1
	}
	return r
}

func show(n int) string {
	let r = (if n == 0 { arg() } else { inWith() }) catch {
	case Bad as e:
		return "failed at " + e.At
	}
	return "\(r)"
}

test Arg {
	assert show(0) == "failed at w"
}

test With {
	assert show(1) == "failed at b"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results %+v", rep.Results)
	}
	for _, r := range rep.Results {
		if r.Status != Passed {
			t.Errorf("%s is %s (%s), want passed", r.Name, r.Status, r.Reason)
		}
	}
}
