package eval

import "testing"

// TestCoveredFailurePassesCatch is the smallest repro of a fixed bug
// (found by batch 17 fix-try r2's probe, ORACLE O2): under `try X catch`,
// the catch takes X's own failure, and a failure covered in a value
// position inside X (an argument) passes it and leaves the frame (rows.md,
// Catch 5). The evaluator caught the argument's failure, so check time
// refuted the tests ergo test passes; the built program gives the values
// asserted here (ergo test: ok Arg, ok With).
//
// Mutant only it kills (testdata/mutants.json): oracle-o2-covered-failure-caught.
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
