package rt

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Clock is the time-reading capability.
type Clock interface {
	Now() time.Time
}

// Timer is one armed deadline. C delivers once when it fires; Stop disarms it
// and reports whether it was still armed.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Scheduler is the capability that arms deadlines. Sleep and After use it, so
// a virtual scheduler drives both without real waiting.
type Scheduler interface {
	NewTimer(d time.Duration) Timer
}

// Live is the wall clock and the host's timers.
type Live struct{}

// Now returns the wall-clock time.
func (Live) Now() time.Time { return time.Now() }

// NewTimer arms a host timer.
func (Live) NewTimer(d time.Duration) Timer { return liveTimer{time.NewTimer(d)} }

type liveTimer struct{ t *time.Timer }

func (l liveTimer) C() <-chan time.Time { return l.t.C }
func (l liveTimer) Stop() bool          { return l.t.Stop() }

// Virtual is a deterministic clock and scheduler. Time moves only by Advance,
// which fires each due timer in deadline order, ties in arming order. Tests
// call AwaitTimers to know a task has armed its deadline before advancing.
type Virtual struct {
	mu     sync.Mutex
	armed  *sync.Cond
	now    time.Time
	seq    uint64
	timers []*virtualTimer // sorted by (when, seq)
	// A clock with a domain (idle.go: an auto clock or a manual one)
	// counts its live members and the parked ones; an auto clock also
	// moves when every live member is parked, and steps counts those
	// moves. change is closed when a member parks or leaves, so a Settle
	// looks again.
	domain, auto bool
	live, parked int
	steps        int
	change       chan struct{}
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
	when time.Time
	seq  uint64
	c    chan time.Time
	task *Task // the sleeper of an auto clock's Sleep, nil otherwise
}

// NewVirtual returns a virtual clock that reads start.
func NewVirtual(start time.Time) *Virtual {
	v := &Virtual{now: start}
	v.armed = sync.NewCond(&v.mu)
	return v
}

// Now returns the virtual time.
func (v *Virtual) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.now
}

// NewTimer arms a timer d after the virtual now. A deadline at or before now
// fires at once.
func (v *Virtual) NewTimer(d time.Duration) Timer {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	t := &virtualTimer{v: v, when: v.now.Add(d), seq: v.seq, c: make(chan time.Time, 1)}
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

func (t *virtualTimer) before(u *virtualTimer) bool {
	return t.when.Before(u.when) || t.when.Equal(u.when) && t.seq < u.seq
}

func (t *virtualTimer) C() <-chan time.Time { return t.c }

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
func (v *Virtual) Advance(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	target := v.now.Add(d)
	for len(v.timers) > 0 && !v.timers[0].when.After(target) {
		t := v.timers[0]
		v.timers = v.timers[1:]
		v.now = t.when
		v.fire(t)
	}
	v.now = target
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
func (v *Virtual) AdvanceSettled(t *Task, d time.Duration) error {
	if !v.domain {
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
	target := v.now.Add(d)
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
		if len(v.timers) == 0 || v.timers[0].when.After(target) {
			if target.After(v.now) {
				v.now = target
			}
			v.armed.Broadcast()
			v.mu.Unlock()
			return nil
		}
		when := v.timers[0].when
		v.now = when
		for len(v.timers) > 0 && v.timers[0].when.Equal(when) {
			tm := v.timers[0]
			v.timers = v.timers[1:]
			v.fire(tm)
		}
		v.armed.Broadcast()
		v.mu.Unlock()
	}
}

// Settle waits until every live member of v's domain but t is parked:
// each waits on a wait the clock sees (idle.go). A member that runs, or
// waits on anything else, keeps it waiting. With no domain it returns at
// once. A stop of t ends it with Interrupted.
func (v *Virtual) Settle(t *Task) error {
	if !v.domain {
		return nil
	}
	v.mu.Lock()
	for {
		self := 0
		if m := t.member.Load(); m != nil && m.v == v && !m.parked {
			self = 1
		}
		if v.parked >= v.live-self {
			v.mu.Unlock()
			return nil
		}
		if v.change == nil {
			v.change = make(chan struct{})
		}
		ch := v.change
		v.mu.Unlock()
		stop := t.Done()
		if Exploring {
			// The settle is a wait the clock does not see: under the
			// explored scheduler the caller gives the baton away until a
			// member parks or leaves, or it is stopped.
			Await(t, func() bool {
				select {
				case <-ch:
					return true
				case <-stop:
					return true
				default:
					return false
				}
			})
		}
		select {
		case <-ch:
		case <-stop:
			return Interrupted
		}
		v.mu.Lock()
	}
}

// Pending returns the number of armed timers.
func (v *Virtual) Pending() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.timers)
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
	if v, ok := s.(*Virtual); ok && v.domain {
		return v.sleep(t, d)
	}
	timer := s.NewTimer(d)
	select {
	case <-timer.C():
		return nil
	case <-t.Done():
		timer.Stop()
		return Interrupted
	}
}
