package irspike

import "testing"

// TestIRMonoCopiesMatchSource is the seeded property over generic calls
// at known types (FLIP F7 PORT-COPY, GenerateMonoCopies): each program
// builds and prints the same under both lowerings, and describe, label,
// count, pairs and Box's pick have IR bodies, so the IR calls the generic
// bodies where the emitter calls its monomorphic copies. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the question is what the two builds print: the
// text build runs the copies, the IR build the generic bodies.
func TestIRMonoCopiesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRMonoCopiesMatchSource", GenerateMonoCopies, nil, "Dog.describe", "label", "count", "pairs", "Box.pick")
}
