package rt

import (
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

var errBody = errors.New("body failed")

func TestExitKeepsEveryCause(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	ready, proceed := make(chan struct{}), make(chan struct{})
	h := Go(&s, "worker", func(task *Task) (int, error) {
		return Scoped(task, nil, func(s *Scope) (int, error) {
			s.Defer(func() { panic("close a") })
			s.Defer(func() { panic("close b") })
			close(ready)
			<-proceed // stopped meanwhile; the body does not poll
			return 0, errBody
		})
	})
	<-ready
	h.Task().Interrupt()
	close(proceed)
	_, err := h.Wait()
	c, ok := err.(*Cause)
	if !ok {
		t.Fatalf("exit %v (%T), want a *Cause", err, err)
	}
	want := []Reason{
		{Kind: KindFailure, Err: errBody},
		{Kind: KindDefect, Value: "close b", InRelease: true},
		{Kind: KindDefect, Value: "close a", InRelease: true},
		{Kind: KindInterruption},
	}
	if len(c.Reasons) != len(want) {
		t.Fatalf("cause has %d reasons, want %d:\n%v", len(c.Reasons), len(want), c)
	}
	for i, r := range c.Reasons {
		w := want[i]
		if r.Kind != w.Kind || r.Err != w.Err || r.Value != w.Value || r.InRelease != w.InRelease {
			t.Fatalf("reason %d is %+v, want %+v", i, r, w)
		}
	}
	if c.Reasons[1].Chain[0].Span != "worker" {
		t.Fatalf("release defect chain %v does not start at the worker", c.Reasons[1].Chain)
	}
	if _, lone := Failure(err); lone {
		t.Fatal("a catch would match a cause that holds defects")
	}
	if OnlyInterruption(err) {
		t.Fatal("OnlyInterruption is true for a cause with a failure and defects")
	}
	if !errors.Is(err, errBody) || !errors.Is(err, Interrupted) {
		t.Fatal("errors.Is does not see the failure and the interruption")
	}
	_ = endScope(&s)
}

func TestReleaseDefectDoesNotSkipLaterReleases(t *testing.T) {
	var closed []string
	_, err := Scoped(Root("main"), nil, func(s *Scope) (int, error) {
		_, _ = Acquire(s, func() (string, error) { return "a", nil }, func(r string) { closed = append(closed, r) })
		_, _ = Acquire(s, func() (string, error) { return "b", nil }, func(string) { panic("close b") })
		_, _ = Acquire(s, func() (string, error) { return "c", nil }, func(r string) { closed = append(closed, r) })
		_, err := Acquire(s, func() (string, error) { return "", errBody }, func(string) { t.Error("a failed open was released") })
		return 0, err
	})
	if !slices.Equal(closed, []string{"c", "a"}) {
		t.Fatalf("released %v, want [c a]", closed)
	}
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 2 || c.Reasons[0].Err != errBody || c.Reasons[1].Value != "close b" {
		t.Fatalf("exit %v, want the open failure then the close defect", err)
	}
}

func TestScopeReleasesReverseOnEveryExit(t *testing.T) {
	errFail := errors.New("typed failure")
	cases := []struct {
		name  string
		body  func(task *Task) (int, error)
		stop  bool
		check func(err error) bool
	}{
		{"success", func(*Task) (int, error) { return 1, nil }, false,
			func(err error) bool { return err == nil }},
		{"failure", func(*Task) (int, error) { return 0, errFail }, false,
			func(err error) bool { f, ok := Failure(err); return ok && f == errFail }},
		{"panic", func(*Task) (int, error) { panic("boom") }, false,
			func(err error) bool {
				c, ok := err.(*Cause)
				return ok && len(c.Reasons) == 1 && c.Reasons[0].Kind == KindDefect && c.Reasons[0].Value == "boom"
			}},
		{"stop", blockUntilStopped, true,
			func(err error) bool { return err == Interrupted }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := Root("main")
			s := NewScope(root)
			var mu sync.Mutex
			var released []string
			acquired := make(chan struct{})
			h := Go(&s, tc.name, func(task *Task) (int, error) {
				return Scoped(task, nil, func(s *Scope) (int, error) {
					for _, name := range []string{"a", "b", "c"} {
						_, _ = Acquire(s, func() (string, error) { return name, nil }, func(r string) {
							mu.Lock()
							released = append(released, r)
							mu.Unlock()
						})
					}
					close(acquired)
					return tc.body(task)
				})
			})
			<-acquired
			if tc.stop {
				h.Task().Interrupt()
			}
			_, err := h.Wait()
			if !tc.check(err) {
				t.Fatalf("unexpected exit %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(released, []string{"c", "b", "a"}) {
				t.Fatalf("released %v, want [c b a]", released)
			}
			_ = endScope(&s)
		})
	}
}

func TestAcquireHasNoInterruptionWindow(t *testing.T) {
	t.Run("stop during open", func(t *testing.T) {
		root := Root("main")
		s := NewScope(root)
		var opens, closes int
		var maskedInOpen, stoppedAfter bool
		h := Go(&s, "acquirer", func(task *Task) (int, error) {
			return Scoped(task, nil, func(s *Scope) (int, error) {
				_, _ = Acquire(s, func() (int, error) {
					root.Interrupt() // the stop lands inside the acquisition
					maskedInOpen = !task.Stopped() && task.StopRequested()
					opens++
					return 1, nil
				}, func(int) { closes++ })
				stoppedAfter = task.Stopped()
				if stoppedAfter {
					return 0, Interrupted
				}
				return 1, nil
			})
		})
		if _, err := h.Wait(); err != Interrupted {
			t.Fatalf("exit %v, want Interrupted", err)
		}
		if !maskedInOpen || !stoppedAfter {
			t.Fatalf("masked during open=%v, stopped after registration=%v", maskedInOpen, stoppedAfter)
		}
		if opens != 1 || closes != 1 {
			t.Fatalf("opened %d, closed %d; want exactly one release", opens, closes)
		}
		_ = endScope(&s)
	})
	t.Run("concurrent stops", func(t *testing.T) {
		for range 50 {
			root := Root("main")
			s := NewScope(root)
			var opens, closes atomic.Int64
			running := make(chan struct{})
			h := Go(&s, "acquirer", func(task *Task) (int, error) {
				return Scoped(task, nil, func(s *Scope) (int, error) {
					for i := 0; ; i++ {
						if i == 8 {
							close(running)
						}
						if task.Stopped() {
							return i, Interrupted
						}
						_, _ = Acquire(s, func() (int, error) { opens.Add(1); return i, nil },
							func(int) { closes.Add(1) })
					}
				})
			})
			<-running
			h.Task().Interrupt()
			if _, err := h.Wait(); err != Interrupted {
				t.Fatalf("exit %v, want Interrupted", err)
			}
			if opens.Load() != closes.Load() {
				t.Fatalf("opened %d, closed %d", opens.Load(), closes.Load())
			}
			_ = endScope(&s)
		}
	})
}

func TestTaskDrainsBeforeLentResourceCloses(t *testing.T) {
	for _, exit := range []string{"success", "failure", "panic"} {
		t.Run(exit, func(t *testing.T) {
			var mu sync.Mutex
			var log []string
			record := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
			var closed atomic.Bool
			_, err := Scoped(Root("main"), nil, func(s *Scope) (int, error) {
				_, _ = Acquire(s, func() (*atomic.Bool, error) { return &closed, nil },
					func(r *atomic.Bool) { r.Store(true); record("resource closed") })
				lending := make(chan struct{})
				Go(s, "lender", func(task *Task) (int, error) {
					close(lending)
					for !task.Stopped() {
						if closed.Load() {
							return 0, errors.New("used the resource after it closed")
						}
						runtime.Gosched()
					}
					record("task drained")
					return 0, Interrupted
				})
				<-lending
				switch exit {
				case "failure":
					return 0, errBody
				case "panic":
					panic("boom")
				}
				return 0, nil
			})
			if exit == "success" && err != nil {
				t.Fatalf("exit %v, want nil: the stopped task's interruption is normalized", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(log, []string{"task drained", "resource closed"}) {
				t.Fatalf("order %v, want the task drained before the resource closed", log)
			}
		})
	}
}

func TestScopeKeepsDefectOfDrainedTask(t *testing.T) {
	_, err := Scoped(Root("main"), nil, func(s *Scope) (int, error) {
		started := make(chan struct{})
		Go(s, "crasher", func(task *Task) (int, error) {
			close(started)
			<-task.Done()
			panic("cleanup defect")
		})
		<-started
		return 1, nil
	})
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Value != "cleanup defect" || c.Reasons[0].Chain[0].Span != "crasher" {
		t.Fatalf("exit %v, want the drained task's defect with its chain", err)
	}
}
