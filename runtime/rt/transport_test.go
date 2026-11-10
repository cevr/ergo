package rt

import (
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// Reviewer probe (round 2, M3): a typed-nil *Cause panic in a body is a
// defect recorded as it is; recovery does not panic again.
func TestTypedNilCausePanicInBodyIsADefect(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Run let a panic escape: %T: %v", p, p)
		}
	}()
	_, err := Run(Root("main"), func(*Task) (int, error) { panic((*Cause)(nil)) })
	c, ok := err.(*Cause)
	if !ok || c == nil || len(c.Reasons) != 1 || c.Reasons[0].Kind != KindDefect {
		t.Fatalf("exit %v, want one defect", err)
	}
	if v, isCause := c.Reasons[0].Value.(*Cause); !isCause || v != nil {
		t.Fatalf("defect value %#v, want the typed-nil *Cause as it was raised", c.Reasons[0].Value)
	}
	_ = err.Error() // printing a typed-nil defect value must not panic
}

// Reviewer probe (round 2, M3): the same panic in a release does not skip the
// next release, leak the mask or lose the body's failure.
func TestTypedNilCausePanicInReleaseKeepsCleanup(t *testing.T) {
	root := Root("main")
	closed := false
	_, err := Run(root, func(task *Task) (int, error) {
		return Scoped(task, nil, func(s *Scope) (int, error) {
			s.Defer(func() { closed = true })
			s.Defer(func() { panic((*Cause)(nil)) })
			return 0, errBody
		})
	})
	if !closed || root.Masked() || !errors.Is(err, errBody) {
		t.Fatalf("other release ran=%v, mask leaked=%v, body failure kept=%v; exit %v",
			closed, root.Masked(), errors.Is(err, errBody), err)
	}
	c, ok := err.(*Cause)
	if !ok || c == nil || len(c.Reasons) != 2 || c.Reasons[1].Kind != KindDefect || !c.Reasons[1].InRelease {
		t.Fatalf("exit %v, want the failure then the release defect", err)
	}
}

type nilCauseScheduler struct{ started <-chan struct{} }
type nilCauseTimer struct{ started <-chan struct{} }

func (s nilCauseScheduler) NewTimer(time.Duration) Timer { return nilCauseTimer(s) }
func (s nilCauseTimer) C() <-chan time.Time {
	<-s.started
	panic((*Cause)(nil))
}
func (nilCauseTimer) Stop() bool { return true }

// Reviewer probe (round 2, M3): a typed-nil *Cause panic from Timer.C still
// passes After's unwind boundary, which drains the child first.
func TestTypedNilCausePanicInTimerStillDrains(t *testing.T) {
	root := Root("main")
	started := make(chan struct{})
	var childDrained atomic.Bool
	drainedBeforeRelease := false
	_, err := Run(root, func(task *Task) (int, error) {
		return Scoped(task, nil, func(s *Scope) (int, error) {
			s.Defer(func() { drainedBeforeRelease = childDrained.Load() })
			return After(task, nilCauseScheduler{started}, time.Hour, func(child *Task) (int, error) {
				close(started)
				<-child.Done()
				childDrained.Store(true)
				return 0, Interrupted
			})
		})
	})
	if !drainedBeforeRelease {
		t.Fatalf("a typed-nil panic bypassed After's drain; exit %v", err)
	}
	c, ok := err.(*Cause)
	if !ok || c == nil || len(c.Reasons) != 1 || c.Reasons[0].Kind != KindDefect {
		t.Fatalf("exit %v, want the timer defect alone", err)
	}
}

// Only the package's own transport carries an exit; a non-nil *Cause raised
// by user code is a defect too, not an unpacked exit.
func TestOnlyTransportCarriesAnExit(t *testing.T) {
	forged := &Cause{Reasons: []Reason{{Kind: KindInterruption}}}
	_, err := Run(Root("main"), func(*Task) (int, error) { panic(forged) })
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Kind != KindDefect || c.Reasons[0].Value != forged {
		t.Fatalf("exit %v, want the raised *Cause recorded as a defect", err)
	}
	// End(nil) transports a real exit to the next boundary.
	_, err = Run(Root("main"), func(task *Task) (int, error) {
		s := NewScope(task)
		defer s.End(nil)
		s.Defer(func() { panic("release defect") })
		return 0, nil
	})
	c, ok = err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Value != "release defect" || !c.Reasons[0].InRelease {
		t.Fatalf("exit %v, want the transported release defect", err)
	}
}

// Reviewer probe (round 2, M2): Parent may be read while the task finishes
// and clears its link; the race detector checks the synchronization.
func TestParentReadDuringCompletion(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	proceed := make(chan struct{})
	h := Go(&s, "child", func(*Task) (int, error) { <-proceed; return 1, nil })
	child := h.Task()
	if child.Parent() != root {
		t.Fatal("a running task has no live parent")
	}
	close(proceed)
	for range 1000 {
		if p := child.Parent(); p != nil && p != root {
			t.Fatal("Parent returned a task that is not the parent")
		}
		runtime.Gosched()
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if child.Parent() != nil {
		t.Fatal("a finished task kept its parent link")
	}
	_ = endScope(&s)
}
