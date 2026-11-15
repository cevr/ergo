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
