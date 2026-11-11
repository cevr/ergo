package v1

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The recovered-hosts property (wf10 DH1; rule 15; runtime.md, Machines
// and actors 16 and 17): a seeded script drives a durable, supervised
// actor whose pure step makes hosts with NewHost, enters, stays and
// ignores, and may carry the old state's hosts. Its committed signals are
// its journal. At generated points the actor crashes and recovers:
//   - stop: the actor stops, and ActorRecovering folds the journal (the
//     recover, as StartDurable's) and starts a new supervised actor in the
//     state it gives;
//   - defect: a step raises, and the supervisor's restart folds the journal
//     again (renew, as SuperviseDurable's);
//   - failed recover: the fold makes its hosts and then fails;
//   - late host: a task the recover started makes a host after the recover
//     returned;
//   - no actor: the start opens no actor.
//
// The reference is the fold of the journal over counts (no hosts): the
// recovered state's entry and number of hosts. What it holds:
//   - the recovered state is the reference's, and a second fold of the
//     journal gives the same (replay is deterministic);
//   - each host the recover made that the recovered state holds is filled,
//     when the start returns, by the first entry's work with a child of the
//     new incarnation (fresh empty hosts after recovery, carried ones too);
//   - each other host the recover made is closed when the start returns:
//     the hosts only an earlier state of the replay held, every host of a
//     failed recover, a host made after the recover returned, and the
//     hosts of a start that opened no actor;
//   - no leaked child: when the old actor stopped, and at the end, every
//     child it started ended and every host it held is closed.
//
// Mutants (runtime/v1 testdata/mutants.json, each killed here only):
// dh1-unbound-closed (NewHost in a recover closes the host at once),
// dh1-first-entry-binds-none (openFirst does not bind the recovered
// hosts), dh1-rest-kept (the hosts the first state does not hold are not
// closed), dh1-all-held (heldHosts counts every host as held),
// dh1-renew-unbound (a restart's fresh runs with no recovery),
// dh1-failed-recover-kept (a failed recover's hosts are not closed),
// dh1-late-host-kept (a host made after the recover returned is kept),
// dh1-no-actor-kept (a start that opens no actor leaves its hosts made)
// and dh1-first-publish-early (openFirst does not wait for the fills).

// dsig is a signal of the script: a decision with the hosts its step
// makes, or boom, a step that raises.
type dsig struct {
	kind  int // decideEnter, decideStay or decideIgnore
	fresh int
	carry bool // an Enter keeps the old state's hosts
	boom  bool
}

// dst is a state: its entry and the hosts it holds.
type dst struct {
	entry int
	hosts []*Host[hostChild]
}

type ddec struct {
	kind int
	next dst
}

// durableHosts is one script's run: the hosts and children made, by whom.
type durableHosts struct {
	mu      sync.Mutex
	made    []*Host[hostChild]
	kids    []hostChild
	journal []dsig
	// marker is the incarnation the next first entry's children get.
	marker int
}

// step is the pure step: it makes e.fresh hosts on t.
func (d *durableHosts) step(t *rt.Task, s dst, e dsig) (ddec, error) {
	if e.boom {
		panic("boom: a step defect")
	}
	fresh := make([]*Host[hostChild], e.fresh)
	for i := range fresh {
		fresh[i] = NewHost[hostChild](t)
	}
	d.mu.Lock()
	d.made = append(d.made, fresh...)
	d.mu.Unlock()
	switch e.kind {
	case decideEnter:
		next := dst{entry: s.entry + 1, hosts: fresh}
		if e.carry {
			next.hosts = append(slices.Clone(fresh), s.hosts...)
		}
		return ddec{kind: decideEnter, next: next}, nil
	case decideStay:
		return ddec{kind: decideStay, next: dst{entry: s.entry, hosts: append(slices.Clone(s.hosts), fresh...)}}, nil
	}
	return ddec{kind: decideIgnore}, nil
}

// fold is the recover's replay of the journal, on t.
func (d *durableHosts) fold(t *rt.Task) dst {
	d.mu.Lock()
	journal := slices.Clone(d.journal)
	d.mu.Unlock()
	var s dst
	for _, e := range journal {
		dec, _ := d.step(t, s, e)
		s = dec.next
	}
	return s
}

// refFold is the reference: the journal's fold over counts.
func refFold(journal []dsig) (entry, hosts int) {
	for _, e := range journal {
		switch e.kind {
		case decideEnter:
			entry++
			if !e.carry {
				hosts = 0
			}
			hosts += e.fresh
		case decideStay:
			hosts += e.fresh
		}
	}
	return entry, hosts
}

// madeSince is the hosts made since n hosts were.
func (d *durableHosts) madeSince(n int) []*Host[hostChild] {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.made[n:])
}

func (d *durableHosts) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.made)
}

// start runs the durable actor in initial: a supervised one whose
// restarts fold the journal (SuperviseDurable's fresh), whose step keeps
// each committed signal in the journal (the save), and whose work fills
// each open host of its state with a child of the incarnation.
func (d *durableHosts) start(t *rt.Task, sl Sleeper, initial dst) *Actor[dst, dsig, ddec] {
	step := func(st *rt.Task, s dst, e dsig) (ddec, error) {
		dec, err := d.step(st, s, e)
		if err == nil && (dec.kind == decideEnter || dec.kind == decideStay) {
			d.mu.Lock()
			d.journal = append(d.journal, e)
			d.mu.Unlock()
		}
		return dec, err
	}
	fresh := func(ft *rt.Task) (dst, error) {
		d.mu.Lock()
		d.marker++
		d.mu.Unlock()
		return d.fold(ft), nil
	}
	work := func(wt *rt.Task, s dst, _ func(dsig)) {
		d.mu.Lock()
		marker := d.marker
		d.mu.Unlock()
		for _, h := range s.hosts {
			if h.peek() != hostOpen {
				continue // a carried host of an ended entry, or filled
			}
			c := MachineChild(Site("courier"), wt, sl, h, marker,
				func(*rt.Task, int, int) (int, error) { return decideIgnore, nil }, nil,
				func(int) time.Duration { return ActorNoTimer() },
				noWork[int], hasNoWork, runNoWork[int], func(d int) int { return d }, func(s, _ int) int { return s },
				func(*rt.Task, int, func(dsig)) {}, 1)
			d.mu.Lock()
			d.kids = append(d.kids, c)
			d.mu.Unlock()
		}
		_, _ = New[int](1).Pop(wt) // until the entry ends
	}
	return MachineSupervise(Site("durable"), t, sl, initial, fresh, 1000,
		step, nil, func(dst) time.Duration { return ActorNoTimer() },
		func(s dst) dst { return s }, func(dst) bool { return true }, work,
		func(dec ddec) int { return dec.kind },
		func(s dst, dec ddec) dst {
			if dec.kind == decideEnter || dec.kind == decideStay {
				return dec.next
			}
			return s
		},
		func(*rt.Task, ddec) {}, 4)
}

func TestDurableHostsRecover(t *testing.T) {
	seed := int64(20261111)
	if s := os.Getenv("ERGO_DURABLE_HOST_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	reached := map[string]bool{}
	kinds := []string{"held host filled", "replayed host closed", "carried host filled", "stay host filled",
		"restart host filled", "failed recover closed", "late host closed", "no actor closed"}
	for n := 0; ; n++ {
		all := true
		for _, k := range kinds {
			all = all && reached[k]
		}
		if n >= 200 && all {
			t.Logf("seed %d: %d scripts", seed, n)
			return
		}
		if n >= 5000 {
			t.Fatalf("seed %d: after %d scripts some kinds were never reached: %v", seed, n, reached)
		}
		durableScript(t, seed, n, rng, reached)
	}
}

// durableScript runs one script with its crashes and checks each recovery.
func durableScript(t *testing.T, seed int64, n int, rng *rand.Rand, reached map[string]bool) {
	t.Helper()
	root := rt.Root("durable hosts")
	sl := releaser{make(chan sleep)}
	d := &durableHosts{}
	var trace []string
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d script %d: %s\nreplay: ERGO_DURABLE_HOST_SEED=%d go test -run TestDurableHostsRecover ./runtime/v1\ntrace:\n  %s",
			seed, n, fmt.Sprintf(format, args...), seed, strings.Join(trace, "\n  "))
	}
	a := d.start(root, sl, dst{})
	var old []*Actor[dst, dsig, ddec] // stopped actors
	steps := 3 + rng.Intn(8)
	for i := 0; i < steps; i++ {
		switch r := rng.Intn(20); {
		case r < 3:
			trace = append(trace, fmt.Sprintf("%d: stop and recover (journal %d)", i, len(d.journal)))
			_ = a.Stop(root)
			old = append(old, a)
			d.noLeak(root, fail, "the stopped actor's")
			a = d.recoverAndCheck(root, sl, fail, reached)
		case r < 5:
			trace = append(trace, fmt.Sprintf("%d: a step defect, restart (journal %d)", i, len(d.journal)))
			before := d.count()
			_, _ = a.Ask(root, dsig{boom: true})
			// The crash answers a State queued before it counted its
			// inbox with the last state; one sent after that answer is
			// the new incarnation's, after its first entry opened.
			_ = a.State(root)
			s := a.State(root)
			d.checkRecovered(root, fail, reached, s, d.madeSince(before), "restart")
		case r < 6:
			trace = append(trace, fmt.Sprintf("%d: a failed recover", i))
			before := d.count()
			_, err := ActorRecovering(root, func(ft *rt.Task) (dst, error) {
				d.fold(ft)
				return dst{}, errors.New("unreadable")
			}, func(st *rt.Task, s dst) *Actor[dst, dsig, ddec] {
				fail("a failed recover started its actor")
				return nil
			})
			if err == nil {
				fail("ActorRecovering of a failed recover gave no failure")
			}
			for _, h := range d.madeSince(before) {
				if h.peek() != hostClosed {
					fail("a failed recover's host is %s, want closed", hostStateName[h.peek()])
				}
				reached["failed recover closed"] = true
			}
		case r < 7:
			trace = append(trace, fmt.Sprintf("%d: a late host and a start with no actor", i))
			d.lateAndNoActor(root, fail, reached)
		default:
			e := dsig{fresh: rng.Intn(3)}
			switch k := rng.Intn(10); {
			case k < 5:
				e.kind, e.carry = decideEnter, rng.Intn(3) == 0
			case k < 8:
				e.kind = decideStay
			default:
				e.kind = decideIgnore
			}
			trace = append(trace, fmt.Sprintf("%d: kind %d fresh %d carry %v", i, e.kind, e.fresh, e.carry))
			if _, err := a.Ask(root, e); err != nil {
				fail("the Ask failed %v", err)
			}
		}
	}
	_ = a.Stop(root)
	old = append(old, a)
	d.noLeak(root, fail, "the end's")
	for _, h := range d.madeSince(0) {
		if h.peek() != hostClosed {
			fail("after every actor ended a host is %s, want closed", hostStateName[h.peek()])
		}
	}
	for _, x := range old {
		if !isDone(x.done) {
			fail("a stopped actor runs")
		}
	}
}

// recoverAndCheck recovers as StartDurable does, and checks the hosts.
func (d *durableHosts) recoverAndCheck(root *rt.Task, sl Sleeper, fail func(string, ...any), reached map[string]bool) *Actor[dst, dsig, ddec] {
	before := d.count()
	a, err := ActorRecovering(root, func(ft *rt.Task) (dst, error) {
		d.mu.Lock()
		d.marker++
		d.mu.Unlock()
		return d.fold(ft), nil
	}, func(st *rt.Task, s dst) *Actor[dst, dsig, ddec] {
		return d.start(st, sl, s)
	})
	if err != nil {
		fail("ActorRecovering failed %v", err)
	}
	// The peek comes first: the start has returned, so each host the
	// recover made is bound and filled, or closed (dh1-first-publish-early).
	made := d.madeSince(before)
	states := make([]uint8, len(made))
	for i, h := range made {
		states[i] = h.peek()
	}
	s := a.State(root)
	d.checkPeeked(root, fail, reached, s, made, states, "recover")
	// Replay is deterministic: a second fold, with no recovery bound,
	// gives the same state; its hosts are closed at once.
	again := d.fold(root)
	if again.entry != s.entry || len(again.hosts) != len(s.hosts) {
		fail("a second replay gives entry %d with %d hosts, the recovered one entry %d with %d", again.entry, len(again.hosts), s.entry, len(s.hosts))
	}
	return a
}

// checkRecovered checks a restart's first state s and the hosts its fresh
// made.
func (d *durableHosts) checkRecovered(root *rt.Task, fail func(string, ...any), reached map[string]bool, s dst, made []*Host[hostChild], why string) {
	states := make([]uint8, len(made))
	for i, h := range made {
		states[i] = h.peek()
	}
	d.checkPeeked(root, fail, reached, s, made, states, why)
}

// checkPeeked holds the recovered state s to the reference, and each host
// the recover made (peeked as states when the start returned) to the rule.
func (d *durableHosts) checkPeeked(root *rt.Task, fail func(string, ...any), reached map[string]bool, s dst, made []*Host[hostChild], states []uint8, why string) {
	d.mu.Lock()
	journal := slices.Clone(d.journal)
	marker := d.marker
	d.mu.Unlock()
	entry, hosts := refFold(journal)
	if s.entry != entry || len(s.hosts) != hosts {
		fail("%s: the recovered state is entry %d with %d hosts, the fold of the journal entry %d with %d", why, s.entry, len(s.hosts), entry, hosts)
	}
	carried, stayed := map[*Host[hostChild]]bool{}, map[*Host[hostChild]]bool{}
	if len(journal) > 0 {
		// Which of the held hosts an earlier signal made: a carried or a
		// stayed one, for the kinds reached.
		last := journal[len(journal)-1]
		fresh := last.fresh
		for i, h := range s.hosts {
			if last.kind == decideEnter && i >= fresh {
				carried[h] = true
			}
			if last.kind == decideStay && i < len(s.hosts)-fresh {
				stayed[h] = true
			}
		}
	}
	for i, h := range made {
		if !slices.Contains(s.hosts, h) {
			if states[i] != hostClosed {
				fail("%s: a host only an earlier state of the replay held is %s, want closed", why, hostStateName[states[i]])
			}
			reached["replayed host closed"] = true
			continue
		}
		if states[i] != hostFilled {
			fail("%s: a host the recovered state holds is %s when the start returned, want filled by the first entry's work", why, hostStateName[states[i]])
		}
		c, err := h.Get(root)
		if err != nil {
			fail("%s: Get of a recovered host failed %v", why, err)
		}
		if got := c.State(root); got != marker {
			fail("%s: a recovered host gave a child of incarnation %d, want %d", why, got, marker)
		}
		reached["held host filled"] = true
		if why == "restart" {
			reached["restart host filled"] = true
		}
		if carried[h] {
			reached["carried host filled"] = true
		}
		if stayed[h] {
			reached["stay host filled"] = true
		}
	}
	for _, h := range s.hosts {
		if !slices.Contains(made, h) {
			fail("%s: the recovered state holds a host its recover did not make", why)
		}
	}
}

// lateAndNoActor runs a recover that starts a task which makes a host
// after the recover returned, and a start that opens no actor: each host
// is closed when ActorRecovering returns, or once made.
func (d *durableHosts) lateAndNoActor(root *rt.Task, fail func(string, ...any), reached map[string]bool) {
	before := d.count()
	release := make(chan struct{})
	var late *rt.Handle[*Host[hostChild]]
	holder := rt.NewScope(root)
	_, err := ActorRecovering(root, func(ft *rt.Task) (dst, error) {
		late = rt.Go(&holder, "late", func(lt *rt.Task) (*Host[hostChild], error) {
			<-release
			return NewHost[hostChild](lt), nil
		})
		return d.fold(ft), nil
	}, func(*rt.Task, dst) *Actor[dst, dsig, ddec] { return nil })
	if err != nil {
		fail("ActorRecovering failed %v", err)
	}
	for _, h := range d.madeSince(before) {
		if h.peek() != hostClosed {
			fail("a host of a start that opened no actor is %s, want closed", hostStateName[h.peek()])
		}
		reached["no actor closed"] = true
	}
	close(release)
	h, err := late.Wait()
	if err != nil {
		fail("the late task failed %v", err)
	}
	if h.peek() != hostClosed {
		fail("a host made after the recover returned is %s, want closed", hostStateName[h.peek()])
	}
	reached["late host closed"] = true
	var e error
	holder.End(&e)
}

// noLeak holds that every child started so far ended: the actor stopped.
func (d *durableHosts) noLeak(root *rt.Task, fail func(string, ...any), whose string) {
	d.mu.Lock()
	kids := slices.Clone(d.kids)
	d.mu.Unlock()
	for _, c := range kids {
		if !isDone(c.done) {
			fail("%s child runs after its actor stopped", whose)
		}
	}
}
