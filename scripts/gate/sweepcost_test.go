package main

import (
	"strings"
	"testing"
)

// TestSweepCostChecksTheRecordAgainstTheRun: a sweep's record is stale when
// its run took more than sweepStaleFactor times the recorded sweep, and a
// package with no record is stale once its run took a third of the hang
// floor; the note names the record command. A recorded sweep weighs the
// step at least its recorded wall (sweepWeight), so the step's timeout,
// hangDeadline of its weight, outlasts it: internal/sketchfront recorded 2
// s of tests and swept in 16 to 30 minutes (GS23). It catches
// gs23-stale-never, gs23-stale-unrecorded-silent and
// gs23-weight-ignores-record (scripts/gate's ledger).
func TestSweepCostChecksTheRecordAgainstTheRun(t *testing.T) {
	recorded := sweepCost{Run: 900, Sweep: 1200}
	for _, c := range []struct {
		recorded sweepCost
		ran      float64
		stale    bool
	}{
		{recorded, 1200, false},
		{recorded, 3 * 1200, false},
		{recorded, 3*1200 + 1, true},
		{sweepCost{}, 60, false},
		{sweepCost{}, 1800, true},
	} {
		note := sweepCostStale("internal/sketchfront", c.recorded, c.ran)
		if (note != "") != c.stale {
			t.Errorf("recorded %+v, ran %vs: note %q; want stale %t", c.recorded, c.ran, note, c.stale)
		}
		if c.stale && !strings.Contains(note, "-record-durations -sweeps internal/sketchfront") {
			t.Errorf("the note %q does not name the record command", note)
		}
	}
	durations := map[string]map[string]float64{"m/internal/sketchfront": {"TestA": 0.5}}
	if got := sweepWeight(durations, "m/internal/sketchfront", recorded); got != 1200 {
		t.Errorf("sweepWeight with a recorded sweep of 1200 s = %v; want 1200", got)
	}
	if got := sweepWeight(durations, "m/internal/sketchfront", sweepCost{}); got != sweepWeightFactor*0.5 {
		t.Errorf("sweepWeight with no record = %v; want %v", got, sweepWeightFactor*0.5)
	}
}
