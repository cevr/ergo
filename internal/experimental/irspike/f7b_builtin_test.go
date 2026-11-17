package irspike

import "testing"

// TestIRBuiltinsMatchSource is the seeded property over calls of externs
// that bind Go's builtins (B-BUILTINS, GenerateBuiltin): append of an
// element and of a string's bytes, cap, copy and clear through a borrow,
// min, max and Delete at its value form. Each program prints the same
// under both lowerings, and the functions that call them have IR bodies.
// A failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because a builtin's call reaches Go's own ODELETE, OCOPY,
// OCLEAR, OMIN, OMAX and OCAP only in the compiler that reads the IR.
func TestIRBuiltinsMatchSource(t *testing.T) {
	f7Property(t, "TestIRBuiltinsMatchSource", GenerateBuiltin, builtinFuncs...)
}
