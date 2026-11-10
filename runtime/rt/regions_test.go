package rt

import (
	"errors"
	"sync/atomic"
	"testing"
)

// Reviewer probe (round 3, M1): a release nested inside a restore, inside an
// acquisition, inside a GoMasked body still protects its helper from a stop
// that arrives after the helper starts.
func TestReleaseShieldAcrossRestoreAndAcquire(t *testing.T) {
	for _, nesting := range []string{"plain restored body", "inside acquire", "restore inside acquire"} {
		t.Run(nesting, func(t *testing.T) {
			root := Root("main")
			outer := NewScope(root)
			started := make(chan *Task, 1)
			allow, controlDone := make(chan struct{}), make(chan struct{})
			var helperStopped atomic.Bool
			go func() {
				defer close(controlDone)
				child := <-started
				root.Interrupt()
				helperStopped.Store(child.StopRequested())
				close(allow)
			}()
			var completed atomic.Bool
			h := GoMasked(&outer, "masked owner", func(task *Task, restore Mask) (int, error) {
				work := func() (int, error) {
					return Scoped(task, nil, func(s *Scope) (int, error) {
						s.Defer(func() {
							local := NewScope(task)
							defer local.End(nil)
							helper := Go(&local, "release helper", func(child *Task) (int, error) {
								started <- child
								select {
								case <-child.Done():
									return 0, Interrupted
								case <-allow:
									return 7, nil
								}
							})
							if _, err := helper.Wait(); err != nil {
								panic(err)
							}
							completed.Store(true)
						})
						return 0, nil
					})
				}
				var err error
				if nesting == "plain restored body" {
					restore.Interruptible(func() { _, err = work() })
				} else {
					_, err = Scoped(task, nil, func(s *Scope) (int, error) {
						return Acquire(s, func() (int, error) {
							if nesting == "restore inside acquire" {
								var exit error
								restore.Interruptible(func() { _, exit = work() })
								return 0, exit
							}
							return work()
						}, func(int) {})
					})
				}
				return 0, err
			})
			_, err := h.Wait()
			<-controlDone
			_ = endScope(&outer)
			if helperStopped.Load() {
				t.Fatal("the ambient stop cascaded into a release-owned helper")
			}
			if !completed.Load() {
				t.Fatal("the ambient stop aborted a release nested inside Acquire and restore")
			}
			if !errors.Is(err, Interrupted) {
				t.Errorf("waiter exit %v, want the interruption", err)
			}
		})
	}
}

// The region stack: each kind's effect on polling and on a child's shield.
func TestRegionKinds(t *testing.T) {
	root := Root("main")
	root.Interrupt()
	shield := func() bool { return root.regions.shields() }
	if !root.Stopped() || shield() {
		t.Fatal("an empty stack must be interruptible and must not shield")
	}
	a0 := root.acquire() // an ordinary Acquire, outside any cleanup
	if root.Stopped() || shield() {
		t.Fatal("an ordinary acquisition must mask the task and must not shield its children")
	}
	root.regions.truncate(a0)
	m := root.Mask() // a defer body or a release
	if root.Stopped() || !shield() {
		t.Fatal("a protecting region must mask and shield")
	}
	a := root.acquire() // Acquire's registration step inside it
	if root.Stopped() || !shield() {
		t.Fatal("an acquisition inside a cleanup must mask and keep the cleanup's shield")
	}
	m.Interruptible(func() {
		// The restore takes the state outside m: unmasked.
		if !root.Stopped() {
			t.Error("a restore of an outermost region did not unmask")
		}
		if shield() {
			t.Error("a restore entry must not shield")
		}
		ra := root.acquire()
		if root.Stopped() || shield() {
			t.Error("an acquisition inside a restore must mask and stay cancellable")
		}
		root.regions.truncate(ra)
		inner := root.Mask()
		if root.Stopped() || !shield() {
			t.Error("a release inside a restore inside an acquisition did not protect")
		}
		inner.Interruptible(func() {
			if !root.Stopped() {
				t.Error("a restore inside a restore did not take the unmasked outside state")
			}
		})
		inner.Unmask()
	})
	root.regions.truncate(a)
	m.Interruptible(func() {
		if !root.Stopped() || shield() {
			t.Error("a restore of a protecting region must unmask and must not shield: it is an explicitly interruptible region")
		}
		nested := root.Mask()
		deeper := root.Mask()
		deeper.Interruptible(func() {
			if root.Stopped() {
				t.Error("an inner restore unmasked its enclosing protecting region")
			}
			if !root.regions.shields() || root.regions.top != restoreShielded {
				t.Error("a restore inside a cleanup must be its own kind and return the cleanup's shield")
			}
		})
		nested.Unmask()
		if root.regions.n != 2 {
			t.Error("Unmask did not close the regions opened inside it")
		}
	})
	m.Unmask()
	if root.Masked() || root.regions.n != 0 {
		t.Fatal("the stack did not empty")
	}
}

// A restore returns to the shield state of the stack below the restored
// region (Effect and ZIO restore semantics), computed when it is pushed.
func TestRestoreReturnsEnclosingShield(t *testing.T) {
	for _, tc := range []struct {
		name               string
		below              []region // the stack below the restored Task.Mask region
		wantMasked, shield bool
	}{
		{"GoMasked body or outermost mask", nil, false, false},
		{"ordinary acquisition", []region{acquiring}, true, false},
		{"acquisition inside a cleanup", []region{protecting, acquiring}, true, true},
		{"cleanup inside a restored acquisition", []region{acquiring, protecting, restoreMasked, protecting}, true, true},
		{"acquisition inside a restored cleanup", []region{protecting, interruptible, acquiring}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := Root("main")
			root.Interrupt()
			for _, r := range tc.below {
				root.regions.push(r)
			}
			m := root.Mask()
			m.Interruptible(func() {
				if root.Masked() != tc.wantMasked {
					t.Errorf("owner masked = %v, want %v", root.Masked(), tc.wantMasked)
				}
				if root.regions.shields() != tc.shield {
					t.Errorf("a child started in the restore is shielded = %v, want %v", root.regions.shields(), tc.shield)
				}
				if root.regions.top == protecting || root.regions.top == acquiring {
					t.Error("the restore entry copied a non-restore kind")
				}
			})
		})
	}
}

// Every region pops on a panic: Acquire's open and a restore.
func TestRegionsPopOnPanic(t *testing.T) {
	root := Root("main")
	_, err := Scoped(root, nil, func(s *Scope) (int, error) {
		return Acquire(s, func() (int, error) { panic("open defect") }, func(int) { t.Error("released a failed acquisition") })
	})
	if err == nil || root.Masked() {
		t.Fatalf("exit %v, masked %v; want the defect and an empty stack", err, root.Masked())
	}
	m := root.Mask()
	func() {
		defer func() { _ = recover() }()
		m.Interruptible(func() { panic("wait defect") })
	}()
	if root.regions.n != 1 || !root.Masked() {
		t.Fatal("a panic in a restore did not pop the restore entry")
	}
	m.Unmask()
	if root.Masked() {
		t.Fatal("the stack did not empty")
	}
}

// Regions deeper than the inline array spill to the overflow slice and back.
func TestDeepRegionNesting(t *testing.T) {
	root := Root("main")
	root.Interrupt()
	var masks []Mask
	for range 3 * inlineRegions {
		masks = append(masks, root.Mask())
	}
	_ = root.acquire() // innermost, in the overflow slice
	if root.Stopped() || !root.regions.shields() {
		t.Fatal("the innermost acquisition must mask and keep the enclosing cleanup's shield")
	}
	inner := masks[len(masks)-1]
	inner.Interruptible(func() {
		// The region below the restored one is 20 protecting masks: the owner
		// stays masked and the restore returns their shield.
		if root.Stopped() || !root.regions.shields() || root.regions.top != restoreShielded {
			t.Error("a deep restore must keep the owner masked and return the enclosing shield")
		}
	})
	if root.regions.top != acquiring {
		t.Fatal("the restore entry was not popped")
	}
	masks[inlineRegions].Unmask()
	if int(root.regions.n) != inlineRegions || !root.regions.shields() || root.Stopped() {
		t.Fatalf("after closing a spilled region: depth %d, shields %v", root.regions.n, root.regions.shields())
	}
	masks[0].Unmask()
	if root.Masked() || !root.Stopped() {
		t.Fatal("the stack did not empty")
	}
}
