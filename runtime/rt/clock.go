package rt

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Instant is a time on a clock: nanoseconds since the Unix epoch, as
// time.Time.UnixNano gives it. Span is a duration in nanoseconds, as
// time.Duration. Neither has a method: the clock types, which the type
// graph of every Task reaches, name no type of package time, so no program
// keeps time.Time's String and Format, or time.Duration's String, for them
// (binsize BS3). The std time bodies convert at their boundary.
type (
	Instant int64
	Span    int64
)

// Clock is the time-reading capability.
type Clock interface {
	Now() Instant
}

// Timer is one armed deadline. C delivers once when it fires; Stop disarms it
// and reports whether it was still armed.
type Timer interface {
	C() <-chan Instant
	Stop() bool
}

// Scheduler is the capability that arms deadlines. Sleep and After use it, so
// a virtual scheduler drives both without real waiting.
type Scheduler interface {
	NewTimer(d Span) Timer
}

// Live is the wall clock and the host's timers.
type Live struct{}

// Now returns the wall-clock time.
func (Live) Now() Instant { return Instant(time.Now().UnixNano()) }

// NewTimer arms a host timer. time.AfterFunc's goroutine sends on its
// channel, so liveTimer holds no *time.Timer, whose C is a channel of
// time.Time (binsize BS3).
func (Live) NewTimer(d Span) Timer { return newLiveTimer(d) }

func newLiveTimer(d Span) liveTimer {
	c := make(chan Instant, 1)
	t := time.AfterFunc(time.Duration(d), func() { c <- Instant(time.Now().UnixNano()) })
	return liveTimer{c: c, stop: t.Stop}
}

type liveTimer struct {
	c    chan Instant
	stop func() bool
}

func (l liveTimer) C() <-chan Instant { return l.c }
func (l liveTimer) Stop() bool        { return l.stop() }

// Virtual is a deterministic clock and scheduler. Time moves only by Advance,
// which fires each due timer in deadline order, ties in arming order. Tests
// call AwaitTimers to know a task has armed its deadline before advancing.
type Virtual struct {
	// mu is the clock's lock; the clocks of one domain share their
	// domain's (idle.go), so a wait checks its event and parks in one step.
	mu    *sync.Mutex
	armed *sync.Cond
	now   Instant
	// at is now, written with it under mu (setNow) and read without
	// it, so a read of the clock takes no lock.
	at     atomic.Int64
	seq    uint64
	timers []*virtualTimer // sorted by (when, seq)
	// d is the domain of a clock that has one (idle.go: an auto clock or
	// a manual one), nil otherwise. An auto clock moves when every live
	// member of its domain is parked, and steps counts those moves.
	d     *domain
	auto  bool
	steps int
	// spare is a sleep's timer that its sleep no longer uses, for the
	// next sleep on v (FU-sleep-timer-reuse): a sleep allocates no timer.
	spare atomic.Pointer[virtualTimer]
	// advancing is set while an AdvanceSettled runs: one at a time.
	advancing bool
}

// AdvanceRunning is AdvanceSettled's result when another AdvanceSettled of
// the clock runs (batch-13 round 2, B1). The first waits until every other
// member is parked, and a caller of AdvanceSettled is not, so a second
// member's call can only come while the first runs; waiting for it would
// wait forever, and both moving the time would have no order.
var AdvanceRunning = errors.New("another task's Advance of this clock is running")

type virtualTimer struct {
	v    *Virtual
	when Instant
	seq  uint64
	c    chan Instant // nil for a sleep's timer
	// task is the sleeper of a Sleep on a clock with a domain, nil
	// otherwise. Its timer has no channel: fire sets fired and resumes
	// the sleeper's park word (FU-virtual-sleep).
	task  *Task
	fired atomic.Bool
	f     func() // an AfterFunc's function, which its fire starts; c is nil
	// settled marks an AfterSettledFunc's timer: it fires only in a step
	// of its instant in which no other timer is due (fireAt).
	settled bool
}

// NewVirtual returns a virtual clock that reads start.
func NewVirtual(start time.Time) *Virtual {
	v := &Virtual{now: Instant(start.UnixNano()), mu: new(sync.Mutex)}
	v.at.Store(start.UnixNano())
	v.armed = sync.NewCond(v.mu)
	return v
}

// Now returns the virtual time.
func (v *Virtual) Now() Instant { return Instant(v.at.Load()) }

// setNow moves the time to i; v.mu is held.
func (v *Virtual) setNow(i Instant) {
	v.now = i
	v.at.Store(int64(i))
}

// NewTimer arms a timer d after the virtual now. A deadline at or before now
// fires at once.
func (v *Virtual) NewTimer(d Span) Timer {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	t := &virtualTimer{v: v, when: v.now + Instant(d), seq: v.seq, c: make(chan Instant, 1)}
	if d <= 0 {
		t.c <- v.now
		return t
	}
	i := sort.Search(len(v.timers), func(i int) bool { return t.before(v.timers[i]) })
	v.timers = append(v.timers, nil)
	copy(v.timers[i+1:], v.timers[i:])
	v.timers[i] = t
	v.armed.Broadcast()
	return t
}

// AfterFunc arms f to run on its own goroutine once the virtual time is d
// past now, as time.AfterFunc does on the host's clock; a d of zero or
// less starts it at once. The result's Stop disarms it and reports
// whether it was still armed. Its deadline is armed like a Sleep's, so an
// auto clock moves to it when its domain is idle, and Pending counts it.
// f runs on a goroutine of its own, never under the clock's lock: it may
// arm and stop timers of the clock.
func (v *Virtual) AfterFunc(d Span, f func()) Timer {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	t := &virtualTimer{v: v, when: v.now + Instant(d), seq: v.seq, f: f}
	if d <= 0 {
		v.runAlarm(f)
		return t
	}
	i := sort.Search(len(v.timers), func(i int) bool { return t.before(v.timers[i]) })
	v.timers = append(v.timers, nil)
	copy(v.timers[i+1:], v.timers[i:])
	v.timers[i] = t
	v.armed.Broadcast()
	return t
}

// AfterSettledFunc is AfterFunc for a function that acts after every other
// event of its instant. On a clock with a domain (an auto clock, or a
// manual one that AdvanceSettled moves) it fires when the time has reached
// its deadline, every other timer due at that instant has fired, and the
// domain has settled: each task those timers woke, and each task those
// woke, has run to a wait the clock sees. It fires before the time moves
// on. So a task whose sleep ends at the same instant runs to its next wait
// (or its end) first, whichever was armed first. Several settled timers
// of one instant fire in one step, in arming order. One that f arms for
// the instant it fires at is no settled timer: a d of zero or less runs at
// once, as AfterFunc's does; a d above zero is a later instant. A plain
// timer that a woken task arms at a later instant fires there, after the
// settled ones of this instant. Without a domain (Advance) there is no
// settling: it fires in deadline order, as AfterFunc's does. It fires
// once; Stop disarms it as AfterFunc's.
func (v *Virtual) AfterSettledFunc(d Span, f func()) Timer {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	t := &virtualTimer{v: v, when: v.now + Instant(d), seq: v.seq, f: f, settled: true}
	if d <= 0 {
		v.runAlarm(f)
		return t
	}
	i := sort.Search(len(v.timers), func(i int) bool { return t.before(v.timers[i]) })
	v.timers = append(v.timers, nil)
	copy(v.timers[i+1:], v.timers[i:])
	v.timers[i] = t
	v.armed.Broadcast()
	return t
}

// fireAt is one step of the instant when, the earliest deadline armed: when
// a timer due at when is not settled, every such timer fires, in arming
// order, and the settled ones stay armed for a later step of when; when
// only settled timers are due, they all fire, in arming order. So a
// settled timer fires in a step of its own, after the domain settled from
// the step before. With no settled timer armed it is the plain step: the
// first look finds a plain timer. v.mu is held.
func (v *Virtual) fireAt(when Instant) {
	plain := false
	for i := 0; i < len(v.timers) && v.timers[i].when == when; i++ {
		if !v.timers[i].settled {
			plain = true
			break
		}
	}
	for i := 0; i < len(v.timers) && v.timers[i].when == when; {
		tm := v.timers[i]
		if plain && tm.settled {
			i++
			continue
		}
		v.removeAt(i)
		v.fire(tm)
	}
}

// removeAt removes the timer at i, keeping the order. v.mu is held.
func (v *Virtual) removeAt(i int) {
	n := copy(v.timers[i:], v.timers[i+1:]) + i
	v.timers[n] = nil
	v.timers = v.timers[:n]
}

// popFirst removes the earliest timer. It keeps the slice's room at its
// front, so a steady arm and fire allocates nothing (a reslice from the
// front would give the room up). v.mu is held.
func (v *Virtual) popFirst() {
	n := copy(v.timers, v.timers[1:])
	v.timers[n] = nil
	v.timers = v.timers[:n]
}

func (t *virtualTimer) before(u *virtualTimer) bool {
	return t.when < u.when || t.when == u.when && t.seq < u.seq
}

func (t *virtualTimer) C() <-chan Instant { return t.c }

func (t *virtualTimer) Stop() bool {
	v := t.v
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, u := range v.timers {
		if u == t {
			v.timers = append(v.timers[:i], v.timers[i+1:]...)
			v.armed.Broadcast()
			return true
		}
	}
	return false
}

// Advance moves the virtual time forward by d, firing every timer due by the
// new time in deadline order. While a timer fires, Now reads its deadline.
func (v *Virtual) Advance(d Span) {
	v.mu.Lock()
	defer v.mu.Unlock()
	target := v.now + Instant(d)
	for len(v.timers) > 0 && v.timers[0].when <= target {
		t := v.timers[0]
		v.popFirst()
		v.setNow(t.when)
		v.fire(t)
	}
	v.setNow(target)
	v.armed.Broadcast()
}

// AdvanceSettled is Advance for a clock with a domain (time.Manual,
// fua-b), called by t: it waits until the domain settles (Settle), then
// moves the time to the earliest deadline due by now+d, fires every timer
// of that instant, in arming order, and settles again, until no timer is
// due; then the time reads now+d. So when it returns, each timer due by
// then has fired at its own instant, and every task it woke, and every
// task those woke, has run to a wait the clock sees. On a clock with no
// domain it is Advance. It is an interruption point of t: a stop of t
// returns Interrupted, with the timers of the instants not reached armed.
// One runs at a time: a call while another runs returns AdvanceRunning at
// once and moves nothing.
func (v *Virtual) AdvanceSettled(t *Task, d Span) error {
	if v.d == nil {
		v.Advance(d)
		return nil
	}
	Point(t, Op{OpWrite, v})
	v.mu.Lock()
	if v.advancing {
		v.mu.Unlock()
		return AdvanceRunning
	}
	v.advancing = true
	target := v.now + Instant(d)
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.advancing = false
		v.mu.Unlock()
	}()
	for {
		if err := v.Settle(t); err != nil {
			return err
		}
		v.mu.Lock()
		if len(v.timers) == 0 || v.timers[0].when > target {
			if target > v.now {
				v.setNow(target)
			}
			v.armed.Broadcast()
			v.mu.Unlock()
			return nil
		}
		when := v.timers[0].when
		v.setNow(when)
		v.fireAt(when) // a settled timer waits for the next turn's Settle
		v.armed.Broadcast()
		v.mu.Unlock()
	}
}

// Settle waits until every live member of v's domain but t is parked:
// each waits on a wait the clock sees (idle.go). A member that runs, or
// waits on anything else, keeps it waiting. With no domain it returns at
// once. A stop of t ends it with Interrupted.
func (v *Virtual) Settle(t *Task) error {
	d := v.d
	if d == nil {
		return nil
	}
	v.mu.Lock()
	m := t.member.Load()
	if m != nil && m.d != d {
		m = nil
	}
	for {
		self := 0
		if m != nil && !m.parked {
			self = 1
		}
		if d.parked >= d.live-self && d.running == 0 {
			v.mu.Unlock()
			return nil
		}
		// The caller waits on its park word until a member parks or
		// leaves (idle resumes every settler) or it is stopped
		// (FU-settle-word): no channel and no select. A wake meant for
		// another wait costs one more look.
		g := d.settles.Load()
		d.settlers = append(d.settlers, t)
		v.mu.Unlock()
		t.Waits()
		stoppable := !t.Masked()
		if Exploring {
			// The settle is a wait the clock does not see: under the
			// explored scheduler the caller gives the baton away until a
			// member parks or leaves, or it is stopped.
			Await(t, func() bool { return d.settles.Load() != g || stoppable && t.stopped.Load() })
		}
		for d.settles.Load() == g && !(stoppable && t.stopped.Load()) {
			t.Suspend()
		}
		v.mu.Lock()
		if d.settles.Load() == g {
			// Stopped before a member parked or left: t is still a settler.
			d.dropSettler(t)
			v.mu.Unlock()
			return Interrupted
		}
	}
}

// dropSettler takes t out of d's settlers. d.mu is held.
func (d *domain) dropSettler(t *Task) {
	for i, w := range d.settlers {
		if w == t {
			last := len(d.settlers) - 1
			d.settlers[i] = d.settlers[last]
			d.settlers[last] = nil
			d.settlers = d.settlers[:last]
			return
		}
	}
}

// Pending returns the number of armed timers.
func (v *Virtual) Pending() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.timers)
}

// Asleep reports whether t, or a task started below it, sleeps on v: a
// timer of a domain clock's Sleep holds its sleeper (FK4-g2).
func (v *Virtual) Asleep(t *Task) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, tm := range v.timers {
		for s := tm.task; s != nil; s = s.parent.Load() {
			if s == t {
				return true
			}
		}
	}
	return false
}

// AwaitTimers blocks until exactly n timers are armed.
func (v *Virtual) AwaitTimers(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for len(v.timers) != n {
		v.armed.Wait()
	}
}

// Sleep waits d on s, or until t is stopped. It is an interruption point
// (I1): it returns Interrupted when t is stopped before or while it waits.
// Inside a masked region it waits the full duration.
func Sleep(t *Task, s Scheduler, d time.Duration) error {
	Point(t, Op{OpSleep, s})
	if t.Stopped() {
		return Interrupted
	}
	if v, ok := s.(*Virtual); ok && v.d != nil {
		return v.sleep(t, Span(d))
	}
	if _, ok := s.(Live); ok {
		return sleepLive(t, d)
	}
	timer := s.NewTimer(Span(d))
	select {
	case <-timer.C():
		return nil
	case <-t.Done():
		timer.Stop()
		return Interrupted
	}
}

// liveTimers holds the host timers of ended live sleeps, for the next.
var liveTimers sync.Pool

// sleepLive is Sleep on the host's timers. It reuses a host timer of an
// ended sleep: since Go 1.23 a timer that Stop or a receive ended keeps no
// stale tick, so Reset arms it as a new one (a stopped phase timer or
// deadline would otherwise cost a timer and its channel each time).
func sleepLive(t *Task, d time.Duration) error {
	timer, _ := liveTimers.Get().(*time.Timer)
	if timer == nil {
		timer = time.NewTimer(d)
	} else {
		timer.Reset(d)
	}
	defer liveTimers.Put(timer)
	select {
	case <-timer.C:
		return nil
	case <-t.Done():
		if !timer.Stop() {
			// Fired as the stop came: with asynctimerchan=1 (the timers
			// of Go before 1.23) the tick may still wait in the channel;
			// take it, so the next Reset starts clean.
			select {
			case <-timer.C:
			default:
			}
		}
		return Interrupted
	}
}
