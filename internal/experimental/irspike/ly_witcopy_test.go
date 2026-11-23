package irspike

import "testing"

// TestIRWitnessCopiesMatchSource is the seeded property over copies for a
// known witness (G4, GenerateWitnessCopy): each program prints the same
// under both lowerings, and every copy of scaledK, areaK and chainK, and
// bumpK's for a *Box, and tallyK's and twiceK's for a Tally through a
// pointer to it, has an IR body (a copy has no witness local, so an
// IR body is one whose operation calls are direct calls); the evaluator
// prints the same. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the copies' direct calls, their failures and the
// copy-to-copy calls show only when the compiled program runs them.
func TestIRWitnessCopiesMatchSource(t *testing.T) {
	var copies []string
	for _, g := range witcopyGenerics {
		copies = append(copies, g+"Rect", g+"Sq")
	}
	f7PropertyEval(t, "TestIRWitnessCopiesMatchSource", GenerateWitnessCopy, append(copies, "bumpKBox", "tallyKTally", "twiceKTally")...)
}
