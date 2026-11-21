package rt

import (
	"testing"
	"time"
)

// TestFireOutlivesItsSleep: a fire that comes from another task (an
// Advance) and is preempted after it marks the timer fired lets the
// sleeper see the mark, return and hand its timer on, which clears the
// timer's task (FU-sleep-timer-reuse). The fire then wakes the task it
// read before the mark. The hook holds the fire in that window until the
// sleep has returned; a fire that reads the timer's task after the mark
// wakes no task and panics (mutant fire-reads-task-late, killed only
// here). The explored scheduler cannot open the window: a fire runs
// inside one step.
func TestFireOutlivesItsSleep(t *testing.T) {
	if Exploring {
		t.Skip("under the explored scheduler a sleep's timer is never handed on")
	}
	root := Root("advancer")
	m := NewManual(root, epoch)
	returned := make(chan error)
	var sleeper *Task
	fireGap = func() {
		// A stray wake: the parked sleeper looks again, sees fired and
		// returns while the fire waits here.
		sleeper.Resume()
		if err := <-returned; err != nil {
			t.Errorf("the sleep ended with %v; want nil", err)
		}
	}
	t.Cleanup(func() { fireGap = nil })
	s := NewScope(root)
	h := Go(&s, "sleeper", func(c *Task) (struct{}, error) {
		sleeper = c // read after AwaitTimers, which the sleep's arm orders
		returned <- Sleep(c, m, time.Second)
		return struct{}{}, nil
	})
	m.AwaitTimers(1)
	m.Advance(Span(time.Second))
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	s.End(nil)
}
