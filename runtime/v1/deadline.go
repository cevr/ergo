package v1

import (
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Deadline is timeout.Deadline (docs/spec/runtime.md, Timeouts 10-11): one
// re-armable deadline on the program's Scheduler, kept across waits. Arm
// arms one alarm on the Scheduler's AlarmClock (Scheduler.Alarms): no task
// and no sleep, time.AfterFunc on the host and the timeline on a
// time.Manual. PopBefore waits for a queue's value or the deadline's fire,
// and leaves the deadline armed when a value comes first, so a loop that
// takes many values under one deadline arms it once (Debounced: one alarm
// per window of d, not per value). Prior art: Go's time.Timer with Reset;
// lodash's debounce, which re-arms for the remaining wait when its timer
// fires early.
//
// One task waits on a Deadline at a time; Arm, Armed and Stop may be
// called from any task.
type Deadline struct {
	clock *AlarmClock
	mu    sync.Mutex
	alarm Alarm
	// gen counts the arms, so the fire of an alarm that Arm or Stop
	// replaced does nothing.
	gen uint64
	// fired is the channel the current arm's fire closes: nil when the
	// deadline is not armed, or its fire was taken by PopBefore.
	fired chan rt.Instant
	// armed is fired != nil, stored under mu at each change of fired, so
	// Armed reads it with no lock (Debounced asks it per value).
	armed atomic.Bool
	// waiter is the task in PopBefore, which the fire wakes in a clock's
	// domain (rt.Waking).
	waiter *rt.Task
	// wake is the word that waiter's wait parks on (Queue.waitOr), which
	// the fire wakes; nil while it does not wait.
	wake *rt.Waiter
}

// NewDeadline is timeout.NewDeadline() on the caller's task t: a deadline
// that is not armed, on the AlarmClock of s, the program's Scheduler. A
// Scheduler gives its AlarmClock (Alarmer): every program's does, since
// Alarms is an operation of the prelude's Scheduler. A Go Sleeper of a
// test with no Alarms is a defect. It takes t only to ask s for its
// AlarmClock, and never waits: its wait policy is none, which
// TestRuntime199NoWaitBindings checks (BLOCKING-DEADLINE).
//
//ergo:wait none
func NewDeadline(t *rt.Task, s Sleeper) *Deadline {
	a, ok := s.(Alarmer)
	if !ok {
		panic("timeout.NewDeadline: the Scheduler gives no AlarmClock")
	}
	return &Deadline{clock: a.Alarms(t)}
}

// Arm arms dl to fire once d has passed, replacing an earlier arm and a
// fire PopBefore has not taken. A d of zero or less fires at once.
//
//ergo:wait none
func (dl *Deadline) Arm(d time.Duration) {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.alarm != nil {
		dl.alarm.Stop()
	}
	dl.gen++
	gen := dl.gen
	ch := make(chan rt.Instant)
	dl.fired = ch
	dl.armed.Store(true)
	dl.alarm = dl.clock.After(d, func() { dl.fire(gen, ch) })
}

// fire is the alarm's function: it closes ch, unless a later Arm or a
// Stop replaced the arm gen, and wakes the waiting task.
func (dl *Deadline) fire(gen uint64, ch chan rt.Instant) {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.gen != gen {
		return
	}
	dl.alarm = nil
	w := rt.Waking(dl.waiter, dl)
	close(ch)
	w.Done()
	if dl.wake != nil {
		// PopBefore parks on its wait's word (Queue.waitOr): the fire wakes
		// it after ch is closed, so it sees the fire.
		dl.wake.Wake()
	}
}

// await sets the word the fire wakes, for the wait of PopBefore that
// parks on w (nil when it ended). A fire that came before it wakes w at
// once, so no fire is lost between the waiter's look and its park.
func (dl *Deadline) await(w *rt.Waiter, fired chan rt.Instant) {
	dl.mu.Lock()
	dl.wake = w
	if w != nil && fired != nil && closedChan(fired) {
		w.Wake()
	}
	dl.mu.Unlock()
}

// Armed reports whether dl is armed: Arm armed it, and PopBefore has not
// taken its fire nor Stop disarmed it. A fire PopBefore has not yet taken
// keeps it armed.
//
//ergo:wait none
func (dl *Deadline) Armed() bool {
	return dl.armed.Load()
}

// Stop disarms dl. It does nothing on a deadline that is not armed.
//
//ergo:wait none
func (dl *Deadline) Stop() {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.alarm != nil {
		dl.alarm.Stop()
		dl.alarm = nil
	}
	dl.gen++
	dl.fired = nil
	dl.armed.Store(false)
}

// PopBefore is timeout.PopBefore(q, dl) on the caller's task t: the front
// value of q, waiting for one until dl fires. It fails Expired when dl has
// fired, taking the fire (dl is then not armed), and Closed on a closed,
// empty queue. A value that can be taken now is taken first, even when dl
// has fired; a value that comes before the fire leaves dl armed. On a
// deadline that is not armed it waits as Pop does. Like Pop, a stop of t
// raises rt.Interrupted, having taken nothing.
//
//ergo:wait caller
func PopBefore[T any](t *rt.Task, q *Queue[T], dl *Deadline) (T, error) {
	rt.Point(t, rt.Op{Kind: rt.OpPop, On: q})
	t.Check()
	return popBefore(t, q, dl)
}

// popBefore is PopBefore after its interruption point.
func popBefore[T any](t *rt.Task, q *Queue[T], dl *Deadline) (T, error) {
	var zero T
	q.mu.Lock()
	if v, ok := q.front(); ok {
		q.mu.Unlock()
		return v, nil
	}
	if q.closed {
		q.mu.Unlock()
		return zero, Closed{}
	}
	dl.mu.Lock()
	ch := dl.fired
	dl.waiter = t
	dl.mu.Unlock()
	defer func() {
		dl.mu.Lock()
		dl.waiter = nil
		dl.mu.Unlock()
	}()
	stop := t.Done()
	for {
		if v, ok := q.front(); ok {
			q.mu.Unlock()
			return v, nil
		}
		if q.closed {
			q.mu.Unlock()
			return zero, Closed{}
		}
		if ch != nil && closedChan(ch) {
			q.mu.Unlock()
			dl.mu.Lock()
			if dl.fired == ch {
				dl.fired = nil
				dl.armed.Store(false)
			}
			dl.mu.Unlock()
			return zero, Expired{}
		}
		tk := q.enlistTaker()
		woke := q.waitOr(t, stop, dl, ch)
		if v, ok := q.delistTaker(tk); ok {
			// A Push handed v over: it is taken, even when the deadline
			// fired or a stop came at the same moment.
			q.mu.Unlock()
			return v, nil
		}
		if !woke {
			q.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
}

// closedChan reports whether ch is closed, without waiting.
func closedChan(ch chan rt.Instant) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// PopWithin is timeout.PopWithin(q, d) on the caller's task t: PopBefore
// with a deadline of its own, armed only when it must wait and stopped
// before it returns. A value that can be taken now is taken with no alarm,
// so a burst arms none. A d of zero or less fails Expired on an empty,
// open queue. s is the program's Scheduler, whose AlarmClock arms the
// deadline.
//
//ergo:wait caller
func PopWithin[T any](t *rt.Task, s Sleeper, q *Queue[T], d time.Duration) (T, error) {
	rt.Point(t, rt.Op{Kind: rt.OpPop, On: q})
	t.Check()
	var zero T
	q.mu.Lock()
	if v, ok := q.front(); ok {
		q.mu.Unlock()
		return v, nil
	}
	if q.closed {
		q.mu.Unlock()
		return zero, Closed{}
	}
	q.mu.Unlock()
	if d <= 0 {
		return zero, Expired{}
	}
	dl := NewDeadline(t, s)
	dl.Arm(d)
	defer dl.Stop()
	return popBefore(t, q, dl)
}
