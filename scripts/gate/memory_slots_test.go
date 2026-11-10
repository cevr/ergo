package main

import (
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestSlotPoolReservesMemoryHeavySteps catches admitting a model search
// alongside another step, or letting later small steps bypass its reservation.
// The oracle counts slots and waits for queue events, never elapsed time.
func TestSlotPoolReservesMemoryHeavySteps(t *testing.T) {
	pool := newSlotPool(4)
	pool.acquire(false)
	model := make(chan struct{})
	go func() { pool.acquireN(false, 100, 4); close(model) }()
	if !testwait.Until(t, func() bool { _, n := pool.waiting(); return n == 1 }) {
		t.Fatal("model did not queue behind the running step")
	}
	small := make(chan struct{})
	go func() { pool.acquire(false); close(small) }()
	if !testwait.Until(t, func() bool { _, n := pool.waiting(); return n == 2 }) {
		t.Fatal("small step bypassed the model reservation")
	}
	pool.release()
	<-model
	pool.mu.Lock()
	free := pool.free
	pool.mu.Unlock()
	if free != 0 {
		t.Fatalf("model left %d slots free; want exclusive ownership", free)
	}
	select {
	case <-small:
		t.Fatal("small step ran alongside the model")
	default:
	}
	pool.releaseN(4)
	<-small
	pool.release()
	if pool.free != 4 {
		t.Fatalf("released pool has %d free slots, want 4", pool.free)
	}
	pool.acquireN(false, 100, 4)
	if pool.free != 0 {
		t.Fatalf("immediate model reservation left %d slots free", pool.free)
	}
	pool.releaseN(4)
}
