package v1

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
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
//
// Beside them, generated member tasks wait on a sink actor in each of its
// waits (settleWaits): the sink's step waits, masked, on a full queue that
// only the test's task empties, one value after each Advance, so the
// waiters wait on the test's task (batch-13 round 2, B1). Every Advance
// must still return: each of those waits is parked. Its liveness bound is
// the test's own deadline (testwait), which names the seed and the trace
// of the Advance that did not return. Mutants, each a wait left unparked:
// Stop's (B1, killed at seed 5), Drain's (B1, seed 23), an Ask's or a
// State's answer (await, seed 0), ExitOf's end (waitDone, seed 0) and a
// full Send's room (mailbox, seed 2).
func TestManualAdvanceSettles(t *testing.T) {
	var where atomic.Value
	where.Store("before the first seed")
	stop := testwait.AfterFunc(t, func() {
		panic("TestManualAdvanceSettles: an Advance did not return by the test's deadline: " + where.Load().(string))
	})
	defer stop()
	for seed := uint64(0); seed < 300; seed++ {
		if msg := settleRun(seed, &where); msg != "" {
			t.Fatalf("seed %d: %s", seed, msg)
		}
	}
}

// TestManualAdvanceOneAtATime is the smallest repro of B1's second failure
// (batch 13, fua-b): two tasks of one manual clock's domain each called
// Advance, and each Settle waited for the other to park, forever. One
// Advance of a clock runs at a time: the second call is a defect raised in
// its caller, and the first returns. Which caller is second is the
// scheduler's choice, so the test holds that exactly one raised it. Its
// liveness bound is the test's own deadline (testwait). Mutant: the check
// of a running Advance removed (both wait forever).
func TestManualAdvanceOneAtATime(t *testing.T) {
	stop := testwait.AfterFunc(t, func() {
		panic("TestManualAdvanceOneAtATime: an Advance did not return by the test's deadline")
	})
	defer stop()
	root := rt.Root("two")
	line := NewTimeline(root, 0, 0)
	sc := NewScope(root)
	advance := func(ct *rt.Task) (raised any) {
		defer func() { raised = recover() }()
		line.Advance(ct, time.Second)
		return nil
	}
	var other any
	h := ownedGo(&sc, Site("other"), func(ct *rt.Task) (struct{}, error) {
		other = advance(ct)
		return struct{}{}, nil
	})
	mine := advance(root)
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	sc.End(nil)
	var defects []string
	for _, r := range []any{mine, other} {
		if r != nil {
			defects = append(defects, fmt.Sprint(r))
		}
	}
	if len(defects) != 1 || !strings.Contains(defects[0], "Advance while another task's Advance of this clock is running") {
		t.Fatalf("raised %q, want the running-Advance defect in exactly one caller", defects)
	}
	if got := line.Read().Mono; got != int64(time.Second) {
		t.Fatalf("the clock reads %d after one Advance(1s)", got)
	}
}

// settleWaits are the waits of a member task on the sink actor.
var settleWaits = []string{"stop", "drain", "ask", "exit", "state", "send"}

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

func settleRun(seed uint64, where *atomic.Value) string {
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
	var trace []string
	move := func(d int) {
		where.Store(fmt.Sprintf("seed %d, Advance(%d) after %v", seed, d, trace))
		line.Advance(root, time.Duration(d))
	}
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

	// The sink steps each signal by a masked Push into full, which holds
	// a value already, so each step waits until the test's task polls.
	full := New[int](1)
	_ = full.Push(root, -1)
	sinkStep := func(ct *rt.Task, s tstate, e tsig) (tdec, error) {
		mask := ct.Mask()
		defer mask.Unmask()
		_ = full.Push(ct, e.x)
		return tdec{kind: decideStay, next: tstate{s.v, s.n + 1}}, nil
	}
	sink := MachineStart(Site("sink"), root, lineSleeper{line}, tstate{}, sinkStep, fire,
		func(tstate) time.Duration { return ActorNoTimer() },
		noWork[tstate], hasNoWork, runNoWork[tsig], func(d tdec) int { return d.kind }, next,
		func(*rt.Task, tdec) {}, 1)
	// At most two: one in the step, one in the inbox of one, so the
	// test's own Sends never wait on itself.
	sends := g.IntN(3)
	for range sends {
		_ = sink.Send(root, tsig{x: 1})
	}

	sc := NewScope(root)
	var ticks atomic.Int64
	ownedGo(&sc, Site("ticker"), func(ct *rt.Task) (struct{}, error) {
		for {
			line.Wait(ct, time.Duration(period))
			ticks.Add(1)
		}
	})
	// The watcher loops over Changes: it gets the first state and then
	// each commit, so after a settle it has seen 1 + n states.
	var seen atomic.Int64
	ownedGo(&sc, Site("watcher"), func(ct *rt.Task) (struct{}, error) {
		for range a.Changes(ct) {
			seen.Add(1)
		}
		return struct{}{}, nil
	})
	var waits []string
	for range g.IntN(4) {
		w := settleWaits[g.IntN(len(settleWaits))]
		waits = append(waits, w)
		ownedGo(&sc, Site("waiter "+w), func(ct *rt.Task) (struct{}, error) {
			switch w {
			case "stop":
				_ = sink.Stop(ct)
			case "drain":
				_ = sink.Drain(ct)
			case "ask":
				_, _ = sink.Ask(ct, tsig{x: 2})
			case "exit":
				ActorExitKind(ct, sink)
			case "state":
				sink.State(ct)
			case "send":
				_ = sink.Send(ct, tsig{x: 3})
			}
			return struct{}{}, nil
		})
	}
	trace = append(trace, fmt.Sprintf("sends %d, waits [%s]", sends, strings.Join(waits, " ")))
	defer func() {
		// Closing full ends every step's Push, so the sink and its
		// waiters end.
		full.End()
		_ = sink.Stop(root)
		_ = a.Stop(root)
		sc.End(nil)
	}()

	// Advance(0) settles: the watcher has its first state, and the ticker
	// sleeps.
	move(0)
	full.Poll()
	shown := func() tstate {
		for s := range a.Changes(root) {
			return s
		}
		return tstate{-1, -1}
	}
	for i := range 6 + g.IntN(12) {
		last := i > 0 && g.IntN(3) == 0
		if last || g.IntN(2) == 0 {
			d := g.IntN(7)
			trace = append(trace, fmt.Sprintf("advance %d", d))
			ref.advance(ref.now + d)
			move(d)
			full.Poll()
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
