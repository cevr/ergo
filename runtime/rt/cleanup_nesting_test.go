package rt

import (
	"errors"
	"sync/atomic"
	"testing"
)

// Reviewer probe (round 4, M1): an acquisition inside an automatic release
// keeps that release's protection, so a helper started by the acquisition's
// open is shielded from the ambient stop, for both nesting orders and both
// stop timings.
func TestAcquireInsideProtectedCleanup(t *testing.T) {
	nestings := []string{"release then acquire", "restore then release then acquire", "restore then acquire then release"}
	timings := []string{"stop before helper start", "stop after helper start"}
	for _, nesting := range nestings {
		for _, timing := range timings {
			t.Run(nesting+"/"+timing, func(t *testing.T) {
				root := Root("main")
				outer := NewScope(root)
				started := make(chan *Task, 1)
				allow, controlDone := make(chan struct{}), make(chan struct{})
				var cascaded, completed atomic.Bool
				go func() {
					defer close(controlDone)
					child := <-started
					if timing == "stop after helper start" {
						root.Interrupt()
					}
					cascaded.Store(child.StopRequested())
					close(allow)
				}()
				h := GoMasked(&outer, "masked owner", func(task *Task, restore Mask) (int, error) {
					helper := func(local *Scope) (int, error) {
						if timing == "stop before helper start" {
							root.Interrupt()
						}
						h := Go(local, "acquisition helper", func(child *Task) (int, error) {
							started <- child
							select {
							case <-child.Done():
								return 0, Interrupted
							case <-allow:
								return 7, nil
							}
						})
						return h.Wait()
					}
					releaseWithAcquire := func() (int, error) {
						return Scoped(task, nil, func(s *Scope) (int, error) {
							s.Defer(func() {
								local := NewScope(task)
								defer local.End(nil)
								_, err := Acquire(&local, func() (int, error) { return helper(&local) }, func(int) {})
								if err != nil {
									panic(err)
								}
								completed.Store(true)
							})
							return 0, nil
						})
					}
					if nesting == "release then acquire" {
						return releaseWithAcquire()
					}
					var err error
					restore.Interruptible(func() {
						if nesting == "restore then acquire then release" {
							_, err = Scoped(task, nil, func(s *Scope) (int, error) {
								return Acquire(s, func() (int, error) {
									return Scoped(task, nil, func(inner *Scope) (int, error) {
										inner.Defer(func() {
											local := NewScope(task)
											defer local.End(nil)
											if _, exit := helper(&local); exit != nil {
												panic(exit)
											}
											completed.Store(true)
										})
										return 0, nil
									})
								}, func(int) {})
							})
						} else {
							_, err = releaseWithAcquire()
						}
					})
					return 0, err
				})
				_, err := h.Wait()
				<-controlDone
				_ = endScope(&outer)
				if cascaded.Load() {
					t.Fatal("the ambient stop reached a helper inside a protected cleanup")
				}
				if !completed.Load() {
					t.Fatalf("the cleanup did not finish; exit %v", err)
				}
				if !errors.Is(err, Interrupted) {
					t.Errorf("exit %v, want the root interruption", err)
				}
			})
		}
	}
}

// Reviewer probe (round 4, M2): the region stack has no depth cap, so 65,535
// open masks do not stop a scope from running its cleanup or keeping the
// body's failure.
func TestDeepMasksKeepCleanup(t *testing.T) {
	task := Root("main")
	var closed bool
	_, err := Run(task, func(task *Task) (int, error) {
		var masks []Mask
		defer func() {
			for i := len(masks) - 1; i >= 0; i-- {
				masks[i].Unmask()
			}
		}()
		for range 65535 {
			masks = append(masks, task.Mask())
		}
		return Scoped(task, nil, func(s *Scope) (int, error) {
			s.Defer(func() { closed = true })
			return 0, errBody
		})
	})
	if !closed || err != errBody || task.Masked() {
		t.Fatalf("cleanup ran=%v, exit %v (want the body failure alone), masked=%v", closed, err, task.Masked())
	}
}
