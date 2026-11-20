package rt

import "sync/atomic"

// The park word (PARK1; rt-spike levers.md §2.2 S2 and S6). Every wait of
// a task on one value (the join of a child, an actor's answer) is one
// waiter, one or more wakers and a stop that must also wake it. The waiter
// parks on its own task's word (Task.Suspend); a waker leaves a wake
// (Task.Resume), and Interrupt resumes the task after it set the stop
// flag. The waiter rechecks its condition and Stopped after every return,
// so a wake that is not for it costs one more look, never a wrong answer.
//
// The word has three states: empty, notified (a wake is pending) and
// parked (the waiter is asleep). A Resume before the park leaves the word
// notified, and the park takes it at once: no wake is lost. Wakes do not
// add up: two Resumes before a park leave one, so a park returns once per
// pending wake (Tokio's and Rust's thread::park parker, the netpoller's
// pollDesc).
//
// Two bodies have that behaviour. The word body (park_word.go) parks the
// goroutine with the Go runtime's own gopark and wakes it with goready,
// reached by the linkname the Go runtime keeps open for them (go.dev/issue/67401);
// it needs no compiler or runtime patch. The stock body (stockParker,
// below) is the same three states with a channel to sleep on: it is the
// reference body, the body of a race build and of a build with the tag
// ergo_stockpark, and what a Go release that closes the linkname runs.

// Park states. The word body keeps the parked goroutine's g in place of
// parkParked.
const (
	parkEmpty    = 0
	parkNotified = 1
	parkParked   = 2
)

// stockParker is the stock-Go park word: one word that points to its
// state and the channel the waiter sleeps on, made on first use, so a
// task keeps the word body's size.
type stockParker struct{ p atomic.Pointer[stockPark] }

type stockPark struct {
	state atomic.Uint32
	c     chan struct{} // one slot: the wake of a parked waiter
}

// of returns the parker's state, made now if no park or wake made it.
func (s *stockParker) of() *stockPark {
	if p := s.p.Load(); p != nil {
		return p
	}
	p := &stockPark{c: make(chan struct{}, 1)}
	if s.p.CompareAndSwap(nil, p) {
		return p
	}
	return s.p.Load()
}

// park returns when a wake is pending, taking it, or sleeps until one
// comes. Only one goroutine parks on a parker.
func (s *stockParker) park() {
	p := s.of()
	if p.state.CompareAndSwap(parkNotified, parkEmpty) {
		return
	}
	if p.state.CompareAndSwap(parkEmpty, parkParked) {
		<-p.c
		return
	}
	// A wake came between the two: take it. A parker another goroutine
	// sleeps on is a misuse, which fails loudly.
	if !p.state.CompareAndSwap(parkNotified, parkEmpty) {
		panic(parkTwice)
	}
}

// parkTwice is the failure of a second goroutine that parks on a parker
// one already sleeps on: two goroutines in waits of one task on its own
// word (Task.Suspend). Dropping either would leave it asleep for good, so
// the misuse fails at once; a wait that two goroutines of a task may
// reach parks on a Waiter of its own (waiter.go).
const parkTwice = "rt: two goroutines park on one task's word at once; a wait two goroutines of a task may share parks on an rt.Waiter"

// unpark wakes the parked waiter, or leaves a wake for its next park.
// Any goroutine may call it.
func (s *stockParker) unpark() {
	p := s.of()
	for {
		switch p.state.Load() {
		case parkNotified:
			return
		case parkEmpty:
			if p.state.CompareAndSwap(parkEmpty, parkNotified) {
				return
			}
		default:
			if p.state.CompareAndSwap(parkParked, parkEmpty) {
				p.c <- struct{}{} // the slot is free: the last wake was received before this park
				return
			}
		}
	}
}

// parkedNow reports whether the waiter sleeps on s (the laws' view).
func (s *stockParker) parkedNow() bool { return s.of().state.Load() == parkParked }

// pending reports whether a wake is pending (the laws' view).
func (s *stockParker) pending() bool { return s.of().state.Load() == parkNotified }

// Suspend parks t's goroutine until a wake for t is pending, and takes
// it: a Resume since the last Suspend returned, or the stop request of
// Interrupt. It may return for a wake meant for another wait of t, so the
// caller loops on its own condition and on Stopped. Call it from t's own
// goroutine, the only one that may park on t: a second goroutine that
// parks while one sleeps on t's word panics (parkTwice), and a wait two
// goroutines of a task may reach parks on a Waiter instead.
func (t *Task) Suspend() { t.wake.park() }

// Resume wakes t's goroutine from its Suspend, or leaves the wake for its
// next one. Call it after the event the waiter checks is visible (an
// atomic store), from any goroutine.
func (t *Task) Resume() { t.wake.unpark() }

// Waits marks the point where t reaches a wait of the runtime, after the
// wait registered what it waits for: held output is flushed and
// WhenPolled's and WhenWaiting's hooks run. Done calls it; a wait on
// Suspend calls it before its first park. Call it from t's own goroutine.
func (t *Task) Waits() {
	if waitFlush.pending.Load() {
		waitFlush.flush()
	}
	if t.stopped.v.Load()&flagPolled != 0 {
		t.firePollHook()
	}
	if waitHooks.n.Load() != 0 {
		t.fireWaitHook()
	}
}
