package main

import (
	"slices"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
)

// The tier main computes reaches the gate it sets up (setUpRun): the steps
// are that tier's, the gate names it, and the facts its record keys
// (collect, through collectTier) hold it as the tier setting, the value the
// replay before the scope keys too (replayWholeGate). -record-durations
// sets up a gate with no tier and no step. Before, main set the tier on
// the gate apart from the steps, and a mutant that dropped it survived
// every test (gate-shard round 1, mutant v3).
func TestGateTierReachesReplay(t *testing.T) {
	root := checkoutRoot(t)
	t.Setenv("ERGO_GATE_CACHE", t.TempDir())
	for _, tier := range []string{fastTier, fullTier} {
		g, err := setUpRun("", "", tier, false)
		if err != nil {
			t.Fatal(err)
		}
		if g.tier != tier {
			t.Errorf("a %s run sets up a gate of tier %q", tier, g.tier)
		}
		want, err := stepsFor(root, tier)
		if err != nil {
			t.Fatal(err)
		}
		if names, wantNames := stepNames(g.steps), stepNames(want); !slices.Equal(names, wantNames) {
			t.Errorf("a %s run declares %d steps, the tier has %d", tier, len(names), len(wantNames))
		}
		g.whole = &wholeGate{mechanism: "test"}
		facts, err := g.collect()
		if err != nil {
			t.Fatal(err)
		}
		recorded := tierOf(facts)
		if recorded != tier {
			t.Errorf("a %s run keys its record with the tier %q", tier, recorded)
		}
		replayed, err := (&gate{root: g.root, cacheDir: g.cacheDir, tier: tier}).collectTier("test")
		if err != nil {
			t.Fatal(err)
		}
		if tierOf(replayed) != recorded {
			t.Errorf("a %s run records under the tier %q and replays under %q", tier, recorded, tierOf(replayed))
		}
	}
	g, err := setUpRun("", "", fullTier, true)
	if err != nil {
		t.Fatal(err)
	}
	if g.tier != "" || len(g.steps) != 0 {
		t.Errorf("-record-durations sets up a gate of tier %q with %d steps; want none", g.tier, len(g.steps))
	}
}

// tierOf is the tier setting of facts, or "" with none.
func tierOf(facts *replay.Facts) string {
	for _, v := range facts.Settings {
		if v.Name == tierSetting && v.Set {
			return v.Value
		}
	}
	return ""
}

func stepNames(steps []*Step) []string {
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	return names
}
