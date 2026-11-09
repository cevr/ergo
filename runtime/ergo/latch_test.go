package ergo

import (
	"context"
	"errors"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

func waitLatchSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-testwait.After(t):
		t.Fatal("managed operation did not complete")
	}
}

func TestLatchIsOneShotAndCancellablePerWaiter(t *testing.T) {
	latch := NewLatch()
	if !latch.Signal() || latch.Signal() {
		t.Fatal("latch completion was not idempotent")
	}
	if err := latch.Await(context.Background()); err != nil {
		t.Fatalf("signal before wait: %v", err)
	}

	independent := NewLatch()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	waiter := make(chan error, 1)
	go func() {
		close(started)
		waiter <- independent.Await(ctx)
	}()
	waitLatchSignal(t, started)
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if independent.IsSignaled() {
		t.Fatal("cancelling a waiter completed the shared latch")
	}
	if !independent.Signal() || !independent.IsSignaled() {
		t.Fatal("latch could not complete after a waiter was cancelled")
	}
	if err := independent.Await(context.Background()); err != nil {
		t.Fatalf("second waiter after signal: %v", err)
	}

	many := NewLatch()
	const waiters = 8
	var group sync.WaitGroup
	group.Add(waiters)
	for range waiters {
		go func() {
			defer group.Done()
			if err := many.Await(context.Background()); err != nil {
				t.Errorf("multiple waiter: %v", err)
			}
		}()
	}
	if !many.Signal() {
		t.Fatal("multiple waiter signal did not complete")
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	waitLatchSignal(t, done)
}
