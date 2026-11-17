package v1

import (
	"sync"
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
	// waiter is the task in PopBefore, which the fire wakes in a clock's
	// domain (rt.Waking).
	waiter *rt.Task
}

// NewDeadline is timeout.NewDeadline() on the caller's task t: a deadline
// that is not armed, on the AlarmClock of s, the program's Scheduler. A
// Scheduler gives its AlarmClock (Alarmer): every program's does, since
// Alarms is an operation of the prelude's Scheduler. A Go Sleeper of a
// test with no Alarms is a defect.
//
//ergo:wait caller
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
}

// Armed reports whether dl is armed: Arm armed it, and PopBefore has not
// taken its fire nor Stop disarmed it. A fire PopBefore has not yet taken
// keeps it armed.
//
//ergo:wait none
func (dl *Deadline) Armed() bool {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.fired != nil
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
			}
			dl.mu.Unlock()
			return zero, Expired{}
		}
		if !q.waitBefore(t, stop, dl, ch) {
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

// waitBefore is wait with a deadline: it releases q.mu until the next
// change, the fire of dl (ch closes) or a stop, then takes it again. It
// reports false on a stop. In an auto clock's domain the waiter parks on
// q and on dl, whose fire wakes it through rt.Waking (Deadline.fire);
// ready keeps it from parking on a fire that has come. A nil ch (dl not
// armed) never fires.
func (q *Queue[T]) waitBefore(t *rt.Task, stop <-chan struct{}, dl *Deadline, ch chan rt.Instant) bool {
	wake := q.enlist()
	q.parked++
	var p rt.Parked
	if t.InDomain() {
		p = rt.ParkEither(t, q, dl, stop, func() bool { return ch != nil && closedChan(ch) })
		q.domain = append(q.domain, t)
	}
	q.mu.Unlock()
	rt.Block(t)
	woke := true
	select {
	case <-wake:
	case <-ch:
	case <-stop:
		woke = false
	}
	rt.Unblock(t)
	q.mu.Lock()
	q.delist(wake)
	q.parked--
	if t.InDomain() {
		p.Unpark()
		q.dropDomain(t)
	}
	return woke
}
