package rt

import (
	"sync"
	"sync/atomic"
)

// A stall of a domain d is a moment when every live member of d waits on
// a wait its clocks see (a clock that a member makes joins d, P3-9, so a
// server on a clock of its own is a member of d too), and no clock of d
// can move: no auto clock has a sleeper, no Manual's Advance runs and no
// AfterFunc's function runs. Nothing those tasks hold can run again
// then, short of a task outside them or the host. A test that armed a
// named point (runtime/v1 hook.go, RT-HOOK) watches for it: a point armed
// and never reached would otherwise leave its law waiting for the release
// the point's function gives, until the test's deadline.
//
// Only WatchStalls turns the watch on; until then the one cost of a park
// is a load of stalls.on. The watch is a goroutine that calls its report
// at each park or leave of a member; the report asks Stalled, which reads
// the domain under its lock, so it reads one state of it.

var stalls struct {
	on     atomic.Bool
	kick   chan struct{}
	flush  chan chan struct{}
	mu     sync.Mutex
	seq    uint64
	live   map[*domain]uint64 // the domains with a live member, by order made
	report func()
	once   sync.Once
}

// WatchStalls calls report, from a goroutine of its own, after each park
// or leave of a member of any domain. Only the first call's report is
// kept; a later call does nothing.
func WatchStalls(report func()) {
	stalls.once.Do(func() {
		stalls.report = report
		stalls.kick = make(chan struct{}, 1)
		stalls.flush = make(chan chan struct{})
		stalls.on.Store(true)
		go func() {
			for {
				select {
				case <-stalls.kick:
					report()
				case done := <-stalls.flush:
					report()
					close(done)
				}
			}
		}()
	})
}

// FlushStalls runs the watch's report once more, on the watch's
// goroutine, and returns when it has: so a test that saw a domain settle
// (Settle) knows the report saw it too. It returns false, at once, when
// no watch runs.
func FlushStalls() bool {
	if !stalls.on.Load() {
		return false
	}
	done := make(chan struct{})
	stalls.flush <- done
	<-done
	return true
}

// DomainOf is the first clock of the domain t is a member of, which names
// the domain to Stalled, or nil.
func DomainOf(t *Task) *Virtual {
	m := t.member.Load()
	if m == nil {
		return nil
	}
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clocks[0]
}

// domainMade records d, a new domain with its first member.
func domainMade(d *domain) {
	stalls.mu.Lock()
	if stalls.live == nil {
		stalls.live = map[*domain]uint64{}
	}
	stalls.seq++
	d.made = stalls.seq
	stalls.live[d] = d.made
	stalls.mu.Unlock()
}

// domainEmpty forgets d, whose last member left: no member of it can wait.
func domainEmpty(d *domain) {
	stalls.mu.Lock()
	delete(stalls.live, d)
	stalls.mu.Unlock()
}

// kickStalls tells the watch that a member parked or left. d.mu is held.
func kickStalls() {
	if stalls.on.Load() {
		select {
		case stalls.kick <- struct{}{}:
		default:
		}
	}
}

// Stalled reports whether the domain of v, a clock with a live member, is
// stalled.
func Stalled(v *Virtual) bool {
	d := v.d
	if d == nil {
		return false
	}
	stalls.mu.Lock()
	defer stalls.mu.Unlock()
	if _, ok := stalls.live[d]; !ok {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live == 0 || d.running > 0 || d.parked < d.live {
		return false
	}
	for _, c := range d.clocks {
		if c.advancing || c.auto && len(c.timers) > 0 {
			return false
		}
	}
	return true
}
