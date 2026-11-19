package rt

import (
	"testing"
	"testing/synctest"
)

// TestSettleEndsOnStop: a Settle of a time.Manual that waits for a member
// the domain cannot see as parked (here a receive on a raw channel) ends
// on a stop of its caller with Interrupted. The settle waits on the
// caller's park word (FU-settle-word), which Interrupt resumes. Mutant
// settle-stop-ignored (the wait looks only for a member that parks) is
// killed only here: the bubble fails on the settle left waiting.
func TestSettleEndsOnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := Root("settle")
		m := NewManual(root, epoch)
		s := NewScope(root)
		block := make(chan struct{})
		busy := Go(&s, "busy", func(*Task) (struct{}, error) {
			<-block
			return struct{}{}, nil
		})
		var got error
		settler := Go(&s, "settler", func(c *Task) (struct{}, error) {
			got = m.Settle(c)
			return struct{}{}, nil
		})
		synctest.Wait()
		_ = settler.Stop()
		if got != Interrupted {
			t.Fatalf("a stopped Settle returned %v; want Interrupted", got)
		}
		close(block)
		if _, err := busy.Wait(); err != nil {
			t.Fatal(err)
		}
		s.End(nil)
	})
}
