package rt

import (
	"sync/atomic"
	"testing"
	"time"
)

// settledRun is one run of settledTie: what the settled timer saw when it
// fired, and the auto clock's steps.
type settledRun struct {
	sawEnded bool    // the sleeper woken at the instant had ended
	at       Instant // Now when it fired
	fired    int32   // how many times it fired
	steps    int     // the auto clock's moves (0 on a manual clock)
}

// settledTie is the tie of PROC-RUN-FLOOR on one clock (auto or manual) in
// one arming order: a child sleeps 1 s and then ends, and a settled timer
// is armed for the same instant, before the child sleeps (settledFirst)
// or after. The root then lets the time pass 1 s and 2 s more.
func settledTie(t *testing.T, auto, settledFirst bool) settledRun {
	t.Helper()
	root := Root("settled tie")
	var v *Virtual
	if auto {
		v = NewAuto(root, epoch)
	} else {
		v = NewManual(root, epoch)
	}
	var ended atomic.Bool
	var run settledRun
	arm := func() {
		v.AfterSettledFunc(Span(time.Second), func() {
			run.sawEnded = ended.Load()
			run.at = v.Now()
			atomic.AddInt32(&run.fired, 1)
		})
	}
	if settledFirst {
		arm()
	}
	s := NewScope(root)
	h := Go(&s, "sleeper", func(c *Task) (struct{}, error) {
		if err := Sleep(c, v, time.Second); err != nil {
			return struct{}{}, err
		}
		ended.Store(true)
		return struct{}{}, nil
	})
	if !settledFirst {
		v.AwaitTimers(1) // the child's sleep is armed: the timer comes after it
		arm()
	}
	if auto {
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		// The root's sleep parks it, so the domain idles at 1 s again and
		// the settled timer fires before the time moves to 3 s.
		if err := Sleep(root, v, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		run.steps = v.Steps()
	} else {
		if err := v.AdvanceSettled(root, Span(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	s.End(nil)
	return run
}

// TestSettledTimerFiresAfterTheInstantSettles: rt.Virtual.AfterSettledFunc
// on an auto clock (testing.VirtualClock) and on a manual one
// (time.Manual's AdvanceSettled), in both arming orders. The settled
// timer fires once, at its instant, after the task woken at that instant
// ran to its end, so task.StopAfter's tie is Done on both clocks
// (runtime.md Tasks 14). Mutants: settled-fires-before-settling (fireAt
// fires settled timers with the plain ones; a race the 32 rounds catch),
// settled-fires-after-time-moved (the deadline one tick late).
func TestSettledTimerFiresAfterTheInstantSettles(t *testing.T) {
	want := Instant(epoch.Add(time.Second).UnixNano())
	for _, auto := range []bool{true, false} {
		for _, first := range []bool{true, false} {
			for round := range 32 {
				r := settledTie(t, auto, first)
				if r.fired != 1 || !r.sawEnded || r.at != want {
					t.Fatalf("auto %v, settled armed first %v, round %d: fired %d times, saw the sleeper ended %v, at %v; want once, true, %v",
						auto, first, round, r.fired, r.sawEnded, r.at, want)
				}
			}
		}
	}
}

// TestSettledTimerTakesOneStep counts the auto clock's moves (a counter,
// not a timing): with no settled timer, the run of settledTie moves once
// per instant (1 s, 3 s), as before AfterSettledFunc; with one, its
// instant takes one more step. So the idle path costs nothing more when
// no settled timer is armed (PROC-RUN-FLOOR condition 4), and two settled
// timers of one instant fire in one step.
func TestSettledTimerTakesOneStep(t *testing.T) {
	steps := func(settled int) int {
		root := Root("steps")
		v := NewAuto(root, epoch)
		for range settled {
			v.AfterSettledFunc(Span(time.Second), func() {})
		}
		s := NewScope(root)
		h := Go(&s, "sleeper", func(c *Task) (struct{}, error) {
			return struct{}{}, Sleep(c, v, time.Second)
		})
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		if err := Sleep(root, v, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		s.End(nil)
		return v.Steps()
	}
	for settled, want := range []int{2, 3, 3} {
		if got := steps(settled); got != want {
			t.Errorf("%d settled timers: the auto clock moved %d times; want %d", settled, got, want)
		}
	}
}

// TestSettledTimerAtNowRunsOnce: a settled timer armed for its own instant
// (a d of zero or less), as one that a settled function arms, runs at once
// and only once: it is no timer of the clock. Mutant
// settled-zero-fires-twice (the zero span both runs and is armed).
func TestSettledTimerAtNowRunsOnce(t *testing.T) {
	root := Root("settled zero")
	v := NewManual(root, epoch)
	var fired atomic.Int32
	v.AfterSettledFunc(Span(time.Second), func() {
		v.AfterSettledFunc(0, func() { fired.Add(1) })
	})
	if err := v.AdvanceSettled(root, Span(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("the zero-span settled timer ran %d times; want 1", got)
	}
	if n := v.Pending(); n != 0 {
		t.Fatalf("%d timers left armed; want 0", n)
	}
}
