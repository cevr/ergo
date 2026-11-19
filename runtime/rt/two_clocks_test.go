package rt

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestAutoClockMovesBesideAnotherClock is the smallest repro of P3-9
// TWO-CLOCKS (wf10/research/P3 F4; evidence/two-clocks-deadlock.ergo): a
// task that made an auto clock (testing.VirtualClock) and then a manual
// clock (time.Manual) that nothing uses starts a child that sleeps on the
// auto clock and joins it. Every task waits on a wait the auto clock sees,
// so the clock moves and the sleep ends. Before the fix the second clock
// took the task out of the auto clock's domain, the auto clock had no
// member left and never moved, and the program deadlocked (30 of 30). The
// bubble turns that deadlock into a failure here. Mutant
// second-clock-leaves-domain (the maker leaves its domain for the new
// clock's) is killed only here.
func TestAutoClockMovesBesideAnotherClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := Root("two clocks")
		auto := NewAuto(root, epoch)
		_ = NewManual(root, epoch)
		s := NewScope(root)
		h := Go(&s, "sleeper", func(c *Task) (struct{}, error) {
			return struct{}{}, Sleep(c, auto, time.Second)
		})
		if _, err := h.Wait(); err != nil {
			t.Fatalf("the sleep on the auto clock ended with %v; want nil", err)
		}
		s.End(nil)
		if got, want := auto.Now(), Instant(epoch.Add(time.Second).UnixNano()); got != want {
			t.Fatalf("auto clock at %v; want %v", got, want)
		}
	})
}

// TestDomainMovesNearestClock: with two auto clocks of one domain armed,
// the one whose deadline is nearest its own now moves first, and the task
// it wakes runs to its end before the other moves. Here the second clock
// made fires first (1 s against 10 s). Mutant idle-moves-first-clock (the
// first clock made with a timer moves) is killed only here.
func TestDomainMovesNearestClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := Root("two auto clocks")
		far := NewAuto(root, epoch)
		near := NewAuto(root, epoch)
		var mu sync.Mutex
		var order []string
		s := NewScope(root)
		sleeper := func(name string, v *Virtual, d time.Duration) *Handle[struct{}] {
			return Go(&s, name, func(c *Task) (struct{}, error) {
				if err := Sleep(c, v, d); err != nil {
					return struct{}{}, err
				}
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return struct{}{}, nil
			})
		}
		a := sleeper("far", far, 10*time.Second)
		b := sleeper("near", near, time.Second)
		for _, h := range []*Handle[struct{}]{a, b} {
			if _, err := h.Wait(); err != nil {
				t.Fatal(err)
			}
		}
		s.End(nil)
		if len(order) != 2 || order[0] != "near" || order[1] != "far" {
			t.Fatalf("woke in the order %v; want [near far]", order)
		}
	})
}
