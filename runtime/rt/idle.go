package rt

import (
	"sort"
	"time"
)

// An auto clock is a Virtual whose time also moves on its own, when every
// task of its domain waits and none can run (testing.VirtualClock;
// docs/spec/stdlib.md, Testing). The domain is the task that made the
// clock, from that call on, and every task started below a member. The
// clock counts its live members and those parked in a wait it can see:
// a sleep on the clock, a queue wait, the join of a task and the race of
// timeout.After. When the two counts are equal and a sleeper is armed,
// the time moves to the earliest deadline, and every sleeper of that
// deadline wakes, in the order they slept.
//
// A wait on anything else (the host's input, a file, the host clock, a
// Manual clock, a lock) is not parked, so the clock never moves past it:
// a task that reads standard input keeps the clock still until the read
// ends.
//
// No window shows a runnable task as parked. A waiter checks its event and
// parks in one step under the clock's lock; a waker clears the parked
// flag of the task it wakes under the same lock, before the event becomes
// visible (the queue's signal, a task's end, a timer, a stop). A parked
// flag that a waker clears too eagerly only keeps the clock still, never
// moves it early; so every waker clears only the waits its event ends.
//
// A manual clock (NewManual, time.Manual) has a domain too, but its time
// moves only by AdvanceSettled: that waits until every member but its
// caller is parked, fires the earliest due deadline, and waits again, so
// it returns with every task it woke at its next wait (synctest.Wait's
// rule). The domain's seen waits are the same for both clocks, so a wait
// the clock cannot see keeps an AdvanceSettled waiting until it ends.
//
// Prior art: Go's testing/synctest moves a bubble's clock when every
// goroutine of the bubble is durably blocked, which the Go runtime knows
// and this package learns from its own waits; Tokio's time::pause with
// auto-advance; Trio's MockClock(autojump_threshold=0). Effect's TestClock
// moves only on adjust.

// member is a task's place in the domain of an auto clock. Its fields
// after v are guarded by v.mu.
type member struct {
	v      *Virtual
	parked bool
	stop   bool   // the parked wait ends on a stop of the task
	on     [2]any // the sources whose event ends the parked wait
}

// stopSource is the source of a stop's wake.
type stopSource struct{}

// NewAuto is an auto clock that reads start. t, the calling task, is its
// first member; a task that was a member of another clock's domain leaves
// it.
func NewAuto(t *Task, start time.Time) *Virtual {
	v := newDomain(t, start)
	v.auto = true
	return v
}

// NewManual is a manual clock with a domain (time.Manual; fua-b): it reads
// start and moves only by AdvanceSettled, which waits for the domain to
// settle, but never on its own. t, the calling task, is its first member,
// as for NewAuto.
func NewManual(t *Task, start time.Time) *Virtual {
	return newDomain(t, start)
}

// newDomain is a Virtual with a domain whose first member is t.
func newDomain(t *Task, start time.Time) *Virtual {
	v := NewVirtual(start)
	v.domain = true
	if old := t.member.Load(); old != nil {
		old.leave()
	}
	v.live = 1
	t.member.Store(&member{v: v})
	return v
}

// join makes t, a new child of parent, a member of parent's clock.
func (t *Task) join(parent *Task) {
	if m := parent.member.Load(); m != nil {
		v := m.v
		v.mu.Lock()
		v.live++
		v.mu.Unlock()
		t.member.Store(&member{v: v})
	}
}

// leave takes a finished member out of its clock's domain; the members
// left may all be parked now.
func (m *member) leave() {
	v := m.v
	v.mu.Lock()
	m.release()
	v.live--
	v.idle()
	v.mu.Unlock()
}

// release clears m's parked flag. v.mu is held.
func (m *member) release() {
	if m.parked {
		m.parked, m.stop, m.on = false, false, [2]any{}
		m.v.parked--
	}
}

// park parks t, a member, on the sources a and b (nil for none), and on
// its stop when stop is not nil, unless ready reports that the wait would
// end at once or the stop it waits on has come. It returns the member to
// unpark after the wait, or nil when t is no member.
func park(t *Task, a, b any, stop <-chan struct{}, ready func() bool) *member {
	m := t.member.Load()
	if m == nil {
		return nil
	}
	v := m.v
	v.mu.Lock()
	if !(stop != nil && t.stopped.Load()) && (ready == nil || !ready()) {
		m.parked, m.stop, m.on = true, stop != nil, [2]any{a, b}
		v.parked++
		v.idle()
	}
	v.mu.Unlock()
	return m
}

// unpark clears m's parked flag after its wait ended. It is nil-safe.
func (m *member) unpark() {
	if m == nil {
		return
	}
	m.v.mu.Lock()
	m.release()
	m.v.mu.Unlock()
}

// waking locks the clock of w, a waiter, and clears its parked flag when
// its wait ends on src. The caller makes the event visible, then unlocks.
// It returns nil when w is nil or no member. A nil src ends no wait: an
// empty slot of on is nil too.
func (w *Task) waking(src any) *member {
	if w == nil {
		return nil
	}
	m := w.member.Load()
	if m == nil {
		return nil
	}
	m.v.mu.Lock()
	if m.parked && waitEndsOn(m, src) {
		m.release()
	}
	return m
}

// waitEndsOn reports whether src's event ends m's parked wait.
func waitEndsOn(m *member, src any) bool {
	switch src {
	case nil:
		return false
	case stopSource{}:
		return m.stop
	default:
		return m.on[0] == src || m.on[1] == src
	}
}

// unlock ends a waking. It is nil-safe.
func (m *member) unlock() {
	if m != nil {
		m.v.mu.Unlock()
	}
}

// Wake makes the event of src visible to w by calling deliver, clearing
// w's parked flag first when w waits on src: deliver closes a channel w
// may be selecting on. Code outside rt whose event ends a parked wait (a
// queue's signal, a deadline's fire) wakes through it or through Waker.
func Wake(w *Task, src any, deliver func()) {
	m := w.waking(src)
	deliver()
	m.unlock()
}

// Waking is Wake split in two, for a caller that makes the event visible
// itself without a closure: the result's Done ends it.
func Waking(w *Task, src any) Waker { return Waker{w.waking(src)} }

// Waker is an open wake (Waking).
type Waker struct{ m *member }

// Done ends the wake.
func (k Waker) Done() { k.m.unlock() }

// Park parks t on src and on its stop, as park does; ready, called under
// the clock's lock, reports that the wait would end at once. The result's
// Unpark ends it. It does nothing for a task that is no member.
func Park(t *Task, src any, stop <-chan struct{}, ready func() bool) Parked {
	if t.member.Load() == nil {
		return Parked{}
	}
	return Parked{park(t, src, nil, stop, ready)}
}

// Parked is a parked wait (Park).
type Parked struct{ m *member }

// Unpark ends the parked wait.
func (p Parked) Unpark() { p.m.unpark() }

// InDomain reports whether t is a member of an auto clock, so a wait
// needs to park.
func (t *Task) InDomain() bool { return t.member.Load() != nil }

// WakeSource is a Timer whose fire is a parked wait's event: the parent
// that waits on its C is woken through Wake with the timer as src. A
// timer that is not one keeps a parked race unparked.
type WakeSource interface {
	Timer
	WakesItsWaiter()
}

// idle moves an auto clock when every live member is parked and a sleeper
// is armed: to the earliest deadline, waking every sleeper of it. v.mu is
// held.
func (v *Virtual) idle() {
	// A Settle looks again: a member parked or left.
	if v.change != nil {
		close(v.change)
		v.change = nil
	}
	if !v.auto || v.live == 0 || v.parked < v.live || len(v.timers) == 0 {
		return
	}
	when := v.timers[0].when
	if when.After(v.now) {
		v.now = when
	}
	v.steps++
	for len(v.timers) > 0 && v.timers[0].when.Equal(when) {
		v.fire(v.timers[0])
		v.timers = v.timers[1:]
	}
	v.armed.Broadcast()
}

// fire wakes the sleeper of t, if any, and delivers t's time. v.mu is held.
func (v *Virtual) fire(t *virtualTimer) {
	if t.task != nil {
		if m := t.task.member.Load(); m != nil && m.v == v && m.parked && m.on[0] == any(t) {
			m.release()
		}
	}
	t.c <- t.when
}

// Steps is the number of times an auto clock moved on its own.
func (v *Virtual) Steps() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.steps
}

// sleep is Sleep on an auto clock: the sleeper parks when it is a member
// of v, and an idle domain wakes it at its deadline. A stop ends the sleep
// and removes the timer.
func (v *Virtual) sleep(t *Task, d time.Duration) error {
	if d <= 0 {
		t.Done()
		return nil
	}
	v.mu.Lock()
	v.seq++
	tm := &virtualTimer{v: v, when: v.now.Add(d), seq: v.seq, c: make(chan time.Time, 1), task: t}
	i := sort.Search(len(v.timers), func(i int) bool { return tm.before(v.timers[i]) })
	v.timers = append(v.timers, nil)
	copy(v.timers[i+1:], v.timers[i:])
	v.timers[i] = tm
	v.armed.Broadcast()
	v.mu.Unlock()
	// Done runs WhenWaiting's hook, so it comes after the timer is armed
	// (Task.Done), and outside v.mu. A deadline that a move fired in
	// between is in tm.c, so the sleeper does not park.
	stop := t.Done()
	v.mu.Lock()
	m := t.member.Load()
	if m != nil && m.v == v {
		if !(stop != nil && t.stopped.Load()) && len(tm.c) == 0 {
			m.parked, m.stop, m.on = true, stop != nil, [2]any{tm, nil}
			v.parked++
		}
		v.idle()
	} else {
		m = nil
	}
	v.mu.Unlock()
	select {
	case <-tm.c:
		m.unpark()
		return nil
	case <-stop:
		m.unpark()
		// A deadline that came at the same instant as the stop has passed:
		// the sleep is done, so a tie in tasks.Race keeps its order.
		select {
		case <-tm.c:
			return nil
		default:
		}
		tm.Stop()
		return Interrupted
	}
}
