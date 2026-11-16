package irspike

import "testing"

// TestIRIndexesMatchSource is the seeded property over indexes of values
// that are no slice (F7, GenerateIndex): a string's byte and an array's
// element, by index and by a for-in. Each program prints the same under
// both lowerings, and the functions that index have IR bodies. A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the decoder's element type of the index (a byte,
// an array's element) is checked only by the patched compile, and a
// position read wrong shows only in the compiled program.
func TestIRIndexesMatchSource(t *testing.T) {
	f7Property(t, "TestIRIndexesMatchSource", GenerateIndex, "code", "same", "at", "total")
}
