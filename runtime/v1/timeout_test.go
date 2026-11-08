package v1

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// The body of timeout.After (docs/spec/runtime.md, Timeouts), driven by a
// test Scheduler: the deadline is the program's Scheduler capability, the
// hidden scheduler argument (docs/spec/stdlib.md, Hidden arguments), so a
// virtual clock decides when it passes and no test waits for real time.

// virtualSleeper is a test Scheduler over rt.Virtual, as a program's own
// Scheduler provider is: Sleep waits on the virtual clock, raises the
// interruption when its task is stopped, and counts the sleeps that are
// still open.
type virtualSleeper struct {
	v    *rt.Virtual
	open atomic.Int32
}

func (s *virtualSleeper) Sleep(t *rt.Task, d time.Duration) {
	s.open.Add(1)
	defer s.open.Add(-1)
	if err := rt.Sleep(t, s.v, d); err != nil {
		raise(err)
	}
}

func newVirtualSleeper() *virtualSleeper {
	return &virtualSleeper{v: rt.NewVirtual(time.Unix(0, 0))}
}

// within fails the test when f does not end in 10 seconds of wall time: a
// watchdog only, so a body that waits for a deadline that never comes fails
// instead of hanging the package.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-testwait.After(t):
		t.Fatalf("%s did not end: After waits for its body and ignores the Scheduler's deadline", what)
	}
}

// TestAfterDeadlineFromScheduler: an hour's deadline passes when the test
// Scheduler advances its virtual clock, not after an hour of wall time.
// The body, which waits until it is stopped, is stopped and drained before
// After returns, and After fails with Expired, the v1 value a catch of
// timeout.Expired matches (Timeouts 4).
func TestAfterDeadlineFromScheduler(t *testing.T) {
	s := newVirtualSleeper()
	var bodyStopped atomic.Bool
	var got int
	var gotErr error
	within(t, "After(1h) under a virtual Scheduler", func() {
		go func() {
			s.v.AwaitTimers(1)
			s.v.Advance(time.Hour)
		}()
		err := runMain(func(root *rt.Task) error {
			got, gotErr = After(Site("test calls timeout.After"), root, s, time.Hour, func(ct *rt.Task) (int, error) {
				defer func() { bodyStopped.Store(ct.Stopped()) }()
				sleepUntilStopped(ct)
				return 7, nil
			})
			return nil
		})
		if err != nil {
			t.Errorf("exit %v", err)
		}
	})
	if got != 0 || gotErr != (Expired{}) {
		t.Fatalf("After = %d, %v; want 0, Expired", got, gotErr)
	}
	if gotErr == (rt.Expired{}) {
		t.Fatal("After returned rt.Expired; a catch of timeout.Expired matches v1.Expired")
	}
	if gotErr.Error() != "timeout.Expired" {
		t.Errorf("Expired text %q, want timeout.Expired", gotErr.Error())
	}
	if !bodyStopped.Load() {
		t.Error("the body ended without a stop: After returned before it drained the body")
	}
	if n := s.open.Load(); n != 0 {
		t.Errorf("%d deadline sleeps still open after After returned", n)
	}
}

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
					got, gotErr = After(Site("test"), root, s, time.Hour, func(c *rt.Task) (int, error) {
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
				_, _ = After(Site("test"), root, s, time.Hour, func(*rt.Task) (int, error) { return explode(), nil })
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
				_, _ = After(Site("test"), root, panicSleeper{}, time.Hour, func(ct *rt.Task) (int, error) {
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

// panicSleeper is a Scheduler whose Sleep panics.
type panicSleeper struct{}

func (panicSleeper) Sleep(*rt.Task, time.Duration) { panic("scheduler broke") }

// TestAfterMaskedCaller: inside a masked region (an exit action, a
// release), the caller's own stop channel is nil (I2), so After races its
// two children alone: the deadline still passes when the Scheduler
// advances, and After fails with Expired.
func TestAfterMaskedCaller(t *testing.T) {
	s := newVirtualSleeper()
	var got int
	var gotErr error
	within(t, "After in a masked caller", func() {
		go func() {
			s.v.AwaitTimers(1)
			s.v.Advance(time.Hour)
		}()
		err := runMain(func(root *rt.Task) error {
			m := root.Mask()
			defer m.Unmask()
			got, gotErr = After(Site("test"), root, s, time.Hour, func(ct *rt.Task) (int, error) {
				sleepUntilStopped(ct)
				return 7, nil
			})
			return nil
		})
		if err != nil {
			t.Errorf("exit %v", err)
		}
	})
	if got != 0 || gotErr != (Expired{}) {
		t.Fatalf("After = %d, %v; want 0, Expired", got, gotErr)
	}
	if n := s.open.Load(); n != 0 {
		t.Errorf("%d deadline sleeps still open", n)
	}
}

// TestAfterParentStopped: a stop of the caller's task stops and drains the
// body and the deadline child, and After's exit is the interruption
// (Timeouts 5), not Expired.
func TestAfterParentStopped(t *testing.T) {
	s := newVirtualSleeper()
	root := rt.Root("main")
	started := make(chan struct{})
	var bodyStopped atomic.Bool
	var err error
	within(t, "After under a stopped parent", func() {
		go func() {
			<-started
			s.v.AwaitTimers(1)
			root.Interrupt()
		}()
		err = runOn(root, func(root *rt.Task) error {
			_, _ = After(Site("test"), root, s, time.Hour, func(ct *rt.Task) (int, error) {
				defer func() { bodyStopped.Store(ct.Stopped()) }()
				close(started)
				sleepUntilStopped(ct)
				return 7, nil
			})
			t.Error("After returned after its caller was stopped")
			return nil
		})
	})
	if err != rt.Interrupted {
		t.Fatalf("exit %v, want the interruption", err)
	}
	if !bodyStopped.Load() {
		t.Error("the body was not stopped")
	}
	if n := s.open.Load(); n != 0 {
		t.Errorf("%d deadline sleeps still open", n)
	}
}
