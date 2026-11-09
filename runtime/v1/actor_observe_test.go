package v1

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The observers, Drain and the exit of a machine actor (actor.go rules 11
// and 12; runtime.md, Machines and actors 13; the Quint model's
// changesInOrderOnce, waitForMatchesReplay, nothingAfterEnd and
// drainStepsQueued) against the pure fold's state list. The machine's
// state is the list of the signals it committed, so the i-th written
// state is the fold's final list cut to i, and a state names its place.
// The runner is held in a step (a hold signal) wherever the test needs a
// known state: a wait starts there, and a Stop or a Drain is called while
// signals are queued behind it. Waits wait for events, never for time.

// The signals of the observed machine: a hold waits in its step until the
// test releases it, then ignores (oHold) or stays (oHoldStay, a state
// written in the step a Stop or a Drain overtakes); oDone is Done and oFail
// a typed failure; any other e < 1000 is by e%4 Enter, Stay, Ignore or
// Emit, and a commit appends e.
const (
	oHold     = -1
	oHoldStay = -2
	oDone     = 1000
	oFail     = 1001
)

func odecide(e int) int {
	switch e {
	case oHold:
		return decideIgnore
	case oHoldStay:
		return decideStay
	case oDone:
		return decideDone
	}
	return []int{decideEnter, decideStay, decideIgnore, decideEmit}[e%4]
}

func ostart(t *rt.Task, capacity int, entered, release chan struct{}) *Actor[[]int, int, rdec] {
	step := func(_ *rt.Task, s []int, e int) (rdec, error) {
		if e == oHold || e == oHoldStay {
			entered <- struct{}{}
			<-release
		}
		if e == oFail {
			return rdec{}, errBoom
		}
		return rdec{odecide(e), e}, nil
	}
	return MachineStart(Site("observed"), t, releaser{make(chan sleep)}, []int{}, step,
		func(*rt.Task, []int, []int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
		func([]int) time.Duration { return ActorNoTimer() }, noWork[[]int], hasNoWork, runNoWork[int],
		func(d rdec) int { return d.kind },
		func(s []int, d rdec) []int {
			switch d.kind {
			case decideEnter, decideStay, decideEmit:
				return append(slices.Clone(s), d.e)
			}
			return s
		}, func(*rt.Task, rdec) {}, capacity)
}

// ofold is the pure fold of the signals the runner steps: the final state
// (every written state is a prefix of it), how the actor ends ("" when
// no signal ends it), and the decision of the last step.
func ofold(stepped []int) (final []int, end string, last rdec, hasLast bool) {
	final = []int{}
	for _, e := range stepped {
		if e == oFail {
			return final, "failed", last, hasLast
		}
		d := rdec{odecide(e), e}
		last, hasLast = d, true
		if d.kind == decideDone {
			return final, "done", last, hasLast
		}
		if d.kind != decideIgnore {
			final = append(final, e)
		}
	}
	return final, "", last, hasLast
}

// oop is one operation of a case: "send" or "ask" e; "observe" with obs
// "full", "lag" (reads its first state, then waits until the actor ended)
// or "wait" (WaitFor len(s) >= k); or the end: "stop" or "drain" with the
// signals q queued behind a held step, "done" or "fail".
type oop struct {
	kind, obs string
	e, k      int
	q         []int
}

type ocase struct {
	capacity int
	ops      []oop
}

func genObserveCase(r *rand.Rand) ocase {
	c := ocase{capacity: r.Intn(4)}
	for range r.Intn(9) {
		switch r.Intn(5) {
		case 0, 1:
			c.ops = append(c.ops, oop{kind: "send", e: r.Intn(40)})
		case 2:
			c.ops = append(c.ops, oop{kind: "ask", e: r.Intn(40)})
		default:
			c.ops = append(c.ops, oop{kind: "observe", obs: []string{"full", "lag", "wait"}[r.Intn(3)], k: r.Intn(10)})
		}
	}
	end := oop{kind: []string{"stop", "drain", "stop", "drain", "done", "fail"}[r.Intn(6)]}
	for range r.Intn(c.capacity + 1) {
		end.q = append(end.q, r.Intn(40))
	}
	c.ops = append(c.ops, end)
	return c
}

// oobs is one observer: its kind, the length of the state at its start,
// what it read, and a wait's answer.
type oobs struct {
	kind     string
	k, start int
	got      [][]int
	answer   []int
	stopped  bool
	done     chan struct{}
}

// inboxClosed reports whether a's admission has closed.
func inboxClosed[S, E, D any](a *Actor[S, E, D]) bool {
	a.inbox.mu.Lock()
	defer a.inbox.mu.Unlock()
	return a.inbox.closed
}

// runObserveCase runs c and returns what broke, "" when nothing did.
func runObserveCase(c ocase, seen map[string]bool) string {
	root := rt.Root("observe")
	entered, release := make(chan struct{}), make(chan struct{})
	a := ostart(root, c.capacity, entered, release)
	lag := make(chan struct{})
	var stepped []int
	var obs []*oobs
	var endErr error
	stoppedQueued := 0
	for _, o := range c.ops {
		switch o.kind {
		case "send":
			if err := a.Send(root, o.e); err != nil {
				return fmt.Sprintf("send %d: %v", o.e, err)
			}
			stepped = append(stepped, o.e)
		case "ask":
			d, err := a.Ask(root, o.e)
			if err != nil || d != (rdec{odecide(o.e), o.e}) {
				return fmt.Sprintf("ask %d: %v %v", o.e, d, err)
			}
			stepped = append(stepped, o.e)
		case "observe":
			// The runner is held: the state is the fold of what was sent.
			if err := a.Send(root, oHold); err != nil {
				return fmt.Sprintf("hold: %v", err)
			}
			<-entered
			now, _, _, _ := ofold(stepped)
			ob := &oobs{kind: o.obs, k: o.k, start: len(now), done: make(chan struct{})}
			obs = append(obs, ob)
			ready := make(chan struct{})
			go func() {
				defer close(ob.done)
				w := rt.Root("observer")
				for s := range a.Changes(w) {
					ob.got = append(ob.got, s)
					if len(ob.got) == 1 {
						close(ready)
						if ob.kind == "lag" {
							<-lag
						}
					}
					if ob.kind == "wait" && len(s) >= ob.k {
						ob.answer = s // std actor.WaitFor's loop
						return
					}
				}
				if len(ob.got) == 0 {
					close(ready)
				}
				ob.stopped = ob.kind == "wait"
			}()
			<-ready
			release <- struct{}{}
			stepped = append(stepped, oHold)
		case "stop", "drain":
			if err := a.Send(root, oHoldStay); err != nil {
				return fmt.Sprintf("hold: %v", err)
			}
			<-entered
			for _, e := range o.q {
				if err := a.Send(root, e); err != nil {
					return fmt.Sprintf("queue %d: %v", e, err)
				}
			}
			ended := make(chan error, 1)
			go func() {
				if o.kind == "stop" {
					ended <- a.Stop()
				} else {
					ended <- a.Drain()
				}
			}()
			for !inboxClosed(a) {
				runtime.Gosched() // admission closed: the call is under way
			}
			release <- struct{}{}
			endErr = <-ended
			stepped = append(stepped, oHoldStay)
			if o.kind == "drain" {
				stepped = append(stepped, o.q...)
				if len(o.q) > 0 {
					seen["a drain stepped queued signals"] = true
				}
			} else {
				stoppedQueued = len(o.q)
			}
			if err := a.Send(root, 1); !errors.Is(err, ActorStopped{}) {
				return fmt.Sprintf("send after %s: %v", o.kind, err)
			}
		case "done", "fail":
			e := oDone
			if o.kind == "fail" {
				e = oFail
			}
			if err := a.Send(root, e); err != nil {
				return fmt.Sprintf("send %d: %v", e, err)
			}
			stepped = append(stepped, e)
			seen[o.kind] = true
		}
	}
	// ExitIsShared: every caller gets one exit, and Stop agrees with it.
	kinds := make(chan int, 3)
	for range 3 {
		go func() { kinds <- ActorExitKind(rt.Root("exit"), a) }()
	}
	final, end, last, hasLast := ofold(stepped)
	wantKind := map[string]int{"done": exitFinished, "failed": exitFailed, "": exitHalted}[end]
	for range 3 {
		if k := <-kinds; k != wantKind {
			return fmt.Sprintf("exit kind %d, want %d (%q)", k, wantKind, end)
		}
	}
	if err := a.Stop(); (end == "failed") != (err == errBoom) || (end != "failed" && endErr != nil) {
		return fmt.Sprintf("Stop %v, the end's %v; want the failure only after %q", err, endErr, end)
	}
	if end == "done" && ActorFinal(a).kind != decideDone {
		return fmt.Sprintf("final %v", ActorFinal(a))
	}
	close(lag)
	for i, ob := range obs {
		<-ob.done
		// The observer reads final[:start], final[:start+1], … once each.
		want := [][]int{}
		for n := ob.start; n <= len(final); n++ {
			want = append(want, final[:n])
			if ob.kind == "wait" && n >= ob.k {
				break
			}
		}
		if !slices.EqualFunc(ob.got, want, slices.Equal) {
			return fmt.Sprintf("observer %d (%s from %d): read %v, want %v", i, ob.kind, ob.start, ob.got, want)
		}
		if ob.kind == "wait" {
			matched := max(ob.start, ob.k) <= len(final)
			if matched == ob.stopped || (matched && !slices.Equal(ob.answer, final[:max(ob.start, ob.k)])) {
				return fmt.Sprintf("wait %d from %d: answer %v stopped %v; final %v", ob.k, ob.start, ob.answer, ob.stopped, final)
			}
			if !matched {
				seen["a wait ended Stopped"] = true
			} else if ob.k > ob.start {
				seen["a wait matched a state after its start"] = true
			}
		}
		if ob.kind == "lag" && stoppedQueued+len(final)-ob.start >= 2 && c.ops[len(c.ops)-1].kind == "stop" {
			seen["a stop overtook unread states"] = true
		}
	}
	if got := a.State(root); !slices.Equal(got, final) {
		return fmt.Sprintf("final state %v, want %v (stepped %v)", got, final, stepped)
	}
	if ActorHasLatest(a) != hasLast || (hasLast && ActorLatest(a) != last) {
		return fmt.Sprintf("latest %v %v, want %v %v", ActorHasLatest(a), ActorLatest(a), hasLast, last)
	}
	if len(obs) >= 3 {
		seen["three observers"] = true
	}
	if stoppedQueued > 0 {
		seen["a stop dropped queued signals"] = true
	}
	a.mu.Lock()
	subs := len(a.subs)
	a.mu.Unlock()
	if subs != 0 {
		return fmt.Sprintf("%d subscriptions left after every loop returned", subs)
	}
	return ""
}

// TestActorObserveAgreesWithFold runs generated cases (seed 1, or
// ERGO_ACTOR_SEED) until each kind below is reached, and at least 300.
// A failure prints the seed, the case and what broke.
func TestActorObserveAgreesWithFold(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_ACTOR_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	want := []string{"a drain stepped queued signals", "a stop dropped queued signals", "a stop overtook unread states",
		"a wait ended Stopped", "a wait matched a state after its start", "three observers", "done", "fail"}
	n := 0
	for ; n < 300 || slices.ContainsFunc(want, func(w string) bool { return !seen[w] }); n++ {
		if n == 5000 {
			for _, w := range want {
				if !seen[w] {
					t.Fatalf("seed %d: %d cases reach no %q", seed, n, w)
				}
			}
		}
		c := genObserveCase(r)
		if why := runObserveCase(c, seen); why != "" {
			t.Fatalf("seed %d, case %d: %s\ncapacity %d, ops %+v", seed, n, why, c.capacity, c.ops)
		}
	}
	t.Logf("seed %d: %d cases", seed, n)
}
