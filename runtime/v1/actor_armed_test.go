package v1

import (
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// heldSleeper is a Sleeper whose sleep registers (Done runs WhenWaiting's
// hook), records its task and ends only on a stop.
type heldSleeper struct{ child atomic.Pointer[rt.Task] }

func (h *heldSleeper) Sleep(t *rt.Task, _ time.Duration) {
	h.child.Store(t)
	<-t.Done()
	panic(rt.Interrupted)
}

// TestArmReturnsOnceTheSleepRegisters holds rule 7's handshake on the
// runner's park word (FU-actor-timer-slot): the start of an actor whose
// first state has a timer returns once the timer child's sleep has
// registered, which resumes the runner. The bubble's Wait is the event:
// when every goroutine waits, the start has returned or never will.
// Mutant arm-skips-resume (the registration resumes no runner) fails its
// assertion, not a deadline; the test then resumes the runner itself so
// the bubble ends.
func TestArmReturnsOnceTheSleepRegisters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := rt.Root("arm")
		sl := &heldSleeper{}
		var a *Actor[[]int, int, rdec]
		var started atomic.Bool
		go func() {
			a = MachineStart(Site("armed"), root, sl, nil, nil,
				func(_ *rt.Task, s []int, e int) (rdec, error) { return rdec{decideStay, e}, nil },
				func(*rt.Task, []int, []int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
				func([]int) time.Duration { return time.Second }, noWork[[]int], hasNoWork, runNoWork[int],
				func(d rdec) int { return d.kind },
				func(s []int, d rdec) []int { return append(slices.Clone(s), d.e) },
				func(*rt.Task, rdec) {}, 1)
			started.Store(true)
		}()
		synctest.Wait()
		if !started.Load() {
			t.Errorf("the start of an actor with a timer did not return once the timer's sleep registered")
			if c := sl.child.Load(); c != nil {
				c.Parent().Resume() // the runner, so the bubble ends
			}
			synctest.Wait()
		}
		if a != nil {
			if err := a.Stop(rt.Root("stop")); err != nil {
				t.Fatal(err)
			}
		}
	})
}
