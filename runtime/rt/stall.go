package rt

import (
	"sort"
	"sync"
	"sync/atomic"
	"weak"
)

// A stall of a domain d is a moment when every live member of d, and of
// every domain a member of d's tree made since (a task of d that made a
// clock of its own: a server on a clock of its own), waits on a wait its
// clock sees, and no clock of them can move: no auto clock has a sleeper
// and no Manual's Advance runs. Nothing those tasks hold can run again
// then, short of a task outside them or the host. A test that armed a
// named point (runtime/v1 hook.go, RT-HOOK) watches for it: a point armed
// and never reached would otherwise leave its law waiting for the release
// the point's function gives, until the test's deadline.
//
// Only WatchStalls turns the watch on; until then the one cost of a park
// is a load of stalls.on. The watch is a goroutine that calls its report
// at each park or leave of a member; the report asks Stalled, which looks
// at the domains at once, under each one's lock taken in the order they
// were made, so it reads one state of them all.

var stalls struct {
	on     atomic.Bool
	kick   chan struct{}
	flush  chan chan struct{}
	mu     sync.Mutex
	seq    uint64
	live   map[*Virtual]uint64 // the domains with a live member, by order made
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

// DomainOf is the clock domain t is a member of, or nil.
func DomainOf(t *Task) *Virtual {
	if m := t.member.Load(); m != nil {
		return m.v
	}
	return nil
}

// domainMade records v, a new domain with its first member, made by a
// task that was a member of up (nil for none).
func domainMade(v, up *Virtual) {
	if up != nil {
		v.up = weak.Make(up)
	}
	stalls.mu.Lock()
	if stalls.live == nil {
		stalls.live = map[*Virtual]uint64{}
	}
	stalls.seq++
	stalls.live[v] = stalls.seq
	stalls.mu.Unlock()
}

// domainEmpty forgets v, whose last member left: no member of it can wait.
func domainEmpty(v *Virtual) {
	stalls.mu.Lock()
	delete(stalls.live, v)
	stalls.mu.Unlock()
}

// kickStalls tells the watch that a member parked or left. v.mu is held.
func kickStalls() {
	if stalls.on.Load() {
		select {
		case stalls.kick <- struct{}{}:
		default:
		}
	}
}

// below reports whether v is d or a domain made below it.
func below(v, d *Virtual) bool {
	for v != nil {
		if v == d {
			return true
		}
		v = v.up.Value()
	}
	return false
}

// Stalled reports whether d, a domain with a live member, is stalled with
// the domains made below it.
func Stalled(d *Virtual) bool {
	stalls.mu.Lock()
	defer stalls.mu.Unlock()
	if _, ok := stalls.live[d]; !ok {
		return false
	}
	var vs []*Virtual
	for v := range stalls.live {
		if below(v, d) {
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool { return stalls.live[vs[i]] < stalls.live[vs[j]] })
	for _, v := range vs {
		v.mu.Lock()
	}
	all := true
	for _, v := range vs {
		if v.live > 0 && (v.parked < v.live || v.advancing || v.auto && len(v.timers) > 0) {
			all = false
		}
	}
	for _, v := range vs {
		v.mu.Unlock()
	}
	return all
}
