//go:build !ergoexplore

package v1

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Live-scheduler tests of timeout.After: the deadline child sleeps on the
// test's own virtual clock (virtualSleeper), outside the explored run's
// domain, so under the ergoexplore tag it waits holding the baton and the
// body never runs (TestAfterDefects's Scheduler case stays with its body
// case). The explored After is checked by the tests in timeout_test.go,
// whose clock an outside goroutine moves.

// TestAfterBodyFirst: a body that ends before the deadline gives its value
// or its typed failure, and the deadline child is stopped and drained, so
// its sleep is not left open and its timer is disarmed.
func TestAfterBodyFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    func(*rt.Task) (int, error)
		want    int
		wantErr error
	}{
		{"value", func(*rt.Task) (int, error) { return 42, nil }, 42, nil},
		{"typed failure", func(*rt.Task) (int, error) { return 0, bad{"body"} }, 0, bad{"body"}},
		{"raised failure", func(*rt.Task) (int, error) { raise(bad{"raised"}); return 1, nil }, 0, bad{"raised"}},
		// A nested After that expired fails its body with Expired: the
		// outer After passes the typed failure through unchanged.
		{"inner Expired", func(*rt.Task) (int, error) { return 0, Expired{} }, 0, Expired{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newVirtualSleeper()
			var got int
			var gotErr error
			within(t, "After with a body that ends", func() {
				err := runMain(func(root *rt.Task) error {
					// The body ends only after the deadline child has
					// registered its sleep, so After must stop and drain a
					// live deadline, not one that never started (S4c r1 M1).
					got, gotErr = After(Site("test"), root, s, nil, time.Hour, func(c *rt.Task) (int, error) {
						s.v.AwaitTimers(1)
						return tc.body(c)
					})
					// Right after After returns, before the root task ends:
					// no deadline sleep is open and no timer is armed.
					if n, p := s.open.Load(), s.v.Pending(); n != 0 || p != 0 {
						t.Errorf("After returned with its deadline alive: %d sleeps open, %d timers armed", n, p)
					}
					return nil
				})
				if err != nil {
					t.Errorf("exit %v", err)
				}
			})
			if got != tc.want || gotErr != tc.wantErr {
				t.Errorf("After = %d, %v; want %d, %v", got, gotErr, tc.want, tc.wantErr)
			}
			if n := s.open.Load(); n != 0 {
				t.Errorf("%d deadline sleeps still open after After returned", n)
			}
			if n := s.v.Pending(); n != 0 {
				t.Errorf("%d virtual timers still armed after After returned", n)
			}
		})
	}
}

// TestAfterDefects: a defect in the body is raised in the caller, never
// turned into Expired or dropped (Timeouts 6); so is a defect of the
// Scheduler's Sleep, which also ends the race, so the body is not left
// without a bound.
func TestAfterDefects(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		s := newVirtualSleeper()
		var err error
		within(t, "After with a body defect", func() {
			err = runMain(func(root *rt.Task) error {
				_, _ = After(Site("test"), root, s, nil, time.Hour, func(*rt.Task) (int, error) { return explode(), nil })
				t.Error("After returned after a defect in its body")
				return nil
			})
		})
		rs := reasons(err)
		if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
			t.Fatalf("exit %v, want one defect", err)
		}
		if n := s.open.Load(); n != 0 {
			t.Errorf("%d deadline sleeps still open", n)
		}
	})
	t.Run("scheduler", func(t *testing.T) {
		var bodyStopped atomic.Bool
		var err error
		within(t, "After with a Scheduler defect", func() {
			err = runMain(func(root *rt.Task) error {
				_, _ = After(Site("test"), root, panicSleeper{}, nil, time.Hour, func(ct *rt.Task) (int, error) {
					defer func() { bodyStopped.Store(ct.Stopped()) }()
					sleepUntilStopped(ct)
					return 7, nil
				})
				t.Error("After returned after a defect in the Scheduler")
				return nil
			})
		})
		if !strings.Contains(err.Error(), "scheduler broke") {
			t.Fatalf("exit %v, want the Scheduler's defect", err)
		}
		var defects int
		for _, r := range reasons(err) {
			if r.Kind == rt.KindDefect {
				defects++
			}
		}
		if defects != 1 {
			t.Errorf("exit %v, want one defect", err)
		}
		if !bodyStopped.Load() {
			t.Error("the body was not stopped when the Scheduler failed")
		}
	})
}

// abortExplored is rt.AbortExplore of the explored build
// (timeout_explored_test.go): the live build has no explored run to stop.
func abortExplored(error) bool { return false }

// panicSleeper is a Scheduler whose Sleep panics.
type panicSleeper struct{}

func (panicSleeper) Sleep(*rt.Task, time.Duration) { panic("scheduler broke") }
