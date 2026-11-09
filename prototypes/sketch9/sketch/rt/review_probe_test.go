package rt

import (
	"runtime"
	"testing"
	"time"
)

func TestReviewAfterDeadlineKeepsDefect(t *testing.T) {
	clock := manual{make(chan time.Time)}
	go func() { clock.fire <- time.Time{} }()
	defer func() {
		if p := recover(); p != "cleanup defect" {
			t.Fatalf("recovered %v, want cleanup defect", p)
		}
	}()
	_, err := After(clock, Root(), time.Hour, func(child *Task) (int, error) {
		for !child.Stopped() {
			runtime.Gosched()
		}
		panic("cleanup defect")
	})
	t.Fatalf("returned %v instead of propagating defect", err)
}

func TestReviewAfterParentStopKeepsDefect(t *testing.T) {
	clock := manual{make(chan time.Time)}
	parent := Root()
	defer func() {
		if p := recover(); p != "cleanup defect" {
			t.Fatalf("recovered %v, want cleanup defect", p)
		}
	}()
	_, err := After(clock, parent, time.Hour, func(child *Task) (int, error) {
		parent.Stop()
		<-child.stop
		panic("cleanup defect")
	})
	t.Fatalf("returned %v instead of propagating defect", err)
}

func TestReviewAfterAncestorStopReturnsInterruption(t *testing.T) {
	clock := manual{make(chan time.Time)}
	root := Root()
	parent := root.Child()
	_, err := After(clock, parent, time.Hour, func(child *Task) (int, error) {
		root.Stop()
		for !child.Stopped() {
			runtime.Gosched()
		}
		return 42, nil
	})
	if err != Interrupted {
		t.Fatalf("got %v, want Interrupted", err)
	}
}
