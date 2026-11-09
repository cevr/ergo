package rt

import (
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// waitUntil yields until cond holds. Tests use it instead of sleeping.
func waitUntil(cond func() bool) {
	for !cond() {
		runtime.Gosched()
	}
}

// waitGroupWithin waits for wg, failing the test after limit. Only a broken
// runtime makes it wait that long.
func waitGroupWithin(t *testing.T, wg *sync.WaitGroup, limit time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not happen within %v", what, limit)
	}
}

// endScope ends s as a body with an error row does and returns the exit.
func endScope(s *Scope) error {
	var err error
	s.End(&err)
	return err
}

func blockUntilStopped(t *Task) (int, error) {
	<-t.Done()
	return 0, Interrupted
}

func TestStopPropagatesToDescendants(t *testing.T) {
	const fan = 3
	const total = 1 + fan + fan*fan
	for _, via := range []string{"task Interrupt", "root Interrupt"} {
		t.Run(via, func(t *testing.T) {
			root := Root("main")
			s := NewScope(root)
			var started, observed sync.WaitGroup
			started.Add(total)
			observed.Add(total)
			var mu sync.Mutex
			var tasks []*Task
			// hold keeps every body, and so every scope, open after the stop:
			// no scope end can stop a descendant, only Interrupt's own walk.
			hold := make(chan struct{})
			observe := func(task *Task) (int, error) {
				mu.Lock()
				tasks = append(tasks, task)
				mu.Unlock()
				started.Done()
				<-task.Done()
				observed.Done()
				<-hold
				return 0, Interrupted
			}
			top := Go(&s, "top", func(task *Task) (int, error) {
				return Scoped(task, func(s *Scope) (int, error) {
					for range fan {
						Go(s, "mid", func(task *Task) (int, error) {
							return Scoped(task, func(s *Scope) (int, error) {
								for range fan {
									Go(s, "leaf", observe)
								}
								return observe(task)
							})
						})
					}
					return observe(task)
				})
			})
			started.Wait()
			if via == "task Interrupt" {
				top.Task().Interrupt()
			} else {
				root.Interrupt()
			}
			waitGroupWithin(t, &observed, 10*time.Second, "every descendant observing the stop")
			mu.Lock()
			for _, task := range tasks {
				if !task.StopRequested() {
					t.Errorf("task #%d %s is not stopped", task.ID(), task.Span())
				}
			}
			mu.Unlock()
			close(hold)
			if _, err := top.Wait(); err != Interrupted {
				t.Fatalf("Wait returned %v, want Interrupted", err)
			}
			if top.task.first != nil || root.first != nil {
				t.Fatal("finished tasks stayed linked to their parents")
			}
			_ = endScope(&s)
		})
	}
}

func TestConcurrentStopAndChildAdmission(t *testing.T) {
	for range 50 {
		root := Root("main")
		parent := &Task{}
		parent.init(root, "parent", false, false)
		start := make(chan struct{})
		children := make([]*Task, 16)
		var workers sync.WaitGroup
		for i := range children {
			workers.Go(func() {
				<-start
				children[i] = &Task{}
				children[i].init(parent, "child", false, false)
			})
		}
		for range 4 {
			workers.Go(func() { <-start; root.Interrupt() })
		}
		close(start)
		workers.Wait()
		for _, c := range children {
			if !c.Stopped() {
				t.Fatal("a child admitted concurrently with Interrupt stayed live")
			}
			c.detach()
		}
		parent.detach()
		if root.first != nil || parent.first != nil {
			t.Fatal("detached children stayed linked")
		}
	}
}

func TestPollObservesStop(t *testing.T) {
	root := Root("main")
	if allocs := testing.AllocsPerRun(100, func() { _ = root.Stopped() }); allocs != 0 {
		t.Fatalf("a poll allocates %v times", allocs)
	}
	s := NewScope(root)
	const pollers = 8
	var polling sync.WaitGroup
	polling.Add(pollers)
	handles := make([]*Handle[int], pollers)
	for i := range handles {
		handles[i] = Go(&s, "poller", func(task *Task) (int, error) {
			polling.Done()
			n := 0
			for !task.Stopped() {
				n++
				if n%64 == 0 {
					runtime.Gosched()
				}
			}
			return n, Interrupted
		})
	}
	checker := Go(&s, "checker", func(task *Task) (int, error) {
		for {
			task.Check() // no error row: the boundary recovers the panic
			runtime.Gosched()
		}
	})
	polling.Wait()
	root.Interrupt()
	for _, h := range handles {
		if _, err := h.Wait(); err != Interrupted {
			t.Fatalf("poller exit %v, want Interrupted", err)
		}
	}
	if _, err := checker.Wait(); err != Interrupted {
		t.Fatalf("Check exit %v, want Interrupted", err)
	}
	_ = endScope(&s)
}

func TestMaskDefersStopUntilUnmask(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	masked := make(chan struct{})
	fail := func(msg string) (string, error) { return "", errors.New(msg) }
	h := Go(&s, "masked", func(task *Task) (string, error) {
		m := task.Mask()
		close(masked)
		waitUntil(task.StopRequested)
		if task.Stopped() {
			return fail("stop observed inside a mask")
		}
		if task.Done() != nil {
			return fail("Done is interruptible inside a mask")
		}
		inner := task.Mask()
		var innerSaw bool
		inner.Interruptible(func() { innerSaw = task.Stopped() })
		if innerSaw {
			return fail("an inner restore unmasked the outer region")
		}
		inner.Unmask()
		if task.Stopped() {
			return fail("closing the inner region exposed the stop")
		}
		var outerSaw bool
		m.Interruptible(func() { outerSaw = task.Stopped() && task.Done() != nil })
		if !outerSaw {
			return fail("the poll pattern did not observe the stop")
		}
		if task.Stopped() {
			return fail("Interruptible did not restore the mask")
		}
		m.Unmask()
		if !task.Stopped() {
			return fail("the stop was not observed after unmask")
		}
		return "done", nil
	})
	<-masked
	h.Task().Interrupt()
	if _, err := h.Wait(); err != Interrupted {
		t.Fatalf("exit %v, want Interrupted", err)
	}

	// Release always runs masked, even when the scope ends by a stop.
	var releaseMasked, releaseSawStop bool
	release := Go(&s, "release", func(task *Task) (int, error) {
		return Scoped(task, func(s *Scope) (int, error) {
			s.Defer(func() { releaseMasked, releaseSawStop = task.Masked(), task.Stopped() })
			return blockUntilStopped(task)
		})
	})
	if err := release.Stop(); err != nil {
		t.Fatalf("Stop returned %v", err)
	}
	if !releaseMasked || releaseSawStop {
		t.Fatalf("release masked=%v sawStop=%v, want masked and no stop", releaseMasked, releaseSawStop)
	}
	_ = endScope(&s)
}

// Reviewer probe (round 1, M2): a child started inside an acquisition starts
// interruptible, so it observes a parent stop once the acquisition ends.
func TestChildStartedInAcquireObservesParentStop(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	proceed := make(chan struct{})
	observed := make(chan bool, 1)
	var h *Handle[int]
	_, _ = Acquire(&s, func() (int, error) {
		h = Go(&s, "background", func(child *Task) (int, error) {
			<-proceed
			observed <- child.Stopped()
			return 0, nil
		})
		return 0, nil
	}, func(int) {})
	root.Interrupt()
	close(proceed)
	if !<-observed {
		t.Fatal("a child started during an acquisition cannot observe the parent's stop")
	}
	if _, err := h.Wait(); err != Interrupted {
		t.Fatalf("Wait returned %v, want Interrupted", err)
	}
	_ = endScope(&s)
}

func TestGoMaskedDefersStopUntilRestore(t *testing.T) {
	for _, exit := range []string{"restore", "unmask"} {
		t.Run(exit, func(t *testing.T) {
			root := Root("main")
			s := NewScope(root)
			ready := make(chan struct{})
			fail := func(msg string) (string, error) { return "", errors.New(msg) }
			h := GoMasked(&s, "flush", func(task *Task, restore Mask) (string, error) {
				close(ready)
				waitUntil(task.StopRequested)
				if task.Stopped() || task.Done() != nil {
					return fail("the stop was observable in the masked body")
				}
				if exit == "unmask" {
					restore.Unmask()
					if !task.Stopped() {
						return fail("unmask did not expose the stop")
					}
					return "", Interrupted
				}
				var saw bool
				restore.Interruptible(func() { saw = task.Stopped() && task.Done() != nil })
				if !saw {
					return fail("restore did not expose the stop")
				}
				if task.Stopped() {
					return fail("restore did not mask the body again")
				}
				return "flushed", nil
			})
			<-ready
			h.Task().Interrupt()
			v, err := h.Wait()
			switch exit {
			case "restore":
				if v != "flushed" || err != nil {
					t.Fatalf("got %q, %v; a stop the masked body never observed replaced its result", v, err)
				}
			case "unmask":
				if err != Interrupted {
					t.Fatalf("got %q, %v; want Interrupted", v, err)
				}
			}
			_ = endScope(&s)
		})
	}
}

func TestChildOfMaskedStoppedParentStartsLive(t *testing.T) {
	root := Root("main")
	m := root.Mask()
	root.Interrupt()
	s := NewScope(root)
	h := Go(&s, "in release", func(task *Task) (string, error) {
		if task.Stopped() {
			return "", Interrupted
		}
		return "flushed", nil
	})
	v, err := h.Wait()
	m.Unmask()
	if v != "flushed" || err != nil {
		t.Fatalf("got %q, %v: a child started where the parent cannot observe its stop was stopped", v, err)
	}
	if err := endScope(&s); err != Interrupted {
		t.Fatalf("scope end of a stopped root returned %v, want Interrupted", err)
	}
}

func TestChildInheritsContextValues(t *testing.T) {
	type key struct{}
	root := Root("main")
	s := NewScope(root)
	region := root.WithValue(key{}, "eu")
	h := Go(&s, "child", func(task *Task) (any, error) { return task.Value(key{}), nil })
	region.End()
	if v, err := h.Wait(); v != "eu" || err != nil {
		t.Fatalf("child saw %v, %v; want eu", v, err)
	}
	if root.Value(key{}) != nil {
		t.Fatal("End did not restore the outer values")
	}
	_ = endScope(&s)
}
