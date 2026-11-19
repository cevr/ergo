package rt

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestSleepEndsOnStop: a Sleep on a time.Manual that nothing advances ends
// on a stop of its task with Interrupted: the sleep waits on the task's
// park word (FU-virtual-sleep), which Interrupt resumes. The bubble's Wait
// is the event: when every goroutine waits, the sleep has ended or never
// will. Mutant vsleep-ignores-stop (the wait looks only for its deadline)
// fails its assertion, not a deadline; the test then advances the clock
// so the bubble ends.
func TestSleepEndsOnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := Root("test")
		m := NewManual(root, epoch)
		s := NewScope(root)
		var sleeper atomic.Pointer[Task]
		var ended atomic.Bool
		var got error
		h := Go(&s, "sleeper", func(c *Task) (struct{}, error) {
			sleeper.Store(c)
			got = Sleep(c, m, time.Hour)
			ended.Store(true)
			return struct{}{}, nil
		})
		m.AwaitTimers(1)
		sleeper.Load().Interrupt()
		synctest.Wait()
		if !ended.Load() {
			t.Errorf("a stopped Sleep did not end before its deadline")
			m.Advance(Span(time.Hour)) // so the bubble ends
		} else if got != Interrupted {
			t.Errorf("a stopped Sleep returned %v; want Interrupted", got)
		}
		_ = h.Stop()
		s.End(nil)
	})
}
