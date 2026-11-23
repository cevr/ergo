package rt

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// An auto clock is a Virtual whose time also moves on its own, when every
// task of its domain waits and none can run (testing.VirtualClock;
// docs/spec/stdlib.md, Testing). The domain is the task that made the
// clock, from that call on, and every task started below a member; a
// clock that a member makes joins the domain (P3-9), so a task is in one
// domain, which every clock its members made shares. The domain counts
// its live members and those parked in a wait it can see:
// a sleep on a clock of the domain, a queue wait, the join of a task and
// the race of timeout.After. When the two counts are equal and a sleeper
// is armed, an auto clock moves to its earliest deadline (idle: the one
// nearest its own now), and every sleeper of that deadline wakes, in the
// order they slept.
//
// A wait on anything else (the host's input, a file, the host clock, a
// clock of another domain, a lock) is not parked, so the clock never
// moves past it:
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
// the clock cannot see keeps an AdvanceSettled waiting until it ends. One
// AdvanceSettled of a clock runs at a time (AdvanceRunning): its caller is
// not parked, so a second one would wait for the first, and the first for
// it.
//
// Prior art: Go's testing/synctest moves a bubble's clock when every
// goroutine of the bubble is durably blocked, which the Go runtime knows
// and this package learns from its own waits; Tokio's time::pause with
// auto-advance; Trio's MockClock(autojump_threshold=0). Effect's TestClock
// moves only on adjust.

// A domain is a set of tasks and the clocks its members made (P3-9): the
// first clock a task makes opens one, and a clock that a member makes
// joins its domain, so the task stays a member and every clock of the
// domain sees the same waits. Its clocks share mu. Before P3-9 a second
// clock took its maker out of the first one's domain: an auto clock made
// before a time.Manual that nothing used then had no member left and
// never moved.
type domain struct {
	mu *sync.Mutex
	// live and parked count the members and those parked in a wait the
	// domain sees. When a member parks or leaves, settles moves on and
	// each task of settlers, a Settle that waits, is resumed on its park
	// word, so it looks again (FU-settle-word).
	live, parked int
	settles      atomic.Uint64
	settlers     []*Task
	clocks       []*Virtual
	// running counts the AfterFunc functions of its clocks started and
	// not yet returned. The domain is not settled, and no auto clock
	// moves, while one runs: its function may wake a parked member (an
	// alarm's waiter, timeout.PopBefore's), which then runs before the
	// time moves on.
	running int
	// made is the domain's number in the order domains were made
	// (stall.go): a number, not a pointer, so the task's type graph
	// reaches no package weak (BS3).
	made uint64
}

// member is a task's place in a domain. Its fields after d are guarded
// by d.mu.
type member struct {
	d      *domain
	parked bool
	stop   bool   // the parked wait ends on a stop of the task
	on     [2]any // the sources whose event ends the parked wait
}

// stopSource is the source of a stop's wake.
type stopSource struct{}

// NewAuto is an auto clock that reads start, in t's domain: the one t is
// a member of, else a new one whose first member is t.
func NewAuto(t *Task, start time.Time) *Virtual {
	v := newDomain(t, start)
	v.auto = true
	return v
}

// NewManual is a manual clock with a domain (time.Manual; fua-b): it reads
// start and moves only by AdvanceSettled, which waits for the domain to
// settle, but never on its own. Its domain is t's, as for NewAuto.
func NewManual(t *Task, start time.Time) *Virtual {
	return newDomain(t, start)
}

// newDomain is a Virtual in t's domain, or in a new domain whose first
// member is t.
func newDomain(t *Task, start time.Time) *Virtual {
	v := NewVirtual(start)
	if m := t.member.Load(); m != nil {
		d := m.d
		v.mu, v.armed, v.d = d.mu, sync.NewCond(d.mu), d
		d.mu.Lock()
		d.clocks = append(d.clocks, v)
		d.mu.Unlock()
		return v
	}
	v.d = &domain{mu: v.mu, live: 1, clocks: []*Virtual{v}}
	t.member.Store(&member{d: v.d})
	domainMade(v.d) // stall.go
	return v
}

// join makes t, a new child of parent, a member of parent's domain.
func (t *Task) join(parent *Task) {
	if m := parent.member.Load(); m != nil {
		t.joinDomain(m.d)
	}
}

func (t *Task) joinDomain(d *domain) {
	d.mu.Lock()
	d.live++
	d.mu.Unlock()
	t.member.Store(&member{d: d})
}

// leave takes a finished member out of its domain; the members left may
// all be parked now.
func (m *member) leave() {
	d := m.d
	d.mu.Lock()
	m.release()
	d.live--
	empty := d.live == 0
	d.idle()
	d.mu.Unlock()
	if empty {
		domainEmpty(d) // stall.go; outside d.mu, which the watch takes after its own
	}
}

// release clears m's parked flag. d.mu is held.
func (m *member) release() {
	if m.parked {
		m.parked, m.stop, m.on = false, false, [2]any{}
		m.d.parked--
	}
}

// park parks t, a member, on the sources a and b (nil for none), and on
// its stop when stop is not nil, unless ready reports that the wait would
// end at once or the stop it waits on has come. It returns the member to
// unpark after the wait, or nil when t is no member.
func park(t *Task, a, b any, stop <-chan struct{}, ready func() bool) *member {
	return parkOn(t, a, b, stop != nil, ready)
}

// parkOn is park for a wait that ends on t's stop when stoppable, the
// form of a wait on t's park word, which has no stop channel.
func parkOn(t *Task, a, b any, stoppable bool, ready func() bool) *member {
	m := t.member.Load()
	if m == nil {
		return nil
	}
	d := m.d
	d.mu.Lock()
	if !(stoppable && t.stopped.Load()) && (ready == nil || !ready()) {
		m.parked, m.stop, m.on = true, stoppable, [2]any{a, b}
		d.parked++
		d.idle()
	}
	d.mu.Unlock()
	return m
}

// unpark clears m's parked flag after its wait ended. It is nil-safe.
func (m *member) unpark() {
	if m == nil {
		return
	}
	m.d.mu.Lock()
	m.release()
	m.d.mu.Unlock()
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
	return m.waking(src)
}

func (m *member) waking(src any) *member {
	m.d.mu.Lock()
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
		m.d.mu.Unlock()
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

// ParkEither is Park on two sources: the wait ends on an event of src or
// of other (a timer whose fire wakes through Wake with itself as src), or
// on t's stop. timeout.PopWithin parks on its queue and its deadline.
func ParkEither(t *Task, src, other any, stop <-chan struct{}, ready func() bool) Parked {
	if t.member.Load() == nil {
		return Parked{}
	}
	return Parked{park(t, src, other, stop, ready)}
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

// idle moves an auto clock of d when every live member is parked and a
// sleeper is armed: to its earliest deadline, waking every sleeper of it.
// With several auto clocks armed, the one whose earliest deadline is the
// nearest to its own now moves, the first made on a tie; the woken tasks
// run to their next wait before any clock moves again. d.mu is held.
func (d *domain) idle() {
	// A Settle looks again: a member parked or left.
	if len(d.settlers) > 0 {
		d.settles.Add(1)
		for i, w := range d.settlers {
			w.Resume()
			d.settlers[i] = nil
		}
		d.settlers = d.settlers[:0]
	}
	kickStalls() // stall.go
	if d.live == 0 || d.parked < d.live || d.running > 0 {
		return
	}
	var next *Virtual
	for _, c := range d.clocks {
		if c.auto && len(c.timers) > 0 && (next == nil || c.timers[0].when-c.now < next.timers[0].when-next.now) {
			next = c
		}
	}
	if next != nil {
		next.move()
	}
}

// move moves v, an auto clock with a timer armed, to its earliest
// deadline and fires every timer of it. v.mu is held.
func (v *Virtual) move() {
	when := v.timers[0].when
	if when > v.now {
		v.setNow(when)
	}
	v.steps++
	schedTick(v)
	for len(v.timers) > 0 && v.timers[0].when == when {
		v.fire(v.timers[0])
		v.popFirst()
	}
	v.armed.Broadcast()
}

// runAlarm starts f, an AfterFunc's function, on a goroutine of its own,
// never under v.mu. Its domain's running counts it until it returns, so a
// Settle waits for it and an auto clock does not move under it: a member
// that f wakes runs before the time moves on. v.mu is held.
func (v *Virtual) runAlarm(f func()) {
	d := v.d
	if d != nil {
		d.running++
	}
	go func() {
		defer func() {
			if d != nil {
				d.mu.Lock()
				d.running--
				d.idle()
				d.mu.Unlock()
			}
		}()
		f()
	}()
}

// fire wakes the sleeper of t, if any, and delivers t's time. v.mu is held.
func (v *Virtual) fire(t *virtualTimer) {
	if t.f != nil {
		v.runAlarm(t.f)
		return
	}
	if task := t.task; task != nil {
		if m := task.member.Load(); m != nil && m.d == v.d && m.parked && m.on[0] == any(t) {
			m.release()
		}
		// The sleeper may return once fired is set and hand t to the next
		// sleep (v.spare), so task was read before.
		t.fired.Store(true)
		if fireGap != nil {
			fireGap()
		}
		task.wake.unpark()
		return
	}
	t.c <- t.when
}

// Steps is the number of times an auto clock moved on its own.
func (v *Virtual) Steps() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.steps
}

// sleepGap, when set, runs in sleep between the clock's unlock and the
// wait: the window in which the sleeper's own deadline may have fired
// (it parked last and moved the clock) and a stop may come before it
// looks. Only rt's tests set it (TestVirtualSleepTieEndsAsSleep); on
// another host or under load a preemption opens the same window.
var sleepGap func(*Task)

// fireGap, when set, runs in fire between the store of fired and the
// sleeper's wake: the window in which the sleeper, already awake, may see
// fired, return and hand its timer on, clearing its task. Only rt's tests
// set it (TestFireOutlivesItsSleep); a preemption opens the same window,
// which the explored scheduler cannot: fire runs inside one step.
var fireGap func()

// sleep is Sleep on an auto clock: the sleeper parks when it is a member
// of v, and an idle domain wakes it at its deadline. A stop ends the sleep
// and removes the timer.
func (v *Virtual) sleep(t *Task, d Span) error {
	if d <= 0 {
		t.Done()
		return nil
	}
	v.mu.Lock()
	v.seq++
	// The timer has no channel: fire sets fired and resumes t's park word
	// (FU-virtual-sleep), and t waits on its word as a join does.
	tm := v.takeSpare()
	tm.v, tm.when, tm.seq, tm.task = v, v.now+Instant(d), v.seq, t
	tm.fired.Store(false)
	i := sort.Search(len(v.timers), func(i int) bool { return tm.before(v.timers[i]) })
	v.timers = append(v.timers, nil)
	copy(v.timers[i+1:], v.timers[i:])
	v.timers[i] = tm
	v.armed.Broadcast()
	// Waits flushes held output and runs WhenWaiting's hook, so it comes
	// after the timer is armed (Task.Done), and outside v.mu; with neither
	// to do the clock stays locked. A deadline that a move fired in
	// between has set fired, so the sleeper does not park. Inside a mask
	// the sleep ignores a stop, as Done's nil channel did.
	if waitFlush.pending.Load() || waitHooks.n.Load() != 0 {
		v.mu.Unlock()
		t.Waits()
		v.mu.Lock()
	}
	stoppable := !t.Masked()
	m := t.member.Load()
	if m != nil && m.d == v.d {
		if !(stoppable && t.stopped.Load()) && !tm.fired.Load() {
			m.parked, m.stop, m.on = true, stoppable, [2]any{tm, nil}
			v.d.parked++
		}
		v.d.idle()
	} else {
		m = nil
	}
	v.mu.Unlock()
	if sleepGap != nil {
		sleepGap(t)
	}
	Block(t)
	// Interrupt resumes the word after it sets the stop flag, so the loop
	// sees either event; a wake that was for an earlier wait costs one
	// more look.
	for !tm.fired.Load() && !(stoppable && t.stopped.Load()) {
		t.wake.park()
	}
	Unblock(t)
	// A fired deadline ends the sleep, even one that came at the same
	// instant as the stop, so a tie in tasks.Race keeps its order. fire
	// cleared the parked flag before it set fired, so there is nothing to
	// unpark.
	if tm.fired.Load() {
		v.putSpare(tm)
		return nil
	}
	m.unpark()
	tm.Stop()
	// A fire that came before the Stop ran under v.mu and is over.
	v.putSpare(tm)
	return Interrupted
}

// takeSpare is v's spare sleep timer, or a new one. Under the explored
// scheduler every sleep has a timer of its own, which names its pending
// wait.
func (v *Virtual) takeSpare() *virtualTimer {
	if !Exploring {
		if tm := v.spare.Swap(nil); tm != nil {
			return tm
		}
	}
	return new(virtualTimer)
}

// putSpare keeps tm, a sleep's timer that no list and no member holds any
// more, for v's next sleep.
func (v *Virtual) putSpare(tm *virtualTimer) {
	if !Exploring {
		tm.task = nil
		v.spare.Store(tm)
	}
}
