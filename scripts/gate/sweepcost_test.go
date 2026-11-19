package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStaleSweepRewritesItsRow: a gate run whose sweep outran its record
// by more than sweepStaleFactor notes it on the step and rewrites the
// row from the run once every step has ended, so the ledger corrects
// itself (GS23, orchestrator 2026-11-15). The sweep is a step that sweeps
// nothing (true) with a record of a nanosecond. It catches
// gs23-stale-not-collected and gs23-rewrite-skipped.
func TestStaleSweepRewritesItsRow(t *testing.T) {
	root := fixture(t)
	write(t, root, sweepCostFile, `{"p": {"run": 1e-9, "sweep": 1e-9}}`)
	steps := []*Step{{Name: "closure sweep ./p", Argv: []string{"true"}, Sweep: "p"}}
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 || !strings.Contains(out, "more than 3 times its recorded") || !strings.Contains(out, "rewrote the rows of p") {
		t.Fatalf("exit %d; want a pass with the stale note and the rewrite\n%s", code, out)
	}
	data, err := os.ReadFile(filepath.Join(root, sweepCostFile))
	if err != nil {
		t.Fatal(err)
	}
	table, err := readSweepCosts(root)
	if err != nil || table["p"].Sweep < 1 {
		t.Fatalf("the row of p after the run: %s (%v); want the run's seconds, at least 1", data, err)
	}
}

// TestSweepCostChecksTheRecordAgainstTheRun: a sweep's record is stale when
// its run took more than sweepStaleFactor times the recorded sweep, and a
// package with no record is stale once its run took a third of the hang
// floor; the note names the record command. A recorded sweep weighs the
// step at least its recorded wall (sweepWeight), so the step's timeout,
// hangDeadline of its weight, outlasts it: internal/sketchfront recorded 2
// s of tests and swept in 16 to 30 minutes (GS23). A stale row is rewritten
// from the run, scaled (rewriteStaleSweeps). It catches gs23-stale-never,
// gs23-stale-unrecorded-silent, gs23-weight-ignores-record and
// gs23-rewrite-unscaled (scripts/gate's ledger).
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
	root := t.TempDir()
	write(t, root, sweepCostFile, `{"a": {"run": 36, "sweep": 81}, "b": {"run": 5, "sweep": 9}}`)
	dirs, err := rewriteStaleSweeps(root, map[string]float64{"a": 1800, "c": 999.5})
	if err != nil || !slices.Equal(dirs, []string{"a", "c"}) {
		t.Fatalf("rewriteStaleSweeps = %v, %v; want [a c]", dirs, err)
	}
	table, err := readSweepCosts(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]sweepCost{"a": {Run: 800, Sweep: 1800}, "b": {Run: 5, Sweep: 9}, "c": {Run: 1000, Sweep: 1000}}
	if !maps.Equal(table, want) {
		t.Fatalf("rewritten table %v; want %v (a scaled, b kept, c new)", table, want)
	}
	durations := map[string]map[string]float64{"m/internal/sketchfront": {"TestA": 0.5}}
	if got := sweepWeight(durations, "m/internal/sketchfront", recorded); got != 1200 {
		t.Errorf("sweepWeight with a recorded sweep of 1200 s = %v; want 1200", got)
	}
	if got := sweepWeight(durations, "m/internal/sketchfront", sweepCost{}); got != sweepWeightFactor*0.5 {
		t.Errorf("sweepWeight with no record = %v; want %v", got, sweepWeightFactor*0.5)
	}
}
