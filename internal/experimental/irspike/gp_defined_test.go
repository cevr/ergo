package irspike

import "testing"

// TestIRDefinedMatchSource is the seeded property over values of a type
// defined over an array (GP-DEFINED, GenerateDefined; lane B's 6cebc4c40,
// its array half): an element read by index, the zero Quad{} and elements
// written into it. Each program prints the same under both lowerings, and
// the functions that use them have IR bodies. A failing seed prints the
// Ergo source, both outcomes and its replay command.
//
// It builds Go because the decoder reads the element through the defined
// type and spells its zero, which only the patched compile checks, and a
// value read or written wrong shows only in the compiled program.
//
// Mutants: gp-defined-array-refused, gp-defined-array-index-refused,
// gp-defined-array-zero-refused.
func TestIRDefinedMatchSource(t *testing.T) {
	f7Property(t, "TestIRDefinedMatchSource", GenerateDefined, definedFuncs...)
}
