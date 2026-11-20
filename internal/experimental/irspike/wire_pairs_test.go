package irspike

import "testing"

// TestIRWirePairsMatchSource is the seeded property over by-name pairs
// and refined types written on cell writers (PORT-WIRE,
// GenerateWirePairs): a plain pair, a pair whose base is a list, std's
// append-form time.Time, refined ints. Each program prints the same under
// both lowerings, the callers have IR bodies, and no codec function on a
// cell writer keeps its text body. A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because a pair's text and a refined value reach the
// writer's state only when the compiled program runs the codec functions.
func TestIRWirePairsMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRWirePairsMatchSource", GenerateWirePairs, cellCodecText, pairSites...)
}
