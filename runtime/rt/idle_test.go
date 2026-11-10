package rt

import (
	"testing"
	"time"
)

// TestVirtualSleepTieEndsAsSleep holds docs/spec/stdlib.md Tasks rule 8:
// a sleep whose deadline came at the instant of the stop ends as a sleep,
// not as an interruption, so a tie in tasks.Race goes to the earliest
// block. The sleeper is the clock's one member, so it parks last and fires
// its own deadline; the stop then comes in the gap before its select,
// where both are ready. Without the fired-timer check the select picks one
// of the two at random, so the 64 rounds catch it but for a chance of
// 2^-64. No Race test reaches the gap: at GOMAXPROCS=1 nothing preempts
// the sleeper there, and only rt can set the hook (C4 mutant
// idle-tie-fix-removed).
func TestVirtualSleepTieEndsAsSleep(t *testing.T) {
	sleepGap = func(task *Task) { task.Interrupt() }
	t.Cleanup(func() { sleepGap = nil })
	for round := range 64 {
		root := Root("tie")
		v := NewAuto(root, epoch)
		if err := Sleep(root, v, time.Second); err != nil {
			t.Fatalf("round %d: a sleep whose deadline came before the stop gave %v; want nil", round, err)
		}
		if !root.StopRequested() {
			t.Fatalf("round %d: the hook did not run: the sleep did not reach the gap", round)
		}
		if got := v.Now(); !got.Equal(epoch.Add(time.Second)) {
			t.Fatalf("round %d: clock at %v; want the deadline %v", round, got, epoch.Add(time.Second))
		}
	}
}
