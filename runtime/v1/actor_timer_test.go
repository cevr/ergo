package v1

import (
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestActorTimerArmsBeforePublish holds rule 7's commit: a state timer's
// sleep is registered with the Scheduler before the entry is published, so
// a driver that sees the entry (Changes) and at once advances a manual
// clock by the delay fires the timer, every time. Generated chains of
// entries with seeded delays; each entry's timer enters the next. The
// mutant it kills: arm returns before the timer child registered its sleep
// (the sleep then starts after the Advance and waits another delay). Set
// ERGO_ACTOR_TIMER_SEED to replay a seed.
func TestActorTimerArmsBeforePublish(t *testing.T) {
	seed := time.Now().UnixNano()
	if v := os.Getenv("ERGO_ACTOR_TIMER_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	for c := 0; c < 200; c++ {
		k := 2 + r.Intn(7)
		delays := make([]time.Duration, k-1)
		for i := range delays {
			delays[i] = time.Duration(1 + r.Int63n(int64(time.Hour)))
		}
		if msg := timerChain(delays); msg != "" {
			t.Fatalf("seed %d (ERGO_ACTOR_TIMER_SEED=%d), case %d, delays %v: %s", seed, seed, c, delays, msg)
		}
	}
}

// timerChain runs an actor whose state i has the timer delays[i], which
// enters state i+1; the last state has none. It returns what broke, or "".
func timerChain(delays []time.Duration) string {
	root := rt.Root("timer chain")
	// The clock's domain is a root of its own: the actor is no member, so
	// Advance does not wait for it, and the publish order shows.
	clock := rt.Root("clock")
	l := NewTimeline(clock, 0, 0)
	last := len(delays)
	step := func(*rt.Task, tstate, tsig) (tdec, error) { return tdec{kind: decideIgnore}, nil }
	fire := func(_ *rt.Task, at tstate, _ tstate) (tdec, error) {
		return tdec{kind: decideEnter, next: tstate{v: at.v + 1}}, nil
	}
	delay := func(s tstate) time.Duration {
		if s.v >= last {
			return ActorNoTimer()
		}
		return delays[s.v]
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter {
			return d.next
		}
		return s
	}
	a := MachineStart(Site("timer chain"), root, lineSleeper{l}, tstate{}, step, fire, delay,
		noWork[tstate], hasNoWork, runNoWork[tsig], func(d tdec) int { return d.kind }, next,
		func(*rt.Task, tdec) {}, 0)
	defer func() { _ = a.Stop() }()
	want := 0
	for s := range a.Changes(root) {
		if s.v != want {
			return "entered state " + strconv.Itoa(s.v) + ", want " + strconv.Itoa(want)
		}
		if s.v == last {
			return ""
		}
		if n := l.Sleepers(); n != 1 {
			return "state " + strconv.Itoa(s.v) + " is published with " + strconv.Itoa(n) + " sleepers registered, want its timer's 1"
		}
		l.Advance(clock, delays[s.v])
		want++
	}
	return "the actor ended before the last state"
}
