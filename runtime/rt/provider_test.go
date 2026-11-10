package rt

import (
	"sync/atomic"
	"testing"
	"time"
)

// panicOnC is a Scheduler whose timer panics in C once the child has started.
type panicOnC struct{ started <-chan struct{} }

func (s panicOnC) NewTimer(time.Duration) Timer { return panicOnCTimer(s) }

type panicOnCTimer struct{ started <-chan struct{} }

func (p panicOnCTimer) C() <-chan time.Time {
	<-p.started
	panic("timer defect")
}

func (panicOnCTimer) Stop() bool { return true }

// panicOnStop is a Scheduler whose timer never fires and panics in Stop.
type panicOnStop struct{}

type panicOnStopTimer struct{ never chan time.Time }

func (panicOnStop) NewTimer(time.Duration) Timer { return panicOnStopTimer{make(chan time.Time)} }
func (p panicOnStopTimer) C() <-chan time.Time   { return p.never }
func (panicOnStopTimer) Stop() bool              { panic("timer stop defect") }

// Reviewer probe (round 1, M1): a provider panic in Timer.C unwinds After
// through its boundary, which drains the child before the caller's releases.
func TestAfterProviderPanicDrainsChild(t *testing.T) {
	root := Root("main")
	started := make(chan struct{})
	var childDrained atomic.Bool
	drainedBeforeRelease := false
	_, err := Scoped(root, nil, func(s *Scope) (int, error) {
		s.Defer(func() { drainedBeforeRelease = childDrained.Load() })
		return After(root, panicOnC{started}, time.Hour, func(child *Task) (int, error) {
			close(started)
			<-child.Done()
			childDrained.Store(true)
			return 0, Interrupted
		})
	})
	if !drainedBeforeRelease {
		t.Errorf("the caller's resource was released before the timed child drained; exit %v", err)
	}
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Value != "timer defect" {
		t.Fatalf("exit %v, want the timer defect alone", err)
	}
}

// Reviewer probe (round 1, M1): a body defect and a defect in Timer.Stop are
// both kept, body first.
func TestAfterProviderPanicKeepsEveryDefect(t *testing.T) {
	_, err := Scoped(Root("main"), nil, func(s *Scope) (int, error) {
		return After(s.Task(), panicOnStop{}, time.Hour, func(*Task) (int, error) {
			panic("body defect")
		})
	})
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 2 || c.Reasons[0].Value != "body defect" ||
		c.Reasons[1].Value != "timer stop defect" || !c.Reasons[1].InRelease {
		t.Fatalf("exit %v, want the body defect then the timer release defect", err)
	}
}
