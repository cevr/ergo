package v1

import (
	"sync"

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
//   - The runner parks on wake, one channel of capacity 1 made with the
//     mailbox, and a Push that finds it parked sends one token without
//     waiting; a token left after another wake-up only makes the runner
//     look again. Queue makes a channel at each park and closes it.
//   - A Push that must wait makes one sender with its own wake channel;
//     that is the only allocation, and only under backpressure.
//   - The task's Done channel is read only before a wait.
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
	closed   bool
	// waiting is true while the runner is parked on wake.
	waiting bool
	wake    chan struct{}
	// taker is the runner's task once bound (bindTaker): its stop puts a
	// token in wake.
	taker *rt.Task
	// parked counts the operations parked: the runner and the senders.
	parked int
	// domainTaker is the runner while it waits in a clock's domain (rt
	// idle.go): notify unparks it before it wakes it (fua-b).
	domainTaker *rt.Task
}

// actorReference selects the reference runner for the actors started
// after it is set: a Queue inbox and a new reply channel per Ask and State
// read, as before MU18. Only tests set it (ERGO_ACTOR_REFERENCE=1, and
// the benchmarks' reference side), so the model-based tests run both.
var actorReference bool

// mailSender is a Push waiting for room.
type mailSender[T any] struct {
	v        T
	accepted bool
	rejected bool
	wake     chan struct{}
	// task is the Push's task when it waits in a clock's domain.
	task *rt.Task
}

// signal wakes s; in a clock's domain its task is unparked first (rt
// idle.go), so no window shows it parked while it can run. The mailbox's
// mu is held.
func (s *mailSender[T]) signal() {
	if s.task != nil {
		rt.Waking(s.task, s).Done()
	}
	s.wake <- struct{}{}
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
	m.capacity, m.wake = capacity, make(chan struct{}, 1)
}

// Push is Queue.Push.
func (m *mailbox[T]) Push(t *rt.Task, v T) error {
	if m.ref != nil {
		return m.ref.Push(t, v)
	}
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
	s := &mailSender[T]{v: v, wake: make(chan struct{}, 1)}
	m.senders = append(m.senders, s)
	m.notify() // at capacity 0 the runner takes v straight from s
	stop := t.Done()
	inDomain := t.InDomain()
	if inDomain {
		s.task = t
	}
	for !s.accepted && !s.rejected {
		m.parked++
		var p rt.Parked
		if inDomain {
			p = rt.Park(t, s, stop, nil)
		}
		m.mu.Unlock()
		woke := true
		select {
		case <-s.wake:
		case <-stop:
			woke = false
		}
		m.mu.Lock()
		p.Unpark()
		m.parked--
		if !woke && !s.accepted && !s.rejected {
			m.dropSender(s)
			m.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
	rejected := s.rejected
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
		m.waiting = true
		m.parked++
		// In a clock's domain the runner parks under m.mu, so notify, which
		// runs under m.mu too, unparks it before it wakes it; its stop
		// unparks it in rt's Interrupt (fua-b).
		var p rt.Parked
		if t.InDomain() {
			m.domainTaker = t
			p = rt.Park(t, m, t.Done(), nil)
		}
		m.mu.Unlock()
		woke := true
		if t == m.taker {
			// The taker's stop puts a token in wake (bindTaker), so one
			// plain receive waits for a value, a close and a stop.
			<-m.wake
			woke = !t.Stopped()
		} else {
			stop := t.Done()
			select {
			case <-m.wake:
			case <-stop:
				woke = false
			}
		}
		m.mu.Lock()
		p.Unpark()
		m.domainTaker = nil
		m.waiting = false
		m.parked--
		if !woke {
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

// Wake (rt.StopWaker) puts a wake token without waiting: the runner looks again. It
// takes no lock (it runs under the stopping task's lock).
func (m *mailbox[T]) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Poll is Queue.Poll.
func (m *mailbox[T]) Poll() (T, bool) {
	if m.ref != nil {
		return m.ref.Poll()
	}
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

// Close is Queue.Close.
func (m *mailbox[T]) Close() {
	if m.ref != nil {
		m.ref.Close()
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		for i, s := range m.senders {
			s.rejected = true
			s.signal()
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

// notify wakes the parked runner. m.mu is held.
func (m *mailbox[T]) notify() {
	if m.waiting {
		m.waiting = false
		if m.domainTaker != nil {
			rt.Waking(m.domainTaker, m).Done()
		}
		select {
		case m.wake <- struct{}{}:
		default: // a token is there already
		}
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
			s.accepted = true
			s.signal()
		}
		return true
	}
	if len(m.senders) > 0 {
		s := m.popSender()
		s.accepted = true
		s.signal()
		*dst = s.v
		return true
	}
	return false
}

// popSender removes the oldest waiting Push. m.mu is held.
func (m *mailbox[T]) popSender() *mailSender[T] {
	s := m.senders[0]
	m.senders[0] = nil
	m.senders = m.senders[1:]
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
