package rt

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// manual is a test clock: its deadline fires only when the test says so.
type manual struct{ fire chan time.Time }

func (m manual) After(time.Duration) <-chan time.Time { return m.fire }

func TestAfterReturnsBodyResult(t *testing.T) {
	clock := manual{make(chan time.Time)}
	v, err := After(clock, Root(), time.Second, func(*Task) (string, error) { return "ok", nil })
	if v != "ok" || err != nil {
		t.Fatalf("got %q, %v", v, err)
	}
}

func TestAfterExpiryStopsAndWaitsForChild(t *testing.T) {
	clock := manual{make(chan time.Time, 1)}
	started := make(chan struct{})
	finished := false
	go func() {
		<-started
		clock.fire <- time.Time{}
	}()
	_, err := After(clock, Root(), time.Second, func(child *Task) (int, error) {
		close(started)
		for !child.Stopped() {
			runtime.Gosched()
		}
		finished = true
		return 0, Interrupted
	})
	if !errors.Is(err, Expired{}) {
		t.Fatalf("got %v, want Expired", err)
	}
	if !finished {
		t.Fatal("After returned before the child finished")
	}
}

func TestAfterParentStopInterrupts(t *testing.T) {
	clock := manual{make(chan time.Time)}
	parent := Root()
	_, err := After(clock, parent, time.Second, func(child *Task) (int, error) {
		parent.Stop()
		for !child.Stopped() {
			runtime.Gosched()
		}
		return 0, nil
	})
	if err != Interrupted {
		t.Fatalf("got %v, want Interrupted", err)
	}
	if !parent.Child().Stopped() {
		t.Fatal("a child of a stopped task must be stopped")
	}
}

func TestAfterRepanicsDefect(t *testing.T) {
	clock := manual{make(chan time.Time)}
	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("recovered %v, want boom", p)
		}
	}()
	_, _ = After(clock, Root(), time.Second, func(*Task) (int, error) { panic("boom") })
	t.Fatal("After returned after a defect")
}
