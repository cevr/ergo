package irspike

import "testing"

// TestIRReleasesMatchSource is the seeded property over std calls whose
// body takes the hidden release of its result (hidden arguments, the
// release; GenerateRelease): each program prints the same under both
// lowerings, and raced, summed and timed have IR bodies, so tasks.Race's
// release of the losers' values, Transform's nil release and After's
// release run from the IR. A failing seed prints the Ergo source, both
// outcomes and its replay command. Mutant ir-result-release-nil (the IR
// passes a nil release for every value) fails it: Race's losers are never
// closed.
//
// It builds Go because the release runs in the runtime's Race on the
// losing values, which only a compiled run shows.
func TestIRReleasesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRReleasesMatchSource", GenerateRelease, nil, releaseFuncs...)
}
