package irspike

import "testing"

// TestIRArrayLitMatchSource is the seeded property over fixed array
// literals (INDEX-NONSLICE, GenerateArrayLit): a [5]int that lists every
// element, the empty [5]int{} with elements written into it, and a
// [3]string. Each program prints the same under both lowerings, and the
// functions that build them have IR bodies. A failing seed prints the
// Ergo source, both outcomes and its replay command.
//
// It builds Go because the decoder's array literal (its element count and
// each element's type) is checked only by the patched compile, and an
// element out of place shows only in the compiled program.
//
// Mutants: arraylit-refused, arraylit-empty-listed,
// arraylit-decoder-order.
func TestIRArrayLitMatchSource(t *testing.T) {
	f7Property(t, "TestIRArrayLitMatchSource", GenerateArrayLit, arrayLitFuncs...)
}
