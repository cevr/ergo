package irspike

import "testing"

// TestIREqualTestsMatchSource is the seeded property over tests whose
// asserts compare values of types that are not basic (GP-DEFINED,
// GenerateEqualTests: "operator on a non-basic type"): the test builds of
// both paths report the same passes and failures, with the same shown
// operands, and every test has an IR body. A failing seed prints the Ergo
// source, both outcomes and its replay command. It builds Go for the
// reason needsBuild gives: an equality the decoder writes wrong shows only
// when the patched compile checks it or the test binary runs it.
//
// Mutants: gp-eq-assert-refused, gp-eq-assert-bin, gp-eq-branch-refused,
// gp-eq-variant-refused, gp-eq-variant-shown-converted.
func TestIREqualTestsMatchSource(t *testing.T) {
	testBuildsAgree(t, "TestIREqualTestsMatchSource", GenerateEqualTests, "ergoTest", equalTestBodies, "FAIL ", "ok ")
}
