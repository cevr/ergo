package irspike

import "testing"

// TestIRWireFormsMatchSource is the seeded property over std json's
// declared wire forms written on cell writers (PORT-WIRE r2,
// GenerateWireForms): the bare Value, the number-text Num, the members
// Object and the nullable instances Nullable[int] and Nullable[time.Time].
// Each program prints the same under both lowerings, the callers have IR
// bodies, and no codec function on a cell writer keeps its text body. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the forms' events reach the writer's state only
// when the compiled program runs the codec functions.
func TestIRWireFormsMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRWireFormsMatchSource", GenerateWireForms, cellCodecText, formSites...)
}
