package v1

import (
	"math/rand"
	"os"
	"strconv"
	"sync/atomic"
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
	a := MachineStart(Site("timer chain"), root, lineSleeper{l}, nil, tstate{}, step, fire, delay,
		noWork[tstate], hasNoWork, runNoWork[tsig], func(d tdec) int { return d.kind }, next,
		func(*rt.Task, tdec) {}, 0)
	defer func() { _ = a.Stop(root) }()
	want := 0
	for s := range a.Changes(root, everyState) {
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

type lineAlarmSleeper struct{ l *Timeline }

func (s lineAlarmSleeper) Sleep(t *rt.Task, d time.Duration) { s.l.Wait(t, d) }
func (s lineAlarmSleeper) Alarms(_ *rt.Task) *AlarmClock     { return s.l.Alarms() }

// TestDurableTimerPreservesRemainingDeadlineOnRestart holds that a durable
// supervised actor recovering from a crash and backoff preserves the remaining
// monotonic target deadline of its armed state rather than resetting the full
// relative timerDelay.
func TestDurableTimerPreservesRemainingDeadlineOnRestart(t *testing.T) {
	root := rt.Root("durable timer preserve")
	l := NewTimeline(root, 0, 0)
	sl := lineAlarmSleeper{l}

	saved := tstate{v: 1} // 1: armed with 10s -> timer fires into state 2
	var firedAt atomic.Int64
	var crashed atomic.Bool
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		if e.x < 0 && !crashed.Swap(true) {
			panic("injected durable step crash")
		}
		return tdec{kind: decideStay, next: s}, nil
	}
	fire := func(_ *rt.Task, _ tstate, _ tstate) (tdec, error) {
		firedAt.Store(l.Read().Mono)
		return tdec{kind: decideEnter, next: tstate{v: 2}}, nil
	}
	delay := func(s tstate) time.Duration {
		if s.v == 1 {
			return 10 * time.Second
		}
		return ActorNoTimer()
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay {
			return d.next
		}
		return s
	}
	a, err := ActorRecovering(root,
		func(*rt.Task) (tstate, error) { return saved, nil },
		func(st *rt.Task, initial tstate) *Actor[tstate, tsig, tdec] {
			return MachineSupervise(Site("durable timer preserve"), st, sl, nil, initial,
				func(*rt.Task) (tstate, error) { return saved, nil },
				2, func(*rt.Task) time.Duration { return 2 * time.Second },
				step, fire, delay,
				noWork[tstate], hasNoWork, runNoWork[tsig],
				nil, func(*rt.Task, tstate) {},
				func(d tdec) int { return d.kind }, next,
				func(*rt.Task, tdec) {}, 4)
		})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop(root) }()

	// Advance 4s of the 10s timer, then inject crash (+2s backoff -> t=6s).
	l.Advance(root, 4*time.Second)
	if err := a.Send(root, tsig{x: -1}); err != nil {
		t.Fatal(err)
	}
	// Backoff is 2s; advancing 2s puts us at t=6s with 4s remaining until t=10s.
	l.Advance(root, 2*time.Second)
	if s := a.State(root); s.v != 1 {
		t.Fatalf("after restart at 6s, state = %d, want 1 (still waiting for remaining 4s)", s.v)
	}
	// Advance 3s (t=9s): timer must NOT have fired yet.
	l.Advance(root, 3*time.Second)
	if s := a.State(root); s.v != 1 {
		t.Fatalf("at 9s before 10s deadline, state = %d, want 1", s.v)
	}
	// Advance final 1s (t=10s): timer must fire at 10s (NOT repeat full 10s until 16s).
	l.Advance(root, 1*time.Second)
	if s := a.State(root); s.v != 2 {
		t.Fatalf("at 10s target deadline, state = %d, want 2 (firedAt=%v)", s.v, firedAt.Load())
	}
	if want := int64(10 * time.Second); firedAt.Load() != want {
		t.Fatalf("firedAt = %d, want %d", firedAt.Load(), want)
	}
}

// TestDurableTimerFiresImmediatelyWhenExpiredDuringBackoff holds that when a
// durable supervised actor's target deadline elapses while backing off, re-arming
// fires immediately instead of panicking MachineTimerNotPositive or repeating.
func TestDurableTimerFiresImmediatelyWhenExpiredDuringBackoff(t *testing.T) {
	root := rt.Root("durable timer expired")
	l := NewTimeline(root, 0, 0)
	sl := lineAlarmSleeper{l}

	saved := tstate{v: 1}
	var firedAt atomic.Int64
	var crashed atomic.Bool
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		if e.x < 0 && !crashed.Swap(true) {
			panic("injected durable step crash")
		}
		return tdec{kind: decideStay, next: s}, nil
	}
	fire := func(_ *rt.Task, _ tstate, _ tstate) (tdec, error) {
		firedAt.Store(l.Read().Mono)
		return tdec{kind: decideEnter, next: tstate{v: 2}}, nil
	}
	delay := func(s tstate) time.Duration {
		if s.v == 1 {
			return 5 * time.Second
		}
		return ActorNoTimer()
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay {
			return d.next
		}
		return s
	}
	a, err := ActorRecovering(root,
		func(*rt.Task) (tstate, error) { return saved, nil },
		func(st *rt.Task, initial tstate) *Actor[tstate, tsig, tdec] {
			return MachineSupervise(Site("durable timer expired"), st, sl, nil, initial,
				func(*rt.Task) (tstate, error) { return saved, nil },
				2, func(*rt.Task) time.Duration { return 4 * time.Second },
				step, fire, delay,
				noWork[tstate], hasNoWork, runNoWork[tsig],
				nil, func(*rt.Task, tstate) {},
				func(d tdec) int { return d.kind }, next,
				func(*rt.Task, tdec) {}, 4)
		})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop(root) }()

	l.Advance(root, 3*time.Second)
	if err := a.Send(root, tsig{x: -1}); err != nil {
		t.Fatal(err)
	}
	// Backoff is 4s -> restart completes at t=7s (> 5s deadline): fires immediately.
	l.Advance(root, 4*time.Second)
	if s := a.State(root); s.v != 2 {
		t.Fatalf("after expired deadline on recovery at 7s, state = %d, want 2", s.v)
	}
	if want := int64(7 * time.Second); firedAt.Load() != want {
		t.Fatalf("firedAt = %d, want %d", firedAt.Load(), want)
	}
}

// TestDurableExpiredSentinelFiresWithoutPanic holds that the expiredTimer
// sentinel (noTimer + 1) triggers an immediate timer firing without raising
// MachineTimerNotPositive.
func TestDurableExpiredSentinelFiresWithoutPanic(t *testing.T) {
	root := rt.Root("expired sentinel")
	l := NewTimeline(root, 0, 0)
	sl := lineAlarmSleeper{l}

	var fired atomic.Bool
	step := func(_ *rt.Task, s tstate, _ tsig) (tdec, error) { return tdec{kind: decideStay, next: s}, nil }
	fire := func(_ *rt.Task, _ tstate, _ tstate) (tdec, error) {
		fired.Store(true)
		return tdec{kind: decideEnter, next: tstate{v: 2}}, nil
	}
	delay := func(s tstate) time.Duration {
		if s.v == 0 {
			return expiredTimer
		}
		return ActorNoTimer()
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay {
			return d.next
		}
		return s
	}
	a := MachineStart(Site("expired sentinel"), root, sl, nil, tstate{v: 0}, step, fire, delay,
		noWork[tstate], hasNoWork, runNoWork[tsig],
		func(d tdec) int { return d.kind }, next, func(*rt.Task, tdec) {}, 2)
	defer func() { _ = a.Stop(root) }()

	l.Advance(root, 0)
	if !fired.Load() || a.State(root).v != 2 {
		t.Fatalf("expired sentinel did not fire immediately: fired=%v state=%v", fired.Load(), a.State(root))
	}
}
