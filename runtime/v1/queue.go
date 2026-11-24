package v1

import (
	"iter"
	"strconv"
	"sync"
	"sync/atomic"

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
	buf      []T // the values that entered, oldest first; len <= capacity
	// size is len(buf), stored under mu at each change of buf, so Len reads
	// it without the lock, as Go's len(ch) reads a channel's count. A read
	// sees the count of one moment between two changes.
	size    atomic.Int64
	senders []*sender[T] // the Pushes waiting for room, oldest first
	closed  bool
	// waiters holds the park word of each waiting Push and Pop
	// (rt.Waiter, WAITOR-WORD); a change a waiter may wait for wakes each
	// and empties the list (signal), so a signal sends on no channel: it
	// readies a parked waiter's goroutine as Go's channel readies a parked
	// receiver's (runtime/chan.go). A wait has a word of its own, not its
	// task's, because Go code may run two goroutines on one task. spare
	// keeps the words of ended waits for the next ones, so a wait
	// allocates nothing once the queue has seen as many waiters at once
	// (Go's channels keep a sudog per blocked goroutine the same way).
	waiters []*rt.Waiter
	spare   []*rt.Waiter
	// spareSender is the sender of the last Push that waited, kept for
	// the next one, so a rendezvous (capacity 0) allocates no sender.
	spareSender *sender[T]
	// takers are the Pops waiting on an empty queue, oldest first. A Push
	// that finds one, with no value before its own, hands its value to
	// the oldest and returns: one wake, and no wait of the Push (Go's
	// send to a parked receiver, runtime/chan.go). spareTakers keeps the
	// takers of ended waits for the next ones.
	takers      []*taker[T]
	spareTakers []*taker[T]
	// parked counts the Pushes and Pops parked in wait, which a change
	// wakes. waits counts every wait the queue has had, a measure of its
	// work that tests read (a rendezvous costs one wait per value).
	parked int
	waits  int
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

// taker is a Pop waiting on an empty queue: a Push hands it v and sets
// got (handTaker).
type taker[T any] struct {
	v   T
	got bool
}

// New is queue.New[T](capacity).
//
//ergo:wait none
func New[T any](capacity int) *Queue[T] {
	if capacity < 0 {
		panic("queue.New: negative capacity " + strconv.Itoa(capacity))
	}
	q := &Queue[T]{capacity: capacity}
	if rt.Exploring {
		nameHandle([]string{"queue.New"}, "", q)
	}
	return q
}

// OpenQueue is queue.OpenQueue[T]: a bounded FIFO queue whose admission
// never ends, so Push, Pop and Offer never fail with Closed.
type OpenQueue[T any] struct {
	q *Queue[T]
}

// NewOpen is queue.NewOpen[T](capacity).
//
//ergo:wait none
func NewOpen[T any](capacity int) *OpenQueue[T] {
	if capacity < 0 {
		panic("queue.NewOpen: negative capacity " + strconv.Itoa(capacity))
	}
	q := &Queue[T]{capacity: capacity}
	if rt.Exploring {
		nameHandle([]string{"queue.NewOpen"}, "", q)
	}
	return &OpenQueue[T]{q: q}
}

// Push is o.Push(v) on the caller's task t: waits while full, never closes.
//
//ergo:wait caller
func (o *OpenQueue[T]) Push(t *rt.Task, v T) {
	_ = o.q.Push(t, v)
}

// Pop is o.Pop() on the caller's task t: waits while empty, never closes.
//
//ergo:wait caller
func (o *OpenQueue[T]) Pop(t *rt.Task) T {
	v, _ := o.q.Pop(t)
	return v
}

// Offer is o.Offer(v): enters now or fails Full, never closes.
//
//ergo:wait none
func (o *OpenQueue[T]) Offer(v T) error {
	return o.q.Offer(v)
}

// Poll is o.Poll(): takes the front value if present, else false.
//
//ergo:wait none
func (o *OpenQueue[T]) Poll() (T, bool) {
	return o.q.Poll()
}

// Cap is o.Cap(): the capacity NewOpen gave the queue.
//
//ergo:wait none
func (o *OpenQueue[T]) Cap() int {
	return o.q.Cap()
}

// Len is o.Len(): untaken values currently buffered.
//
//ergo:wait none
func (o *OpenQueue[T]) Len() int {
	return o.q.Len()
}

// Push is q.Push(v) on the caller's task t: nil when v entered the queue,
// Closed when it did not. It raises rt.Interrupted, and v never enters the
// queue, when t is stopped before it begins or while it waits.
//
//ergo:wait caller
func (q *Queue[T]) Push(t *rt.Task, v T) error {
	rt.Point(t, rt.Op{Kind: rt.OpPush, On: q})
	t.Check()
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return Closed{}
	}
	if len(q.senders) == 0 && len(q.buf) == 0 && len(q.takers) > 0 {
		// A Pop waits on the empty queue: v goes to it, which takes v.
		q.handTaker(v)
		q.mu.Unlock()
		return nil
	}
	if len(q.senders) == 0 && len(q.buf) < q.capacity {
		q.buf = append(q.buf, v)
		q.size.Store(int64(len(q.buf)))
		q.signal()
		q.mu.Unlock()
		return nil
	}
	s := q.spareSender
	if s != nil {
		q.spareSender = nil
		s.v = v
	} else {
		s = &sender[T]{v: v}
	}
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
	// s has left the line (front or End took it out), so nothing else
	// holds it: it is kept, emptied, for the next Push that waits.
	*s = sender[T]{}
	q.spareSender = s
	q.mu.Unlock()
	if rejected {
		return Closed{}
	}
	return nil
}

// Pop is q.Pop() on the caller's task t: the front value, or Closed on a
// closed, empty queue. It raises rt.Interrupted, and takes no value, when
// t is stopped before it begins or while it waits.
//
//ergo:wait caller
func (q *Queue[T]) Pop(t *rt.Task) (T, error) {
	rt.Point(t, rt.Op{Kind: rt.OpPop, On: q})
	t.Check()
	v, ok := q.take(t)
	if !ok {
		return v, Closed{}
	}
	return v, nil
}

// Values is q.Values() on the caller's task t. Each step is a Pop on t,
// so a stop of t ends the loop with rt.Interrupted.
//
//ergo:wait caller
func (q *Queue[T]) Values(t *rt.Task) iter.Seq[T] {
	return func(yield func(T) bool) {
		for {
			rt.Point(t, rt.Op{Kind: rt.OpPop, On: q})
			t.Check()
			v, ok := q.take(t)
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
//
//ergo:wait none
func (q *Queue[T]) Poll() (T, bool) {
	rt.PointRunning(rt.Op{Kind: rt.OpPop, On: q})
	q.mu.Lock()
	v, ok := q.front()
	q.mu.Unlock()
	return v, ok
}

// Full is queue.Full, the typed failure of Offer on a queue that has no
// room now. A full queue is an outcome a producer that must not wait
// handles (it drops the value, or slides), so it is in Offer's row.
type Full struct{}

// Error is the failure's text, its Ergo name.
func (Full) Error() string { return "queue.Full" }

// Offer is q.Offer(v): v enters at the back now, or not at all. It fails
// with Closed on a closed queue and with Full when the queue holds its
// capacity, or when earlier Pushes still wait for room (they come first).
// It never waits, so it takes no task and is no interruption point. On a
// queue of capacity 0 it always fails Full: there is never room. Prior
// art: Go's select with a send and a default case; Effect's Queue.offer
// on a dropping queue, which answers false.
//
//ergo:wait none
func (q *Queue[T]) Offer(v T) error {
	rt.PointRunning(rt.Op{Kind: rt.OpPush, On: q})
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return Closed{}
	}
	if len(q.senders) > 0 || len(q.buf) >= q.capacity {
		return Full{}
	}
	q.buf = append(q.buf, v)
	q.size.Store(int64(len(q.buf)))
	q.signal()
	return nil
}

// Slide is q.Slide(v): v enters at the back now, never waiting; when the
// queue holds its capacity, the value at the front leaves it first and is
// given back, Some(old), so its caller owns it and releases it when it is a
// resource (resources R42), as Poll's caller does (Effect's sliding
// strategy: the newest values stay). The queue never releases it: a
// release is the element type's Ergo code, which may need capabilities the
// queue does not hold. Eviction and
// entry are one step under the queue's lock, so v enters exactly once and
// no other producer can take the room between them: a sliding value is
// never lost while an older one stays. A Push still waiting for room keeps
// waiting: v does not wait, so it enters before them. It fails with Closed
// on a closed queue, and a capacity of 0 is a defect: there is never room
// to slide into. Prior art: Effect's Queue.sliding; Java's
// ArrayBlockingQueue has no sliding offer and needs poll and offer under
// one lock to build it.
//
//ergo:wait none
func (q *Queue[T]) Slide(v T) (T, bool, error) {
	return q.slide(v)
}

// slide is Slide, giving the value it dropped and whether it dropped one
// (the tests read them).
func (q *Queue[T]) slide(v T) (old T, dropped bool, err error) {
	rt.PointRunning(rt.Op{Kind: rt.OpPush, On: q})
	if q.capacity == 0 {
		panic("queue.Slide: a queue of capacity 0 has no room to slide into")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return old, false, Closed{}
	}
	if len(q.buf) >= q.capacity {
		old, dropped = q.buf[0], true
		var zero T
		q.buf[0] = zero // the slot keeps no reference
		q.buf = q.buf[1:]
	}
	q.buf = append(q.buf, v)
	q.size.Store(int64(len(q.buf)))
	q.signal()
	return old, dropped, nil
}

// Cap is q.Cap(): the capacity New gave the queue. It never waits.
//
//ergo:wait none
func (q *Queue[T]) Cap() int {
	return q.capacity
}

// Len is q.Len(): how many values are in the queue that no Pop, Poll or
// step of Values has taken yet. It is between 0 and the capacity; the
// value of a Push still waiting for room is not counted, so a queue of
// capacity 0 always has Len 0. It never waits, and takes no lock: it reads
// the count each change of the buffer stores (std-speed-1). Prior art: Go's
// len(ch), which reads the channel's count without its lock too; Effect's
// Queue.size, without its negative count of waiting takers.
//
//ergo:wait none
func (q *Queue[T]) Len() int {
	rt.PointRunning(rt.Op{Kind: rt.OpRead, On: q})
	return int(q.size.Load())
}

// End is q.End().
//
//ergo:wait none
func (q *Queue[T]) End() {
	rt.PointRunning(rt.Op{Kind: rt.OpClose, On: q})
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

// Drain is the release of a queue whose values are resources (resource ownership L8 B3;
// resources R13, Q7): it ends admission, as End does (a waiting Push is
// rejected, and its caller releases its value), and takes every value left,
// oldest first. The owner's generated release then releases them last
// entered first. It never waits.
//
//ergo:wait none
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
// ok is false on a closed, empty queue. When a stop of t comes while it
// waits, it raises rt.Interrupted, having taken nothing.
func (q *Queue[T]) take(t *rt.Task) (v T, ok bool) {
	q.mu.Lock()
	// t's stop channel is asked for only when take must wait: Done is a
	// wait of the runtime, where held output is flushed (stdlib.md io 11),
	// and a take that finds a value waits on nothing.
	var stop <-chan struct{}
	asked := false
	for {
		if v, ok = q.front(); ok {
			q.mu.Unlock()
			return v, true
		}
		if q.closed {
			q.mu.Unlock()
			return v, false
		}
		if !asked {
			stop, asked = t.Done(), true
		}
		tk := q.enlistTaker()
		woke := q.wait(t, stop)
		if v, ok = q.delistTaker(tk); ok {
			// A Push handed v over: it is taken, even when a stop came
			// at the same moment (the stop is seen at the next point).
			q.mu.Unlock()
			return v, true
		}
		if !woke {
			q.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
}

// enlistTaker is a waiting Pop's taker, from spareTakers or new, in the
// takers a Push hands its value to. q.mu is held.
func (q *Queue[T]) enlistTaker() *taker[T] {
	var tk *taker[T]
	if n := len(q.spareTakers); n > 0 {
		tk = q.spareTakers[n-1]
		q.spareTakers[n-1] = nil
		q.spareTakers = q.spareTakers[:n-1]
	} else {
		tk = new(taker[T])
	}
	q.takers = append(q.takers, tk)
	return tk
}

// delistTaker ends a Pop's wait as tk: the value a Push handed it, and
// true, or false when none did (tk then leaves the takers). tk is kept,
// empty, in spareTakers. q.mu is held.
func (q *Queue[T]) delistTaker(tk *taker[T]) (v T, ok bool) {
	if tk.got {
		v, ok = tk.v, true
	} else {
		for i, w := range q.takers {
			if w == tk {
				last := len(q.takers) - 1
				copy(q.takers[i:], q.takers[i+1:])
				q.takers[last] = nil
				q.takers = q.takers[:last]
				break
			}
		}
	}
	*tk = taker[T]{}
	q.spareTakers = append(q.spareTakers, tk)
	return v, ok
}

// handTaker hands v to the oldest waiting Pop and wakes it. q.mu is held.
func (q *Queue[T]) handTaker(v T) {
	tk := q.takers[0]
	q.takers[0] = nil
	if len(q.takers) == 1 {
		q.takers = q.takers[:0]
	} else {
		q.takers = q.takers[1:]
	}
	tk.v, tk.got = v, true
	q.signal()
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
		q.size.Store(int64(len(q.buf)))
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

// popSender removes the oldest waiting Push. q.mu is held. The line's
// last Push leaves it empty on the same array, so a Push that waits next
// appends with no allocation.
func (q *Queue[T]) popSender() *sender[T] {
	s := q.senders[0]
	q.senders[0] = nil
	if len(q.senders) == 1 {
		q.senders = q.senders[:0]
	} else {
		q.senders = q.senders[1:]
	}
	return s
}

// wait releases q.mu until the next change or until stop closes, then takes
// it again. It reports false when stop woke it; a nil stop (a masked task,
// I2) never does. A Push whose value entered at the same moment keeps its
// success; a Pop that stopped takes nothing, and the values stay queued.
func (q *Queue[T]) wait(t *rt.Task, stop <-chan struct{}) bool {
	return q.waitOr(t, stop, nil, nil)
}

// waitOr is wait that also ends on the fire of dl (fired closes; a nil
// fired never does), for PopBefore. It is the one place a queue's waiter
// parks. The waiter parks on a word of its own (rt.Waiter, from spare): a
// signal, the fire of dl (Deadline.fire) and a stop of t (rt's Interrupt)
// each wake it, and it looks again; a wake that changed nothing only
// makes its caller look again too. In an auto clock's domain the waiter
// parks under q.mu, so the next signal, which also runs under q.mu,
// unparks it before it wakes it (rt idle.go): on q alone, with a nil
// ready, since the waiter has just seen that it must wait and no change
// can come while q.mu is held; or on q and dl, whose fire wakes it
// through rt.Waking, with a ready that keeps it from parking on a fire
// that has come.
func (q *Queue[T]) waitOr(t *rt.Task, stop <-chan struct{}, dl *Deadline, fired chan rt.Instant) bool {
	w := q.enlist()
	q.parked++
	q.waits++
	var p rt.Parked
	if t.InDomain() {
		if dl == nil {
			p = rt.Park(t, q, stop, nil)
		} else {
			p = rt.ParkEither(t, q, dl, stop, func() bool { return fired != nil && closedChan(fired) })
		}
		q.domain = append(q.domain, t)
	}
	if dl != nil {
		dl.await(w, fired)
	}
	q.mu.Unlock()
	rt.Block(t)
	// A nil stop (a masked task) never ends the wait.
	w.Park(t, stop != nil)
	rt.Unblock(t)
	if dl != nil {
		dl.await(nil, nil)
	}
	q.mu.Lock()
	q.delist(w)
	q.parked--
	if t.InDomain() {
		p.Unpark()
		q.dropDomain(t)
	}
	return stop == nil || !t.Stopped()
}

// enlist is a wait's word, from spare or new, in the waiters that the
// next signal wakes. q.mu is held.
func (q *Queue[T]) enlist() *rt.Waiter {
	var w *rt.Waiter
	if n := len(q.spare); n > 0 {
		w = q.spare[n-1]
		q.spare[n-1] = nil
		q.spare = q.spare[:n-1]
	} else {
		w = new(rt.Waiter)
	}
	q.waiters = append(q.waiters, w)
	return w
}

// delist ends the wait on w: it leaves the waiters if no signal took it
// out (a stop or a deadline ended the wait), so the list does not grow
// with waits that ended, and it is kept in spare. A wake left on it
// after its wait ended (a signal or a fire that raced the stop) only
// makes the next wait that takes it look once more. q.mu is held.
func (q *Queue[T]) delist(w *rt.Waiter) {
	for i, x := range q.waiters {
		if x == w {
			last := len(q.waiters) - 1
			copy(q.waiters[i:], q.waiters[i+1:])
			q.waiters[last] = nil
			q.waiters = q.waiters[:last]
			break
		}
	}
	q.spare = append(q.spare, w)
}

// signal wakes every waiter, which looks again. q.mu is held. A waiter of
// an auto clock's domain is unparked first (rt.Waking), so no window shows
// it parked while it can run.
func (q *Queue[T]) signal() {
	if len(q.waiters) > 0 {
		for _, w := range q.domain {
			rt.Waking(w, q).Done()
		}
		for i, w := range q.waiters {
			w.Wake()
			q.waiters[i] = nil
		}
		q.waiters = q.waiters[:0]
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
