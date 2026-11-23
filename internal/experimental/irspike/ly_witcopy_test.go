package irspike

import (
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWitnessCopiesMatchSource is the seeded property over copies for a
// known witness (G4, GenerateWitnessCopy): each program prints the same
// under both lowerings and the evaluator. Every copy of scaledK, areaK,
// chainK, tallyK and twiceK has an IR body (a copy has no witness local,
// so an IR body is one whose operation calls are direct calls). A
// failing seed prints the source, the outcomes and its replay command.
//
// It builds Go because the copies' direct calls, their failures and the
// copy-to-copy calls show only when the compiled program runs them.
func TestIRWitnessCopiesMatchSource(t *testing.T) {
	var copies []string
	for _, g := range witcopyGenerics {
		copies = append(copies, g+"Rect", g+"Sq")
	}
	copies = append(copies, "tallyKTally", "twiceKTally")
	for i, name := range copies {
		copies[i] = lower.HelperSpelling("", name)
	}
	f7PropertyEval(t, "TestIRWitnessCopiesMatchSource", GenerateWitnessCopy, copies...)
}
