package rt

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// TestSettleEndsWhenAMemberLeaves: a Settle of a time.Manual that waits
// for a member the domain cannot see as parked ends, with nil, once that
// member leaves the domain and the clock's maker parks: each wakes the
// settle (FU-settle-word). The test's own task is no member, so the
// bubble's Wait is the event: when every goroutine waits, the settle has
// ended or it never will. Mutants settle-skips-resume (a leave or a park
// wakes no settle) and settles-not-bumped (a woken settle does not see
// that a member left) fail its assertion, not a deadline.
func TestSettleEndsWhenAMemberLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := Root("test")
		block := make(chan struct{})
		var ended atomic.Bool
		var got error
		s := NewScope(root)
		maker := Go(&s, "maker", func(c *Task) (struct{}, error) {
			m := NewManual(c, epoch)
			sc := NewScope(c)
			busy := Go(&sc, "busy", func(*Task) (struct{}, error) {
				<-block
				return struct{}{}, nil
			})
			settler := Go(&sc, "settler", func(c *Task) (struct{}, error) {
				got = m.Settle(c)
				ended.Store(true)
				return struct{}{}, nil
			})
			_, err := busy.Wait()
			if err == nil {
				_, err = settler.Wait()
			}
			sc.End(&err)
			return struct{}{}, err
		})
		synctest.Wait()
		if ended.Load() {
			t.Fatalf("a Settle ended (%v) while a member ran", got)
		}
		close(block)
		synctest.Wait()
		if !ended.Load() {
			t.Errorf("a Settle did not end once the busy member left the domain and its maker parked")
			_ = maker.Stop() // so the bubble ends
		} else if got != nil {
			t.Errorf("the Settle ended with %v; want nil", got)
		}
		s.End(nil)
	})
}
