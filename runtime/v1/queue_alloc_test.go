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

// TestRendezvousWaitsAllocateNothing counts the heap allocations of n
// values through a queue of capacity 0, taken by PopBefore under one
// deadline that stays armed (stream.Debounced keeping up with its source).
// Every Push waits for its Pop, so each value is at least one wait on each
// side. Once the queue has its wake channels and its sender, a wait
// allocates nothing (the floor of Go's unbuffered channel), so the count
// is far under one per value; a wake channel or a sender made per wait
// gives n or more. Mutants queue-wait-makes-wake and queue-push-new-sender
// (runtime/v1 ledger).
func TestRendezvousWaitsAllocateNothing(t *testing.T) {
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
}
