package irspike

import "testing"

// TestIRCodecWitnessesMatchSource is the seeded property over generic
// bodies whose type parameters are under the codec constraint (PORT-WITNESS,
// GenerateCodecWitness): each program prints the same under both
// lowerings, and each site has an IR body. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because the codec witness locals, the generic codec
// functions that take them and the failures their Dec fields carry show
// only when the compiled program runs them.
func TestIRCodecWitnessesMatchSource(t *testing.T) {
	f7Property(t, "TestIRCodecWitnessesMatchSource", GenerateCodecWitness, codecWitnessSites...)
}
