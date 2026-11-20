package irspike

import "testing"

// TestIRSeqValuesMatchSource is the seeded property over Seq[int] values
// that are not ranged over (GP-DEFINED, GenerateSeqValues; the Seq half of
// lane B's 6cebc4c40): a Seq moved through, called with a yield, and a
// function literal converted to Seq[int]. Each program prints the same
// under both lowerings, and the functions that use them have IR bodies. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the decoder converts the literal to the defined
// type (ergoFuncConv) and calls a value through it, which only the patched
// compile checks, and a value called or converted wrong shows only in the
// compiled program.
//
// Mutants: gp-seq-conv-refused, gp-seq-call-refused.
func TestIRSeqValuesMatchSource(t *testing.T) {
	f7Property(t, "TestIRSeqValuesMatchSource", GenerateSeqValues, seqValueFuncs...)
}
