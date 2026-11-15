package closure

import (
	"math"
	"testing"
	"time"
)

// TestRunDeadlineFollowsRecordedCPU: a run's deadline is ten times its
// recorded cost, at least 30 minutes, and its cost is the CPU testCPUFile
// records for its tests where that is more than their wall (gate16b-fix3:
// two runs of internal/sketch/lower stopped at a fixed 19m30s under load).
// Mutants: withDeadlineCosts keeping the wall cost; runDeadline ignoring
// the cost.
func TestRunDeadlineFollowsRecordedCPU(t *testing.T) {
	t.Setenv(runDeadlineVariable, "")
	plan := withDeadlineCosts([]sweepRun{
		{tests: []string{"TestBuilds", "TestPure"}, cost: 45},
		{tests: []string{"TestUnrecorded"}, cost: 20},
	}, map[string]float64{"TestBuilds": 290, "TestPure": 10})
	if plan[0].deadlineCost != 300 || plan[1].deadlineCost != 20 {
		t.Fatalf("deadline costs %v and %v; want 300 (the CPU) and 20 (the wall)", plan[0].deadlineCost, plan[1].deadlineCost)
	}
	got, err := runDeadline(plan[0].deadlineCost)
	if err != nil {
		t.Fatal(err)
	}
	want := 50 * time.Minute
	if end, ok := binaryDeadline(); ok {
		want = max(min(want, time.Until(end)-runReportMargin), time.Second)
	}
	if diff := got - want; diff > time.Second || diff < -time.Second {
		t.Errorf("runDeadline(300) = %v; want %v", got, want)
	}
	if floor, err := runDeadline(20); err != nil || (floor < 30*time.Minute && want == 50*time.Minute) {
		t.Errorf("runDeadline(20) = %v, %v; want at least 30m", floor, err)
	}
}

// TestUnmeasuredRunHasTheBinaryDeadline: a run with a test that has no
// recorded time is bounded by the test binary's own deadline, which the
// gate sets from the package's record, not by the 30 minute floor (CS1:
// TestDirectMatchesGoBuild, new and unrecorded, outran it). runDeadline
// then takes the binary's (TestRunDeadlineFollowsRecordedCPU). Mutants
// sweep-unmeasured-floored and sweep-unmeasured-hang-floored (this
// package's ledger).
func TestUnmeasuredRunHasTheBinaryDeadline(t *testing.T) {
	plan := withDeadlineCosts([]sweepRun{
		{tests: []string{"TestNew", "TestOld"}, cost: 45, unmeasured: []string{"TestNew"}},
	}, map[string]float64{"TestOld": 10})
	if got := hangDeadline(plan[0].deadlineCost); got != math.MaxInt64 {
		t.Fatalf("the unmeasured run's own bound is %v (cost %v); want none", got, plan[0].deadlineCost)
	}
}
