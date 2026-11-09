package v1

import (
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestManualAdvanceSettles holds fua-b's rule (runtime.md, Machines and
// actors 11.16): time.Manual's Advance(d) returns once every timer due by
// now+d has fired, at its own instant, and every task it woke has run to
// its next wait. Generated machines with state timers run as actors on a
// manual clock, beside a task that ticks on the same clock; the trace is
// Sends and Advances by the test's task, the clock's first member. After
// each Advance, with no wait of its own, the test reads the state the
// actor shows and the ticks, and holds them against a reference timed
// fold. Mutant: Advance returns before the domain settles.
func TestManualAdvanceSettles(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		if msg := settleRun(seed); msg != "" {
			t.Fatalf("seed %d: %s", seed, msg)
		}
	}
}

// settleMachine is a generated machine: the timer of each state (0 is
// none) and what its fire decides.
type settleMachine struct {
	n      int
	timer  []int
	kind   []int // decideEnter, decideEmit or decideStay
	target []int
}

// settleRef is the reference timed fold: the state, the instant its entry
// opened, and that entry's due timer.
type settleRef struct {
	m     settleMachine
	s     tstate
	now   int
	armed bool
	due   int
}

func (r *settleRef) open() {
	r.armed = r.m.timer[r.s.v] > 0
	r.due = r.now + r.m.timer[r.s.v]
}

// advance moves to target, firing each due timer at its own instant.
func (r *settleRef) advance(target int) {
	for r.armed && r.due <= target {
		r.now = r.due
		v := r.s.v
		switch r.m.kind[v] {
		case decideStay:
			r.s = tstate{v, r.s.n + 1}
			r.armed = false // the entry stays; its timer has fired
		default:
			r.s = tstate{r.m.target[v], r.s.n + 1}
			r.open()
		}
	}
	r.now = target
}

func (r *settleRef) send(x int) {
	r.s = tstate{x % r.m.n, r.s.n + 1}
	r.open()
}

func settleRun(seed uint64) string {
	g := rand.New(rand.NewPCG(seed, 16))
	m := settleMachine{n: 2 + g.IntN(3)}
	for range m.n {
		m.timer = append(m.timer, g.IntN(5))
		m.kind = append(m.kind, []int{decideEnter, decideEmit, decideStay}[g.IntN(3)])
		m.target = append(m.target, g.IntN(m.n))
	}
	period := 2 + g.IntN(4)
	ref := &settleRef{m: m}
	ref.open()

	root := rt.Root("settle")
	line := NewTimeline(root, 0, 0)
	move := func(d int) { line.Advance(root, time.Duration(d)) }
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		return tdec{kind: decideEnter, next: tstate{e.x % m.n, s.n + 1}}, nil
	}
	fire := func(_ *rt.Task, at tstate, s tstate) (tdec, error) {
		k := m.kind[at.v]
		if k == decideStay {
			return tdec{kind: k, next: tstate{s.v, s.n + 1}}, nil
		}
		return tdec{kind: k, next: tstate{m.target[at.v], s.n + 1}, out: s.n}, nil
	}
	delay := func(s tstate) time.Duration {
		if m.timer[s.v] == 0 {
			return ActorNoTimer()
		}
		return time.Duration(m.timer[s.v])
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideIgnore || d.kind == decideReject || d.kind == decideDone {
			return s
		}
		return d.next
	}
	a := MachineStart(Site("settle"), root, lineSleeper{line}, tstate{}, step, fire, delay,
		noWork[tstate], hasNoWork, runNoWork[tsig], func(d tdec) int { return d.kind }, next,
		func(*rt.Task, tdec) {}, 4)
	sc := NewScope(root)
	var ticks atomic.Int64
	Go(&sc, Site("ticker"), func(ct *rt.Task) (struct{}, error) {
		for {
			line.Wait(ct, time.Duration(period))
			ticks.Add(1)
		}
	})
	// The watcher loops over Changes: it gets the first state and then
	// each commit, so after a settle it has seen 1 + n states.
	var seen atomic.Int64
	Go(&sc, Site("watcher"), func(ct *rt.Task) (struct{}, error) {
		for range a.Changes(ct) {
			seen.Add(1)
		}
		return struct{}{}, nil
	})
	defer func() {
		_ = a.Stop()
		sc.End(nil)
	}()

	// Advance(0) settles: the watcher has its first state, and the ticker
	// sleeps.
	move(0)
	shown := func() tstate {
		for s := range a.Changes(root) {
			return s
		}
		return tstate{-1, -1}
	}
	var trace []string
	for i := range 6 + g.IntN(12) {
		last := i > 0 && g.IntN(3) == 0
		if last || g.IntN(2) == 0 {
			d := g.IntN(7)
			trace = append(trace, fmt.Sprintf("advance %d", d))
			ref.advance(ref.now + d)
			move(d)
			if got := shown(); got != ref.s {
				return fmt.Sprintf("after %v: the actor shows %v, the fold %v (machine %+v)", trace, got, ref.s, m)
			}
			if got, want := seen.Load(), int64(1+ref.s.n); got != want {
				return fmt.Sprintf("after %v: the watcher saw %d states, want %d", trace, got, want)
			}
			if got, want := ticks.Load(), int64(ref.now/period); got != want {
				return fmt.Sprintf("after %v: %d ticks of period %d at %d, want %d", trace, got, period, ref.now, want)
			}
			continue
		}
		x := g.IntN(10)
		trace = append(trace, fmt.Sprintf("send %d", x))
		ref.send(x)
		if err := a.Send(root, tsig{x: x}); err != nil {
			return fmt.Sprintf("after %v: Send failed: %v", trace, err)
		}
	}
	// The trace ends with an Advance that settles the last Sends.
	move(0)
	if got := shown(); got != ref.s {
		return fmt.Sprintf("at the end of %v: the actor shows %v, the fold %v (machine %+v)", trace, got, ref.s, m)
	}
	return ""
}
