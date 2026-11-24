package v1

import (
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// mailbox is an actor's inbox (MU18): Queue's contract (queue.go) for one
// taker, the runner, at a hand channel's cost. Every operation keeps
// Queue's rules: FIFO, capacity and backpressure with waiting Pushes served
// in the order they began, a rendezvous at capacity 0, Close that rejects
// the waiting Pushes and keeps the queued values for Pop and Poll, and
// interruption points that raise rt.Interrupted with nothing taken or
// entered. What differs is only how it stores and wakes:
//
//   - The values sit in a ring that grows by doubling up to the capacity,
//     so a Push allocates nothing once the ring has grown, and an idle
//     actor holds no buffer (Queue's slice moves forward and reallocates).
//   - The runner parks on its task's park word (rt park.go; PARK3), and a
//     Push that finds it parked resumes it; a stop resumes it too (rt's
//     Interrupt). A wake-up meant for another wait of the task only makes
//     the runner look again; only an inbox notification or a stop can
//     end the wait the clock sees.
//   - A Push that must wait takes a sender and parks on its own task's
//     word. The mailbox keeps one spare sender and the senders' slice
//     keeps its room, so a steady rendezvous (one Push waiting at a time)
//     allocates nothing; each further Push waiting at once makes one.
//   - No wait selects on the task's Done channel.
//
// ref, when set, is the reference: every operation is the Queue's
// (SetActorReferenceForTest), so the model-based tests run both.
type mailbox[T any] struct {
	ref *Queue[T]

	mu       sync.Mutex
	capacity int
	ring     []T // len is a power of two or 0; it grows up to the capacity
	head, n  int
	senders  []*mailSender[T] // the Pushes waiting for room, oldest first
	spare    *mailSender[T]   // a sender no Push holds, for the next wait
	closed   bool
	// waiter names the parked Pop until notify makes its wake visible.
	// The taker reads it without mu after parking on its task's word.
	waiter atomic.Pointer[rt.Task]
	// taker is the runner's task once bound (bindTaker): its stop resumes
	// it (Wake), besides rt's own resume at the stop.
	taker *rt.Task
	// parked counts the operations parked: the runner and the senders.
	parked int
}

// actorReference selects the reference runner for the actors started
// after it is set: a Queue inbox and a new reply channel per Ask and State
// read, as before MU18. Only tests set it (ERGO_ACTOR_REFERENCE=1, and
// the benchmarks' reference side), so the model-based tests run both.
var actorReference bool

// mailSender is a Push waiting for room.
type mailSender[T any] struct {
	v      T
	result atomic.Uint32 // the sender reads it without the mailbox's mu
	task   *rt.Task      // the Push's task, parked on its word
	// domain says the Push waits in a clock's domain.
	domain bool
}

const (
	mailPending uint32 = iota
	mailAccepted
	mailRejected
)

// signal makes the acceptance or rejection visible after clearing the
// sender's parked flag in its clock's domain. The mailbox's mu is held.
func (s *mailSender[T]) signal(result uint32) {
	var wake rt.Waker
	if s.domain {
		wake = rt.Waking(s.task, s)
	}
	s.result.Store(result)
	s.task.Resume()
	wake.Done()
}

// newMailbox is the inbox of an actor with the given capacity (not
// negative: machineStart checked it).
func newMailbox[T any](capacity int, reference bool) *mailbox[T] {
	m := &mailbox[T]{}
	m.init(capacity, reference)
	return m
}

// init makes m, a zero mailbox, the empty open inbox of the capacity (the
// Actor holds its mailbox by value).
func (m *mailbox[T]) init(capacity int, reference bool) {
	if reference {
		m.ref = New[T](capacity)
		return
	}
	m.capacity = capacity
}

// Push is Queue.Push.
func (m *mailbox[T]) Push(t *rt.Task, v T) error {
	if m.ref != nil {
		return m.ref.Push(t, v)
	}
	rt.Point(t, rt.Op{Kind: rt.OpPush, On: m})
	t.Check()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Closed{}
	}
	if len(m.senders) == 0 && m.n < m.capacity {
		m.put(v)
		m.notify()
		m.mu.Unlock()
		return nil
	}
	return m.pushWait(t, v)
}

// pushWait waits for room for v, with m.mu held, and releases it.
func (m *mailbox[T]) pushWait(t *rt.Task, v T) error {
	s := m.spare
	if s != nil {
		m.spare = nil
		s.v, s.task = v, t
	} else {
		s = &mailSender[T]{v: v, task: t}
	}
	m.senders = append(m.senders, s)
	m.notify() // at capacity 0 the runner takes v straight from s
	stop := t.Done()
	s.domain = t.InDomain()
	for s.result.Load() == mailPending {
		if t.Stopped() {
			m.dropSender(s)
			m.mu.Unlock()
			panic(rt.Interrupted)
		}
		m.parked++
		var p rt.Parked
		if s.domain {
			p = rt.Park(t, s, stop, nil)
		}
		m.mu.Unlock()
		rt.Block(t)
		for s.result.Load() == mailPending && !t.Stopped() {
			t.Suspend()
		}
		rt.Unblock(t)
		m.mu.Lock()
		p.Unpark()
		m.parked--
	}
	rejected := s.result.Load() == mailRejected
	// Nobody holds s now: the runner or Close took it out of senders and
	// signaled it under m.mu. It waits, emptied, for the next Push that
	// must wait.
	*s = mailSender[T]{}
	m.spare = s
	m.mu.Unlock()
	if rejected {
		return Closed{}
	}
	return nil
}

// popInto is Queue.Pop for the runner, the one taker, into *dst: the
// value is copied once, from the ring to dst. On a failure, and before a
// wait, *dst is the zero value, so it keeps no reference. A stopped take
// returns rt.Interrupted as its error, where Pop raises it: the runner
// ends with that exit without a panic, whose unwinding (and the stack it
// grows to) was most of a stop's cost (MU18 profile).
func (m *mailbox[T]) popInto(t *rt.Task, dst *T) error {
	if m.ref != nil {
		return m.refPopInto(t, dst)
	}
	rt.Point(t, rt.Op{Kind: rt.OpPop, On: m})
	if t.Stopped() {
		*dst = *new(T)
		return rt.Interrupted
	}
	m.mu.Lock()
	for {
		if m.frontInto(dst) {
			m.mu.Unlock()
			return nil
		}
		*dst = *new(T)
		if m.closed {
			m.mu.Unlock()
			return Closed{}
		}
		m.waiter.Store(t)
		m.parked++
		// In a clock's domain the runner parks under m.mu, so notify, which
		// runs under m.mu too, unparks it before it wakes it; its stop
		// unparks it in rt's Interrupt (fua-b).
		var p rt.Parked
		if t.InDomain() {
			p = rt.Park(t, m, t.Done(), nil)
		} else if t != m.taker {
			t.Waits() // as Done did; the taker's wait never read Done
		}
		m.mu.Unlock()
		// A value, a close and a stop each resume t's word (notify, rt's
		// Interrupt); another wake-up of t only makes it look again.
		rt.Block(t)
		for m.waiter.Load() != nil && !t.Stopped() {
			t.Suspend()
		}
		rt.Unblock(t)
		m.mu.Lock()
		p.Unpark()
		m.waiter.Store(nil)
		m.parked--
		if t.Stopped() {
			m.mu.Unlock()
			return rt.Interrupted
		}
	}
}

// refPopInto is popInto on the reference Queue. It is a call of its own,
// so its value's copy is not in popInto's frame, which stays on the
// runner's stack while it waits (the stack of an idle actor, MU18).
//
//go:noinline
func (m *mailbox[T]) refPopInto(t *rt.Task, dst *T) error {
	v, err := m.ref.Pop(t)
	*dst = v
	return err
}

// bindTaker makes t, the runner's task, the taker whose stop wakes a
// waiting popInto (rt's OnInterrupt), so its wait needs no select.
func (m *mailbox[T]) bindTaker(t *rt.Task) {
	if m.ref != nil {
		return
	}
	m.taker = t
	t.OnInterrupt(m)
}

// unbindTaker removes the taker's wake before its task ends.
func (m *mailbox[T]) unbindTaker() {
	if m.taker != nil {
		m.taker.OnInterrupt(nil)
	}
}

// Wake (rt.StopWaker) resumes the taker without waiting: the runner looks
// again. It takes no lock (it runs under the stopping task's lock).
func (m *mailbox[T]) Wake() { m.taker.Resume() }

// Poll is Queue.Poll.
func (m *mailbox[T]) Poll() (T, bool) {
	if m.ref != nil {
		return m.ref.Poll()
	}
	rt.PointRunning(rt.Op{Kind: rt.OpPop, On: m})
	var v T
	m.mu.Lock()
	ok := m.frontInto(&v)
	m.mu.Unlock()
	return v, ok
}

// Len is Queue.Len.
func (m *mailbox[T]) Len() int {
	if m.ref != nil {
		return m.ref.Len()
	}
	m.mu.Lock()
	n := m.n
	m.mu.Unlock()
	return n
}

// Close is Queue.End (the actor ends its mailbox).
func (m *mailbox[T]) Close() {
	if m.ref != nil {
		m.ref.End()
		return
	}
	rt.PointRunning(rt.Op{Kind: rt.OpClose, On: m})
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		for i, s := range m.senders {
			s.signal(mailRejected)
			m.senders[i] = nil
		}
		m.senders = nil
		m.notify()
	}
	m.mu.Unlock()
}

// waiters is how many operations are parked (the tests' inboxParked).
func (m *mailbox[T]) waiters() int {
	if m.ref != nil {
		m.ref.mu.Lock()
		defer m.ref.mu.Unlock()
		return m.ref.parked
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.parked
}

// isClosed reports whether admission has closed.
func (m *mailbox[T]) isClosed() bool {
	if m.ref != nil {
		m.ref.mu.Lock()
		defer m.ref.mu.Unlock()
		return m.ref.closed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// notify clears the domain's parked flag before the wake is visible to
// the runner, which may already be looking again. m.mu is held.
func (m *mailbox[T]) notify() {
	if waiter := m.waiter.Load(); waiter != nil {
		wake := rt.Waking(waiter, m)
		m.waiter.Store(nil)
		waiter.Resume()
		wake.Done()
	}
}

// put adds v at the back; there is room (m.n < m.capacity). m.mu is held.
func (m *mailbox[T]) put(v T) {
	if m.n == len(m.ring) {
		m.grow()
	}
	m.ring[(m.head+m.n)&(len(m.ring)-1)] = v
	m.n++
}

// grow doubles the ring (from 1), keeping the order; the length stays a
// power of two, so an index is a mask. It may pass the capacity by less
// than a factor of two; m.n never does.
func (m *mailbox[T]) grow() {
	size := 2 * len(m.ring)
	if size == 0 {
		size = 1
	}
	ring := make([]T, size)
	for i := range m.n {
		ring[i] = m.ring[(m.head+i)&(len(m.ring)-1)]
	}
	m.ring, m.head = ring, 0
}

// frontInto moves the front value to *dst without waiting, as
// Queue.front: the oldest queued value, whose room then goes to the oldest
// waiting Push, or, at capacity 0, the oldest waiting Push's value. m.mu
// is held.
func (m *mailbox[T]) frontInto(dst *T) bool {
	if m.n > 0 {
		i := m.head
		*dst = m.ring[i]
		var zero T
		m.ring[i] = zero // the slot keeps no reference
		m.head = (i + 1) & (len(m.ring) - 1)
		m.n--
		if len(m.senders) > 0 {
			s := m.popSender()
			m.put(s.v)
			s.signal(mailAccepted)
		}
		return true
	}
	if len(m.senders) > 0 {
		s := m.popSender()
		s.signal(mailAccepted)
		*dst = s.v
		return true
	}
	return false
}

// popSender removes the oldest waiting Push. m.mu is held.
func (m *mailbox[T]) popSender() *mailSender[T] {
	s := m.senders[0]
	m.senders[0] = nil
	if len(m.senders) == 1 {
		// The last waiter: keep the slice's room, so a steady rendezvous
		// does not grow it again (PARK3).
		m.senders = m.senders[:0]
	} else {
		m.senders = m.senders[1:]
	}
	return s
}

// dropSender removes s, a Push that stopped waiting. m.mu is held.
func (m *mailbox[T]) dropSender(s *mailSender[T]) {
	for i, w := range m.senders {
		if w == s {
			copy(m.senders[i:], m.senders[i+1:])
			m.senders[len(m.senders)-1] = nil
			m.senders = m.senders[:len(m.senders)-1]
			return
		}
	}
}
