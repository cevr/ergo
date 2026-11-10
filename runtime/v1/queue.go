package v1

import (
	"fmt"
	"iter"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Queue is queue.Queue[T], the body of the std package queue
// (docs/spec/runtime.md, Queues): a FIFO of T between tasks with a fixed
// capacity. Push, Pop and Values take the caller's task as a hidden first
// argument, which the lowering passes (gap 1 ruling; docs/spec/stdlib.md,
// Hidden arguments); Poll, Len and End never wait and take none. The
// lowering calls these methods directly (stdlib.md, Types 3). The contract:
//
//   - New(capacity) makes an open, empty queue. capacity is how many values
//     it holds that no Pop has taken yet; 0 makes every Push wait for the
//     Pop that takes its value, as Go's unbuffered channel does. A negative
//     capacity is a defect.
//   - Push(v) adds v at the back. While the queue holds capacity values it
//     waits (backpressure); waiting pushes enter in the order they began.
//     Push on a closed queue, and a Push still waiting when the queue
//     closes, fail with Closed, and v never enters the queue.
//   - Pop() takes the value at the front, waiting while the queue is open
//     and empty. On a closed queue it still takes each value that entered
//     before the close; on a closed, empty queue it fails with Closed. Its
//     Go value is then the zero value, which the failure makes unreachable
//     to Ergo code: no value is invented (types.md, Zero values 4).
//   - Values() is the queue as a Seq[T] (Go's iter.Seq): each step is a
//     Pop, so it drains the queue in FIFO order and ends normally when the
//     queue is closed and empty. A loop that stops early (break, return,
//     try) takes no further value: the rest stay in the queue for the next
//     Pop or Values.
//   - End() ends admission. It is idempotent, never waits and never
//     fails. Values already in the queue stay for Pop and Values.
//   - Poll() takes the value Pop would take when one can be taken now, and
//     reports false otherwise; it never waits. Len() is the count of values
//     in the queue, between 0 and the capacity.
//
// Every value that a Push delivered (the Push returned no failure) is
// taken exactly once, by one Pop, Poll or step of Values, in the order the
// values entered; a value whose Push failed is never taken. Several tasks
// may Push, Pop and iterate one queue at once; the order in which waiting
// Pops are served is not specified.
//
// Push, Pop and each step of Values are interruption points of the caller's
// task (I1): when the task is stopped before the operation begins, or while
// it waits, the operation raises rt.Interrupted, an interruption, not
// Closed (a closed queue is a protocol outcome; a stop comes from outside).
// A stopped Push's value never enters the queue; a stopped Pop takes no
// value. A Push whose value entered at the moment of the stop returns
// success, and the stop is seen at the next interruption point. Inside a
// mask (I2) the waits are uninterruptible. Prior art: Go's select over a
// channel and ctx.Done(); Kotlin's suspending send and receive, cancellable
// while suspended; tokio's send and recv, dropped by a cancelled task.
type Queue[T any] struct {
	mu       sync.Mutex
	capacity int
	buf      []T          // the values that entered, oldest first; len <= capacity
	senders  []*sender[T] // the Pushes waiting for room, oldest first
	closed   bool
	// wake is closed, and cleared, at each change a waiter may wait for;
	// a waiter makes it. It is nil when nobody waits, so an uncontended
	// Push or Pop allocates nothing for it.
	wake chan struct{}
	// parked counts the Pushes and Pops parked in wait, which a change
	// wakes.
	parked int
	// domain holds the parked waiters that are members of an auto clock's
	// domain (rt idle.go), which a signal unparks before it wakes them.
	domain []*rt.Task
}

// Closed is queue.Closed, the typed failure of Push on a closed queue and
// of Pop on a closed, empty one. A closed queue is a normal end of the
// protocol, so it is in the operations' error rows, and a catch handles
// it; an interruption stays a stop from outside.
type Closed struct{}

// Error is the failure's text, its Ergo name.
func (Closed) Error() string { return "queue.Closed" }

// sender is a Push waiting for room.
type sender[T any] struct {
	v        T
	accepted bool // v entered the queue: into buf, or straight to a Pop
	rejected bool // the queue closed first: v never entered
}

// New is queue.New[T](capacity).
func New[T any](capacity int) *Queue[T] {
	if capacity < 0 {
		panic(fmt.Sprintf("queue.New: negative capacity %d", capacity))
	}
	return &Queue[T]{capacity: capacity}
}

// Push is q.Push(v) on the caller's task t: nil when v entered the queue,
// Closed when it did not. It raises rt.Interrupted, and v never enters the
// queue, when t is stopped before it begins or while it waits.
func (q *Queue[T]) Push(t *rt.Task, v T) error {
	t.Check()
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return Closed{}
	}
	if len(q.senders) == 0 && len(q.buf) < q.capacity {
		q.buf = append(q.buf, v)
		q.signal()
		q.mu.Unlock()
		return nil
	}
	s := &sender[T]{v: v}
	q.senders = append(q.senders, s)
	q.signal() // with capacity 0, a waiting Pop takes v straight away
	stop := t.Done()
	for !s.accepted && !s.rejected {
		if !q.wait(t, stop) && !s.accepted && !s.rejected {
			// Stopped first: v leaves the line and never enters the queue.
			q.dropSender(s)
			q.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
	rejected := s.rejected
	q.mu.Unlock()
	if rejected {
		return Closed{}
	}
	return nil
}

// Pop is q.Pop() on the caller's task t: the front value, or Closed on a
// closed, empty queue. It raises rt.Interrupted, and takes no value, when
// t is stopped before it begins or while it waits.
func (q *Queue[T]) Pop(t *rt.Task) (T, error) {
	t.Check()
	v, ok := q.take(t, t.Done())
	if !ok {
		return v, Closed{}
	}
	return v, nil
}

// Values is q.Values() on the caller's task t. Each step is a Pop on t,
// so a stop of t ends the loop with rt.Interrupted.
func (q *Queue[T]) Values(t *rt.Task) iter.Seq[T] {
	return func(yield func(T) bool) {
		for {
			t.Check()
			v, ok := q.take(t, t.Done())
			if !ok || !yield(v) {
				return
			}
		}
	}
}

// Poll is q.Poll(): the front value and true when a value can be taken now,
// else the zero value and false. It never waits, so it takes no task and is
// no interruption point. A value is taken as Pop takes it: the oldest in the
// buffer, or, on an empty buffer (capacity 0), the value of the oldest
// waiting Push, which then returns success. An empty queue gives false,
// open or closed. The lowering adapts (T, bool) to the program's Option[T]
// (stdlib.md, Types 5). Prior art: Go's select with a default case, and
// v, ok := <-ch; Effect's Queue.poll.
func (q *Queue[T]) Poll() (T, bool) {
	q.mu.Lock()
	v, ok := q.front()
	q.mu.Unlock()
	return v, ok
}

// Len is q.Len(): how many values are in the queue that no Pop, Poll or
// step of Values has taken yet. It is between 0 and the capacity; the
// value of a Push still waiting for room is not counted, so a queue of
// capacity 0 always has Len 0. It never waits. Prior art: Go's len(ch);
// Effect's Queue.size, without its negative count of waiting takers.
func (q *Queue[T]) Len() int {
	q.mu.Lock()
	n := len(q.buf)
	q.mu.Unlock()
	return n
}

// End is q.End().
func (q *Queue[T]) End() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		for i, s := range q.senders {
			s.rejected = true
			q.senders[i] = nil
		}
		q.senders = nil
		q.signal()
	}
	q.mu.Unlock()
}

// Drain is the release of a queue whose values are resources (#197 L8 B3;
// resources R13, Q7): it ends admission, as End does (a waiting Push is
// rejected, and its caller releases its value), and takes every value left,
// oldest first. The owner's generated release then releases them last
// entered first. It never waits.
func (q *Queue[T]) Drain() []T {
	q.End()
	q.mu.Lock()
	var out []T
	for {
		v, ok := q.front()
		if !ok {
			break
		}
		out = append(out, v)
	}
	q.mu.Unlock()
	return out
}

// take removes the front value, waiting while the queue is open and empty;
// ok is false on a closed, empty queue. When stop closes while it waits,
// it raises rt.Interrupted, having taken nothing.
func (q *Queue[T]) take(t *rt.Task, stop <-chan struct{}) (v T, ok bool) {
	q.mu.Lock()
	for {
		if v, ok = q.front(); ok {
			q.mu.Unlock()
			return v, true
		}
		if q.closed {
			q.mu.Unlock()
			return v, false
		}
		if !q.wait(t, stop) {
			q.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
}

// front removes and returns the front value without waiting: the oldest
// value in the buffer, whose room then goes to the oldest waiting Push, or,
// with an empty buffer (capacity 0, a rendezvous), the value of the oldest
// waiting Push. ok is false when no value can be taken now. q.mu is held.
func (q *Queue[T]) front() (v T, ok bool) {
	if len(q.buf) > 0 {
		v = q.buf[0]
		var zero T
		q.buf[0] = zero // the slot keeps no reference
		q.buf = q.buf[1:]
		if len(q.senders) > 0 {
			// The room goes to the oldest waiting Push.
			s := q.popSender()
			q.buf = append(q.buf, s.v)
			s.accepted = true
			q.signal()
		}
		return v, true
	}
	if len(q.senders) > 0 {
		// An empty buffer with a waiting Push: capacity 0, a rendezvous.
		s := q.popSender()
		s.accepted = true
		q.signal()
		return s.v, true
	}
	return v, false
}

// dropSender removes s, a Push that stopped waiting, from the line. q.mu
// is held.
func (q *Queue[T]) dropSender(s *sender[T]) {
	for i, w := range q.senders {
		if w == s {
			copy(q.senders[i:], q.senders[i+1:])
			q.senders[len(q.senders)-1] = nil
			q.senders = q.senders[:len(q.senders)-1]
			return
		}
	}
}

// popSender removes the oldest waiting Push. q.mu is held.
func (q *Queue[T]) popSender() *sender[T] {
	s := q.senders[0]
	q.senders[0] = nil
	q.senders = q.senders[1:]
	return s
}

// wait releases q.mu until the next change or until stop closes, then takes
// it again. It reports false when stop woke it; a nil stop (a masked task,
// I2) never does. A Push whose value entered at the same moment keeps its
// success; a Pop that stopped takes nothing, and the values stay queued.
func (q *Queue[T]) wait(t *rt.Task, stop <-chan struct{}) bool {
	if q.wake == nil {
		q.wake = make(chan struct{})
	}
	ch := q.wake
	q.parked++
	// In an auto clock's domain the waiter parks under q.mu, so the next
	// signal, which also runs under q.mu, unparks it before it wakes it
	// (rt idle.go). ready is nil: the waiter has just seen that it must
	// wait, and no change can come while q.mu is held.
	var p rt.Parked
	if t.InDomain() {
		p = rt.Park(t, q, stop, nil)
		q.domain = append(q.domain, t)
	}
	q.mu.Unlock()
	woke := true
	select {
	case <-ch:
	case <-stop:
		woke = false
	}
	q.mu.Lock()
	q.parked--
	if t.InDomain() {
		p.Unpark()
		q.dropDomain(t)
	}
	return woke
}

// signal wakes every waiter, which looks again. q.mu is held. A waiter of
// an auto clock's domain is unparked first (rt.Waking), so no window shows
// it parked while it can run.
func (q *Queue[T]) signal() {
	if q.wake != nil {
		for _, w := range q.domain {
			rt.Waking(w, q).Done()
		}
		close(q.wake)
		q.wake = nil
	}
}

// dropDomain removes t from the domain waiters. q.mu is held.
func (q *Queue[T]) dropDomain(t *rt.Task) {
	for i, w := range q.domain {
		if w == t {
			q.domain = append(q.domain[:i], q.domain[i+1:]...)
			return
		}
	}
}
