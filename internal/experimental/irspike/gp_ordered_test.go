package irspike

import "testing"

// TestIROrderedMatchSource is the seeded property over generic functions
// whose type parameter is constrained by ordered (GP-ORDER,
// GenerateOrdered): <, <=, > and >= on the parameter, instantiated at int,
// int64, float64 (a NaN, -0 and +0 among the values) and string, and std
// cmp.Compare and cmp.Less on the same values. Each program prints the
// same under both lowerings, and the generic functions have IR bodies. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the patched compile's decoder judges the ordering
// on the type parameter (types2's type set of the generated ordered
// constraint), which only the patched compile checks, and an operator
// lowered wrong shows only in the compiled program's output.
//
// Mutants: gp-order-refused.
func TestIROrderedMatchSource(t *testing.T) {
	f7Property(t, "TestIROrderedMatchSource", GenerateOrdered, orderedFuncs...)
}
