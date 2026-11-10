package rt

import (
	"errors"
	"sync/atomic"
	"testing"
)

// Reviewer probe (round 5, M1): a restore returns to the state of the region
// enclosing the restored one (Effect and ZIO restore semantics). A child
// started in a restore under an ordinary Acquire is cancellable, so its masked
// owner cannot deadlock waiting on it; a child started in a restore whose
// enclosing region is a cleanup keeps that cleanup's shield. Both stop
// timings; the whole run sits inside a deadlock watchdog.
func TestRestoreChildCancellation(t *testing.T) {
	cases := []struct {
		name                                string
		inAcquire, outerCleanup, wantMasked bool
		wantShield                          bool
	}{
		{"outermost cleanup restore", false, false, false, false},
		{"ordinary acquisition", true, false, true, false},
		{"acquisition inside outer cleanup", true, true, true, true},
	}
	for _, tc := range cases {
		for _, timing := range []string{"stop before child start", "stop after child start"} {
			t.Run(tc.name+"/"+timing, func(t *testing.T) {
				root := Root("main")
				started := make(chan *Task, 1)
				allow := make(chan struct{})
				var reached, sawMasked atomic.Bool
				operation := func(task *Task) (int, error) {
					m := task.Mask()
					defer m.Unmask()
					var err error
					m.Interruptible(func() {
						sawMasked.Store(task.Masked())
						_, err = Scoped(task, nil, func(local *Scope) (int, error) {
							if timing == "stop before child start" {
								root.Interrupt()
							}
							h := Go(local, "restored helper", func(child *Task) (int, error) {
								started <- child
								select {
								case <-child.Done():
									return 0, Interrupted
								case <-allow: // only a shielded child is let go
									return 7, nil
								}
							})
							return h.Wait()
						})
					})
					return 0, err
				}
				var err error
				controlDone := make(chan struct{})
				// No rescue for a cancellable child: if the stop does not reach
				// it, its masked owner waits forever and the watchdog fails.
				within(t, "the masked owner's Wait on its restored child (no deadlock)", func() {
					go func() {
						defer close(controlDone)
						child := <-started
						if timing == "stop after child start" {
							root.Interrupt()
						}
						reached.Store(child.StopRequested())
						if tc.wantShield {
							close(allow)
						}
					}()
					_, err = Run(root, func(task *Task) (int, error) {
						if !tc.inAcquire {
							return operation(task)
						}
						acquire := func() (int, error) {
							return Scoped(task, nil, func(s *Scope) (int, error) {
								return Acquire(s, func() (int, error) { return operation(task) }, func(int) {})
							})
						}
						if !tc.outerCleanup {
							return acquire()
						}
						return Scoped(task, nil, func(s *Scope) (int, error) {
							s.Defer(func() {
								if _, exit := acquire(); exit != nil {
									panic(exit)
								}
							})
							return 0, nil
						})
					})
				})
				<-controlDone
				if reached.Load() == tc.wantShield {
					t.Fatalf("the ambient stop reached the restored child = %v, want shielded = %v", reached.Load(), tc.wantShield)
				}
				if !errors.Is(err, Interrupted) || root.Masked() {
					t.Fatalf("exit %v, mask leaked %v", err, root.Masked())
				}
				if sawMasked.Load() != tc.wantMasked {
					t.Errorf("owner masked inside the restore = %v, want %v", sawMasked.Load(), tc.wantMasked)
				}
			})
		}
	}
}
