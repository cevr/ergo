package irspike

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRPtrWitnessMatchSource is the seeded property over operation calls
// through a pointer to a type parameter (GeneratePtrWitness): each program
// prints the same under both lowerings, with the fold on and off, bump and
// twice have IR bodies, and no body keeps its text for "operation call
// through a pointer to a type parameter". Mutants: ir-ptr-witness-no-deref
// (a plain operation gets the pointer: the IR build fails), and
// ir-ptr-witness-mut-deref (a mut operation gets a copy: its write is lost).
//
// It builds Go because the agreement is the compiled programs'.
func TestIRPtrWitnessMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRPtrWitnessMatchSource", GeneratePtrWitness, func(rep lower.IRReport) string {
		for why := range rep.Fallback {
			if strings.Contains(why, "pointer to a type parameter") {
				return "a body keeps its text: " + why
			}
		}
		return ""
	}, ptrWitnessSites...)
}
