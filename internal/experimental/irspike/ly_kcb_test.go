package irspike

import "testing"

// TestIRKnownCallbacksMatchSource is the seeded property over known-callback
// variants (F7 "known-callback variant", GenerateKnownCallback): each
// program prints the same under both lowerings, the callers and the
// variants have IR bodies, and the IR calls each variant (a caller's known
// site, twice's forward to apply's). Each program also stops two tasks
// inside spin's 2^62-step loop, one through spin's variant and one
// through the uniform spin, and prints only once both stops have ended
// them (LY-POLL): a loop that does not poll hangs, and lyRun names it at
// the test's deadline. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because a plain literal's result, a variant's direct call
// of its callback, a forward to the next variant and a stop that ends a
// running loop show only when the compiled program runs them.
func TestIRKnownCallbacksMatchSource(t *testing.T) {
	lyProperty(t, "TestIRKnownCallbacksMatchSource", GenerateKnownCallback, []string{"applyKnown", "twiceKnown", "pickKnown", "mapGKnown"},
		"useApply", "useTwice", "usePick", "useMap", "useBound", "applyKnown", "twiceKnown", "pickKnown", "mapGKnown", "spin", "spinKnown")
}
