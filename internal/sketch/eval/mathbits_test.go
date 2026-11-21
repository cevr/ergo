package eval

import "testing"

// TestGoBitConversionsRunAtCheckTime: math's bit conversions run at check
// time, read through `import go math` (a host package's function, the
// binding's extern of the same target) or through a program's own
// extern, and give Go's bits. Before MX-EVMATHBITS each was skipped ("a
// Go function (Float64bits), even a pure one"), so cborpack's
// NegativesAndOtherItems never ran at check time; there a skip agrees
// with the build, so no example verdict reaches the mutants it kills
// (testdata/mutants.json): evmathbits-host-excluded,
// evmathbits-float64-missing, evmathbits-frombits-missing.
func TestGoBitConversionsRunAtCheckTime(t *testing.T) {
	pkg := checked(t, `import go math

extern go math.Float64frombits func fromBits(b uint64) float64

test ImportGo {
	assert math.Float64bits(1.0) == 0x3ff0000000000000
	assert math.Float32bits(float32(1.0)) == uint32(0x3f800000)
	assert math.Float32frombits(uint32(0x40000000)) == float32(2.0)
}

test ProgramExtern {
	assert fromBits(0x4000000000000000) == 2.0
	assert math.Float64frombits(math.Float64bits(-0.5)) == -0.5
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ImportGo": true, "ProgramExtern": true}
	seen := 0
	for _, r := range rep.Results {
		if !want[r.Name] {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("%s: %s (%s), want passed", r.Name, r.Status, r.Reason)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of the %d tests reported: %+v", seen, len(want), rep.Results)
	}
}
