package irspike

import "testing"

// TestIRRefinePatternsMatchSource is the seeded property over a refinement's
// pattern call on the IR (GP-PATTERN, GenerateRefinePattern): each derived
// Parse takes and refuses the same strings under both lowerings, and it
// and its callers have IR bodies. A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the question is the run of the matcher the IR
// body calls; only the compiled program runs it.
//
// Mutant gp-pattern-gated: the IR keeps "call kind 13" (a pattern call
// falls back), so ParseT0 has no IR body.
func TestIRRefinePatternsMatchSource(t *testing.T) {
	f7Property(t, "TestIRRefinePatternsMatchSource", GenerateRefinePattern, refinePatternFuncs...)
}
