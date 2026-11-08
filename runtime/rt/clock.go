package rt

import (
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
	// An auto clock (idle.go) also moves when every live member of its
	// domain is parked; steps counts those moves.
	auto         bool
	live, parked int
	steps        int
}

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
	if t.Stopped() {
		return Interrupted
	}
	if v, ok := s.(*Virtual); ok && v.auto {
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
