package rt

import (
	"errors"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// within runs f on its own goroutine and fails the test if f has not returned
// at the test's deadline (testwait): a shield that also blocked direct stops would hang instead.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-testwait.After(t):
		t.Fatalf("%s did not finish by the test's deadline", what)
	}
}

// Reviewer probe (round 2, M1): a stop that arrives before or after a release
// starts its helper does not reach the helper, so the release completes.
func TestStopDuringReleaseKeepsHelper(t *testing.T) {
	for _, timing := range []string{"stop before helper start", "stop after helper start"} {
		t.Run(timing, func(t *testing.T) {
			root := Root("main")
			started := make(chan *Task, 1)
			allow := make(chan struct{})
			controlDone := make(chan struct{})
			var helperStopped bool
			if timing == "stop after helper start" {
				go func() {
					defer close(controlDone)
					child := <-started
					root.Interrupt()
					helperStopped = child.StopRequested()
					close(allow)
				}()
			} else {
				close(allow)
				close(controlDone)
			}
			completed := false
			_, err := Run(root, func(task *Task) (int, error) {
				return Scoped(task, nil, func(s *Scope) (int, error) {
					s.Defer(func() {
						if timing == "stop before helper start" {
							root.Interrupt()
						}
						local := NewScope(task)
						defer local.End(nil)
						h := Go(&local, "release helper", func(child *Task) (int, error) {
							started <- child
							select {
							case <-child.Done():
								return 0, Interrupted
							case <-allow:
								return 7, nil
							}
						})
						if v, exit := h.Wait(); exit != nil || v != 7 {
							panic(exit)
						}
						completed = true
					})
					return 0, nil
				})
			})
			<-controlDone
			if helperStopped {
				t.Fatal("the ambient stop cascaded into a release-owned helper")
			}
			if !completed {
				t.Fatalf("the ambient stop aborted the release at its helper's Wait: %v", err)
			}
			if !errors.Is(err, Interrupted) {
				t.Fatalf("root exit %v, want the interruption after the release", err)
			}
		})
	}
}

// A shielded helper still stops by its handle's Stop, by a timeout and by its
// scope's drain.
func TestShieldedHelperStopsDirectly(t *testing.T) {
	inRelease := func(t *testing.T, release func(task *Task)) {
		root := Root("main")
		within(t, "the release", func() {
			_, _ = Scoped(root, nil, func(s *Scope) (int, error) {
				s.Defer(func() {
					root.Interrupt() // ambient stop: must not be what ends the helper
					release(root)
				})
				return 0, nil
			})
		})
	}
	t.Run("handle Stop", func(t *testing.T) {
		inRelease(t, func(task *Task) {
			local := NewScope(task)
			defer local.End(nil)
			h := Go(&local, "helper", blockUntilStopped)
			if !h.Task().shielded || h.Task().StopRequested() {
				t.Error("the helper is not shielded from the ambient stop")
			}
			if err := h.Stop(); err != nil {
				t.Errorf("Stop returned %v", err)
			}
		})
	})
	t.Run("timeout", func(t *testing.T) {
		inRelease(t, func(task *Task) {
			v := NewVirtual(epoch)
			go func() { v.AwaitTimers(1); v.Advance(time.Hour) }()
			_, err := After(task, v, time.Hour, blockUntilStopped)
			if err != (Expired{}) {
				t.Errorf("After in a release returned %v, want Expired", err)
			}
		})
	})
	t.Run("scope drain", func(t *testing.T) {
		var drained bool
		inRelease(t, func(task *Task) {
			local := NewScope(task)
			Go(&local, "helper", func(child *Task) (int, error) {
				<-child.Done()
				drained = true
				return 0, Interrupted
			})
			local.End(nil)
		})
		if !drained {
			t.Fatal("the release-local scope did not drain its helper")
		}
	})
}
