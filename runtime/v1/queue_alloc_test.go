package v1

import (
	"runtime"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// alarmSleeper is a Go Scheduler with the host's alarms, for NewDeadline.
type alarmSleeper struct{}

func (alarmSleeper) Sleep(t *rt.Task, d time.Duration) {}
func (alarmSleeper) Alarms(*rt.Task) *AlarmClock       { return HostAlarms() }

// TestRendezvousWaitsOnceAllocatingNothing counts the waits and the heap
// allocations of n values through a queue of capacity 0, taken by
// PopBefore under one deadline that stays armed (stream.Debounced keeping
// up with its source). Each value needs one wait, of one side: a Push that
// finds the Pop waiting hands it the value and returns, and a Pop that
// finds a Push waiting takes its value and wakes it (Go's unbuffered
// channel); a Push that waits for a waiting Pop's take makes two. Once
// the queue has its wake channels, senders and takers, a wait allocates
// nothing, so the allocations are far under one per value; one made per
// wait gives n or more. Mutants queue-push-waits-for-taker,
// queue-wait-makes-wake, queue-push-new-sender, queue-sender-kept-dirty
// (runtime/v1 ledger).
func TestRendezvousWaitsOnceAllocatingNothing(t *testing.T) {
	const n = 2000
	q := New[int](0)
	done := make(chan struct{})
	c := rt.Root("consumer")
	dl := NewDeadline(c, alarmSleeper{})
	dl.Arm(time.Hour)
	defer dl.Stop()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	go func() {
		defer close(done)
		p := rt.Root("producer")
		for i := 0; i < n; i++ {
			if err := q.Push(p, i); err != nil {
				panic(err)
			}
		}
		q.End()
	}()
	k := 0
	for {
		if _, err := PopBefore(c, q, dl); err != nil {
			if _, ok := err.(Closed); !ok {
				t.Fatalf("PopBefore failed %v after %d values", err, k)
			}
			break
		}
		k++
	}
	<-done
	runtime.ReadMemStats(&after)
	if k != n {
		t.Fatalf("took %d values, want %d", k, n)
	}
	if got := after.Mallocs - before.Mallocs; got > n/4 {
		t.Fatalf("%d values through a rendezvous made %d allocations, want under %d: a wait allocates", n, got, n/4)
	}
	q.mu.Lock()
	waits := q.waits
	q.mu.Unlock()
	if waits > n+n/4 {
		t.Fatalf("%d values through a rendezvous took %d waits, want about one per value: a Push waits for the Pop it handed its value to", n, waits)
	}
}
