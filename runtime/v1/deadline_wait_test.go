package v1

import (
	"errors"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// heldAlarm is an alarm the test fires by hand.
type heldAlarm struct{}

func (heldAlarm) Stop() bool { return true }

// TestPopBeforeEndsOnAFireWhileParked arms a deadline on a clock the test
// fires by hand, waits until PopBefore sleeps on the empty queue, and
// fires: PopBefore fails Expired, its wait woken by the fire
// (Deadline.fire wakes the word the wait parks on, WAITOR-WORD), and the
// ended wait has left the queue's waiters. Mutants
// (testdata/mutants.json): deadline-fire-skips-wake,
// queue-delist-keeps-waiter.
func TestPopBeforeEndsOnAFireWhileParked(t *testing.T) {
	fire := make(chan func(), 1)
	dl := &Deadline{clock: NewAlarmClock(func() time.Duration { return 0 }, func(d time.Duration, f func()) Alarm {
		fire <- f
		return heldAlarm{}
	})}
	dl.Arm(time.Hour)
	q := New[int](0)
	got := make(chan error, 1)
	go func() {
		_, err := PopBefore(rt.Root("pop"), q, dl)
		got <- err
	}()
	if !testwait.Until(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.parked == 1
	}) {
		t.Fatal("PopBefore never waited on the empty queue")
	}
	(<-fire)()
	if err := <-got; !errors.As(err, new(Expired)) {
		t.Fatalf("PopBefore on a fire while it waits returned %v; want Expired", err)
	}
	q.mu.Lock()
	left := len(q.waiters)
	q.mu.Unlock()
	if left != 0 {
		t.Fatalf("the wait the fire ended left %d waiters; want 0: ended waits pile up until a signal", left)
	}
}
