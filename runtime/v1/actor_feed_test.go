package v1

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The sources of a machine actor (actor.go rule 14; runtime.md, Machines
// and actors 15): generated sets of sources fed to a recorder actor, each
// one ending, failing with a defect, waiting forever after its values, or
// sending forever, and an actor that a Done, a typed failure, a source's
// defect, a Stop or a Drain ends. Against the rules: each source's values
// are stepped as a prefix of its values, in its order; a Drain steps every
// value queued before it; when the actor's Stop or Drain returns, no
// source runs (a waiting one was interrupted); the exit is the one the
// stepped values and the sources' defects give; and a Feed or a live
// State read on the ended actor fails Stopped, its source never called.
// Waits wait for events; a lost event fails at the test's deadline.

// The recorder's marker signals; every other signal is k*8+source and
// Stays, appended to the state.
const (
	fDone = -1
	fFail = -2
)

// fstart is the recorder: its state is the list of the signals it stayed
// on, so the state after the end is the stepped list without its marker.
func fstart(t *rt.Task, capacity int) *Actor[[]int, int, rdec] {
	step := func(_ *rt.Task, s []int, e int) (rdec, error) {
		switch e {
		case fDone:
			return rdec{decideDone, e}, nil
		case fFail:
			return rdec{}, errBoom
		}
		return rdec{decideStay, e}, nil
	}
	return MachineStart(Site("fed"), t, releaser{make(chan sleep)}, []int{}, step,
		func(*rt.Task, []int, []int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
		func([]int) time.Duration { return ActorNoTimer() }, noWork[[]int], hasNoWork, runNoWork[int],
		func(d rdec) int { return d.kind },
		func(s []int, d rdec) []int {
			if d.kind == decideStay {
				return append(slices.Clone(s), d.e)
			}
			return s
		}, func(*rt.Task, rdec) {}, capacity)
}

// fsource is a generated source: its values, then how it ends ("ends",
// "fails" with a defect, "waits" until its task is stopped, or "endless",
// which sends k*8+source for k on from len(values)).
type fsource struct {
	values []int
	end    string
}

type fcase struct {
	capacity int
	sources  []fsource
	finish   string // the test's end when no signal ended the actor: "stop" or "drain"
}

func genFeedCase(r *rand.Rand) fcase {
	c := fcase{capacity: r.Intn(3), finish: []string{"stop", "drain"}[r.Intn(2)]}
	for i := range 1 + r.Intn(3) {
		s := fsource{end: []string{"ends", "fails", "waits", "endless"}[r.Intn(4)]}
		for k := range r.Intn(6) {
			s.values = append(s.values, k*8+i)
		}
		if r.Intn(4) == 0 {
			s.values = append(s.values, []int{fDone, fFail}[r.Intn(2)])
		}
		c.sources = append(c.sources, s)
	}
	return c
}

// testDeadline fires shortly before t's deadline, never when t has none: a
// wait that would hang fails there, by an assertion.
func testDeadline(t *testing.T) <-chan time.Time {
	d, ok := t.Deadline()
	if !ok {
		return nil
	}
	return time.After(time.Until(d) * 9 / 10)
}

// fstate is one running source's record.
type fstate struct {
	called   atomic.Bool
	panicked atomic.Bool
	// sentAfterEnd is set when a send returned after admission closed.
	sentAfterEnd atomic.Bool
	settled      chan struct{} // closed when it returned, waits, or sent 3 endless values
	once         sync.Once
}

func (f *fstate) settle() { f.once.Do(func() { close(f.settled) }) }

func runFeedCase(t *testing.T, c fcase, seen map[string]bool) string {
	root := rt.Root("feed case")
	a := fstart(root, c.capacity)
	var running atomic.Int64
	states := make([]*fstate, len(c.sources))
	for i, src := range c.sources {
		f := &fstate{settled: make(chan struct{})}
		states[i] = f
		run := func(ft *rt.Task, send func(int)) {
			f.called.Store(true)
			running.Add(1)
			defer running.Add(-1)
			defer f.settle()
			for _, v := range src.values {
				send(v)
			}
			switch src.end {
			case "fails":
				f.panicked.Store(true)
				panic("a source's defect")
			case "waits":
				f.settle()
				<-ft.Done()
				panic(rt.Interrupted)
			case "endless":
				for k := len(src.values); ; k++ {
					closed := inboxClosed(a)
					send(k*8 + i)
					if closed {
						// The send on a closed inbox returned: the source
						// goes on after the actor's end.
						f.sentAfterEnd.Store(true)
					}
					if k == len(src.values)+2 {
						f.settle()
					}
				}
			}
		}
		if err := ActorFeed(root, a, run); err != nil {
			// Only a marker of an earlier source ends the actor this soon.
			seen["a feed raced the end"] = true
			if !errors.Is(err, ActorStopped{}) {
				return fmt.Sprintf("feed %d: %v", i, err)
			}
		}
	}
	limit := testDeadline(t)
	for i, f := range states {
		select {
		case <-f.settled:
		case <-a.done:
		case <-limit:
			return fmt.Sprintf("source %d never settled and the actor did not end", i)
		}
	}
	endedAlone := false
	select {
	case <-a.done:
		endedAlone = true
	default:
	}
	var raised any
	func() {
		defer func() { raised = recover() }()
		if !endedAlone && c.finish == "drain" {
			_ = a.Drain()
		} else {
			_ = a.Stop()
		}
	}()
	// The end returned: no source may run now (rule 14).
	if n := running.Load(); n != 0 {
		return fmt.Sprintf("%d sources still run after the actor's end", n)
	}
	for i, f := range states {
		if f.sentAfterEnd.Load() {
			return fmt.Sprintf("source %d: a send returned after the actor's admission closed", i)
		}
	}
	stepped := a.State(root)
	kind := ActorExitKind(root, a)
	marker := 0
	if ActorHasLatest(a) {
		if d := ActorLatest(a); d.kind == decideDone {
			marker = fDone
		}
	}
	if kind == exitFailed {
		marker = fFail
	}
	// Each source's stepped values are a prefix of its values, in order.
	drained := !endedAlone && c.finish == "drain"
	for i, src := range c.sources {
		var got []int
		for _, e := range stepped {
			if e >= 0 && e%8 == i {
				got = append(got, e)
			}
		}
		all := slices.Clone(src.values)
		if src.end == "endless" && (len(all) == 0 || all[len(all)-1] >= 0) {
			for k, n := len(all), len(all)+len(got); k < n; k++ {
				all = append(all, k*8+i)
			}
		}
		if len(got) > len(all) || !slices.Equal(got, all[:len(got)]) {
			return fmt.Sprintf("source %d: stepped %v, not a prefix of %v", i, got, all)
		}
		// A Drain with no earlier end steps every value its source sent
		// before it.
		if drained && kind == exitHalted && src.end != "endless" {
			want := slices.DeleteFunc(slices.Clone(src.values), func(v int) bool { return v < 0 })
			if !slices.Equal(got, want) {
				return fmt.Sprintf("source %d: the drain stepped %v, want %v", i, got, want)
			}
			seen["a drain stepped every source"] = true
		}
	}
	panicked := false
	for _, f := range states {
		panicked = panicked || f.panicked.Load()
	}
	wantKind := exitHalted
	switch {
	case marker == fFail:
		wantKind = exitFailed
	case panicked:
		wantKind = exitDefected
	case marker == fDone:
		wantKind = exitFinished
	}
	if kind != wantKind {
		return fmt.Sprintf("exit kind %d, want %d (marker %d, a source's defect %v)", kind, wantKind, marker, panicked)
	}
	if (raised != nil) != (kind == exitDefected) {
		return fmt.Sprintf("the end raised %v with exit kind %d", raised, kind)
	}
	// The ended actor takes no source and answers no live read.
	var late fstate
	if err := ActorFeed(root, a, func(*rt.Task, func(int)) { late.called.Store(true) }); !errors.Is(err, ActorStopped{}) {
		return fmt.Sprintf("a Feed after the end: %v", err)
	}
	if late.called.Load() {
		return "a Feed after the end ran its source"
	}
	if _, err := ActorLiveState(root, a); !errors.Is(err, ActorStopped{}) {
		return fmt.Sprintf("a live State read after the end: %v", err)
	}
	for i, src := range c.sources {
		if src.end == "waits" && states[i].called.Load() {
			seen["the end interrupted a waiting source"] = true
		}
		if src.end == "endless" && states[i].called.Load() {
			seen["a source outlived the actor's end"] = true
		}
	}
	seen[map[int]string{exitFinished: "done", exitFailed: "fail", exitDefected: "a source's defect", exitHalted: c.finish}[kind]] = true
	return ""
}

// TestActorLiveReadQueuedAtTheEndFailsStopped: a live State read (Can's)
// queued behind a step when a Stop ends the actor is released with
// Stopped, not answered as live: the property's reads come after the end,
// when admission refuses them, so they never reach this release.
func TestActorLiveReadQueuedAtTheEndFailsStopped(t *testing.T) {
	root := rt.Root("live read")
	entered, hold := make(chan struct{}), make(chan struct{})
	a := signalLog(root, 1, entered, hold)
	if err := a.Send(root, -1); err != nil {
		t.Fatal(err)
	}
	limit := testDeadline(t)
	select {
	case <-entered:
	case <-limit:
		t.Fatal("the runner never took the held signal")
	}
	read := make(chan error, 1)
	go func() {
		_, err := ActorLiveState(rt.Root("reader"), a)
		read <- err
	}()
	stopped := make(chan struct{})
	for a.inbox.Len() == 0 {
		select {
		case <-limit:
			t.Fatal("the live read was never queued")
		default:
			runtime.Gosched()
		}
	}
	go func() {
		defer close(stopped)
		_ = a.Stop()
	}()
	for !inboxClosed(a) {
		select {
		case <-limit:
			t.Fatal("the Stop never closed admission")
		default:
			runtime.Gosched()
		}
	}
	close(hold)
	select {
	case err := <-read:
		if !errors.Is(err, ActorStopped{}) {
			t.Fatalf("a live read queued at the end: %v, want Stopped", err)
		}
	case <-limit:
		t.Fatal("the queued live read was never answered")
	}
	<-stopped
}

// TestActorFeedSourcesEndWithTheActor is the acceptance property of
// actor.Feed (MU10): seeded (1, or ERGO_ACTOR_SEED), at least 300 cases
// and until each kind below is reached. A failure prints the seed, the
// case and what broke.
func TestActorFeedSourcesEndWithTheActor(t *testing.T) {
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
	want := []string{"done", "fail", "a source's defect", "stop", "drain", "a drain stepped every source",
		"the end interrupted a waiting source", "a source outlived the actor's end"}
	n := 0
	for ; n < 300 || slices.ContainsFunc(want, func(w string) bool { return !seen[w] }); n++ {
		if n == 5000 {
			for _, w := range want {
				if !seen[w] {
					t.Fatalf("seed %d: %d cases reach no %q", seed, n, w)
				}
			}
		}
		c := genFeedCase(r)
		if why := runFeedCase(t, c, seen); why != "" {
			t.Fatalf("seed %d, case %d: %s\ncapacity %d, sources %+v, finish %s", seed, n, why, c.capacity, c.sources, c.finish)
		}
	}
	t.Logf("seed %d: %d cases", seed, n)
}
