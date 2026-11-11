package v1

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestManualTreeSettles holds runtime.md, Machines and actors 11.16 for
// trees of children (rule 13) on a time.Manual (wf10 MK1): every wait of a
// work and of an entry's stop is one the clock sees, so Advance returns,
// and the trees step as a reference timed tree does.
//
// Each case is a generated tree of machine actors on one manual clock. The
// root has no timer; each entry's work starts a planned set of children
// (depth 2 at most) and then either returns, so it waits for its children
// in waitKids, or sleeps until its entry ends. A child's state timer fires
// an Emit (an output to its parent, and a new entry of the child, which
// stops the children of the old one) or a Done (an output, and the end).
// A parent counts the outputs of its entry's children. The test's task,
// the clock's first member, sends re-entries to live actors and Advances
// the clock; after each Advance it reads every actor's state through its
// inbox and holds it, the clock's time and the set of ended actors against
// the reference. The case ends with a Stop of the root by a member task
// while the works release slowly: a work that the stop interrupts sleeps
// on the clock before it ends, so each entry's stop of a child waits for
// time to pass; the test's task Advances by 1 until the Stop returns.
//
// Two timers due at one instant fire together, in an order the scheduler
// chooses, so a case whose reference meets such a tie is drawn again.
// Liveness is bounded by the test's deadline (testwait), which names the
// case and the Advance that did not return. A failure prints the replay
// command. Mutants (runtime/v1 ledger): mk1-waitkids-unparked (a work's
// wait for its children is a raw receive; the downloads manager's
// deadlock) and mk1-halt-unparked (an entry's stop of a child waits for the
// child's end unparked).
func TestManualTreeSettles(t *testing.T) {
	base := uint64(1)
	if s := os.Getenv("ERGO_TREE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		base = n
	}
	var where atomic.Value
	where.Store("before the first case")
	stop := testwait.AfterFunc(t, func() {
		panic(fmt.Sprintf("TestManualTreeSettles: an Advance or the final Stop did not return by the test's deadline: %s (replay: ERGO_TREE_SEED=%d go test -run TestManualTreeSettles ./runtime/v1)", where.Load(), base))
	})
	defer stop()
	seen := map[string]bool{}
	want := []string{"a timer fired while a returned work waited for its children", "an Emit stopped children",
		"a Done ended a child", "a sent re-entry", "depth 2", "a stop waited for a release"}
	cases, ties := 0, 0
	for n := uint64(0); cases < 200 || slices.ContainsFunc(want, func(w string) bool { return !seen[w] }); n++ {
		if n == 20000 {
			for _, w := range want {
				if !seen[w] {
					t.Fatalf("seed %d: %d cases reach no %q", base, cases, w)
				}
			}
		}
		seed := base*100003 + n
		trace, tied, why := treeCase(seed, seen, &where)
		if tied {
			ties++
			continue
		}
		cases++
		if why != "" {
			t.Fatalf("case seed %d: %s\ntrace %v\nreplay: ERGO_TREE_SEED=%d go test -run TestManualTreeSettles ./runtime/v1", seed, why, trace, base)
		}
	}
	t.Logf("seed %d: %d cases, %d drawn again for a tie", base, cases, ties)
}

// The signals of the tree's machine.
const (
	mtReenter = iota // the test's: a new entry
	mtOut            // a child's output, as its parent's signal
)

// mtNode is an actor's state: its id (a path from the root), its entry
// count, its depth, and the outputs of its entry's children it counted.
type mtNode struct {
	id    string
	entry int
	depth int
	got   int
}

type mtSig struct{ kind int }

type mtDec struct {
	k int
	s mtNode
}

type mtActor = Actor[mtNode, mtSig, mtDec]

// mtPlan is what an entry does: the children its work starts, whether
// the work returns after it started them (or sleeps until its entry
// ends), how long its stop's release sleeps once the final stop began,
// and, for a child, its timer and whether the timer's fire is a Done.
type mtPlan struct {
	kids   int
	ret    bool
	rel    int
	delay  int
	isDone bool
}

func mtPlanOf(seed uint64, id string, entry, depth int) mtPlan {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d %s %d", seed, id, entry)
	r := rand.New(rand.NewPCG(h.Sum64(), 7))
	p := mtPlan{ret: r.IntN(2) == 0, rel: r.IntN(4)}
	switch depth {
	case 0:
		p.kids = 1 + r.IntN(2)
	case 1:
		p.kids = r.IntN(3)
	}
	if depth > 0 {
		p.delay = 1 + r.IntN(30)
		p.isDone = r.IntN(4) == 0
	}
	return p
}

// mtDriver runs the actors of one case.
type mtDriver struct {
	seed  uint64
	line  *Timeline
	final atomic.Bool // the final stop began: a stopped work releases slowly

	mu     sync.Mutex
	actors map[string]*mtActor
}

func (d *mtDriver) step(_ *rt.Task, s mtNode, e mtSig) (mtDec, error) {
	if e.kind == mtOut {
		s.got++
		return mtDec{k: decideStay, s: s}, nil
	}
	s.entry, s.got = s.entry+1, 0
	return mtDec{k: decideEnter, s: s}, nil
}

func (d *mtDriver) fire(_ *rt.Task, at mtNode, s mtNode) (mtDec, error) {
	if mtPlanOf(d.seed, at.id, at.entry, at.depth).isDone {
		return mtDec{k: decideDone}, nil
	}
	s.entry, s.got = s.entry+1, 0
	return mtDec{k: decideEmit, s: s}, nil
}

func (d *mtDriver) delay(s mtNode) time.Duration {
	if s.depth == 0 {
		return noTimer
	}
	return time.Duration(mtPlanOf(d.seed, s.id, s.entry, s.depth).delay)
}

// forever is longer than any case's time: a sleep its entry's end stops.
const forever = time.Duration(1) << 50

func (d *mtDriver) work(t *rt.Task, s mtNode, _ func(mtSig)) {
	p := mtPlanOf(d.seed, s.id, s.entry, s.depth)
	defer func() {
		if p.rel > 0 && d.final.Load() {
			m := t.Mask()
			d.line.Wait(t, time.Duration(p.rel))
			m.Unmask()
		}
	}()
	for i := range p.kids {
		id := fmt.Sprintf("%s/%d.%d", s.id, s.entry, i)
		c := MachineChild(Site("tree"), t, lineSleeper{d.line}, nil, mtNode{id: id, depth: s.depth + 1},
			d.step, d.fire, d.delay, func(s mtNode) mtNode { return s }, func(mtNode) bool { return true }, d.work,
			func(x mtDec) int { return x.k }, mtNext,
			func(_ *rt.Task, x mtDec, deliver func(mtSig)) {
				if x.k == decideEmit || x.k == decideDone {
					deliver(mtSig{kind: mtOut})
				}
			}, 4)
		d.mu.Lock()
		d.actors[id] = c
		d.mu.Unlock()
	}
	if !p.ret {
		d.line.Wait(t, forever)
	}
}

func mtNext(s mtNode, x mtDec) mtNode {
	switch x.k {
	case decideEnter, decideStay, decideEmit:
		return x.s
	}
	return s
}

// mtRef is the reference timed tree.
type mtRef struct {
	seed   uint64
	now    int
	alive  map[string]bool
	node   map[string]*mtNode
	parent map[string]string
	kids   map[string][]string // the current entry's children, in start order
	due    map[string]int      // the armed timers
	seen   map[string]bool
}

func (r *mtRef) open(id string) {
	n := r.node[id]
	p := mtPlanOf(r.seed, id, n.entry, n.depth)
	if n.depth > 0 {
		r.due[id] = r.now + p.delay
	}
	r.kids[id] = nil
	for i := range p.kids {
		c := fmt.Sprintf("%s/%d.%d", id, n.entry, i)
		r.node[c] = &mtNode{id: c, depth: n.depth + 1}
		r.alive[c], r.parent[c] = true, id
		r.kids[id] = append(r.kids[id], c)
		if n.depth+1 == 2 {
			r.seen["depth 2"] = true
		}
		r.open(c)
	}
}

// exit ends id's entry: its live children end, last started first.
func (r *mtRef) exit(id string) {
	ks := r.kids[id]
	for i := len(ks) - 1; i >= 0; i-- {
		if r.alive[ks[i]] {
			r.end(ks[i])
		}
	}
	delete(r.due, id)
}

func (r *mtRef) end(id string) {
	r.exit(id)
	r.alive[id] = false
}

func (r *mtRef) reenter(id string) {
	for _, k := range r.kids[id] {
		if r.alive[k] {
			r.seen["an Emit stopped children"] = true
		}
	}
	r.exit(id)
	n := r.node[id]
	n.entry, n.got = n.entry+1, 0
	r.open(id)
}

// waitingWork reports whether a live actor's entry has a work that
// returned and waits for its live children.
func (r *mtRef) waitingWork() bool {
	for id, ok := range r.alive {
		n := r.node[id]
		if ok && mtPlanOf(r.seed, id, n.entry, n.depth).ret && slices.ContainsFunc(r.kids[id], func(k string) bool { return r.alive[k] }) {
			return true
		}
	}
	return false
}

// advance fires each timer due by target at its own instant; false when
// two are due at one instant.
func (r *mtRef) advance(target int) bool {
	for {
		first, at, n := "", 0, 0
		for id, when := range r.due {
			switch {
			case first == "" || when < at:
				first, at, n = id, when, 1
			case when == at:
				n++
			}
		}
		if first == "" || at > target {
			r.now = target
			return true
		}
		if n > 1 {
			return false
		}
		if r.waitingWork() {
			r.seen["a timer fired while a returned work waited for its children"] = true
		}
		r.now = at
		delete(r.due, first)
		n0 := r.node[first]
		r.node[r.parent[first]].got++
		if mtPlanOf(r.seed, first, n0.entry, n0.depth).isDone {
			r.seen["a Done ended a child"] = true
			r.end(first)
		} else {
			r.reenter(first)
		}
	}
}

func (r *mtRef) live() []string {
	var out []string
	for id, ok := range r.alive {
		if ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// treeCase runs one case: its trace, whether the reference met a tie (the
// case is drawn again), and what broke.
func treeCase(seed uint64, seen map[string]bool, where *atomic.Value) (trace []string, tied bool, why string) {
	g := rand.New(rand.NewPCG(seed, 23))
	// The reference first: a case with a tie is drawn again before it runs.
	type op struct {
		advance int
		reenter string
	}
	ref := &mtRef{seed: seed, alive: map[string]bool{"r": true}, node: map[string]*mtNode{"r": {id: "r"}},
		parent: map[string]string{}, kids: map[string][]string{}, due: map[string]int{}, seen: map[string]bool{}}
	ref.open("r")
	var ops []op
	for range 4 + g.IntN(10) {
		if g.IntN(3) == 0 {
			live := ref.live()
			id := live[g.IntN(len(live))]
			ops = append(ops, op{reenter: id})
			ref.reenter(id)
			continue
		}
		d := g.IntN(40)
		ops = append(ops, op{advance: d})
		if !ref.advance(ref.now + d) {
			return nil, true, ""
		}
	}

	// Then the run, against a fresh reference.
	root := rt.Root("tree")
	dr := &mtDriver{seed: seed, line: NewTimeline(root, 0, 0), actors: map[string]*mtActor{}}
	ref = &mtRef{seed: seed, alive: map[string]bool{"r": true}, node: map[string]*mtNode{"r": {id: "r"}},
		parent: map[string]string{}, kids: map[string][]string{}, due: map[string]int{}, seen: seen}
	move := func(d int) {
		where.Store(fmt.Sprintf("case seed %d, Advance(%d) after %v", seed, d, trace))
		dr.line.Advance(root, time.Duration(d))
	}
	a := MachineStart(Site("tree"), root, lineSleeper{dr.line}, mtNode{id: "r"}, dr.step, dr.fire, dr.delay,
		func(s mtNode) mtNode { return s }, func(mtNode) bool { return true }, dr.work,
		func(x mtDec) int { return x.k }, mtNext, func(*rt.Task, mtDec) {}, 4)
	dr.mu.Lock()
	dr.actors["r"] = a
	dr.mu.Unlock()
	ref.open("r")
	check := func() string {
		if got := dr.line.Read().Mono; got != int64(ref.now) {
			return fmt.Sprintf("the clock reads %d, the reference %d", got, ref.now)
		}
		dr.mu.Lock()
		actors := make(map[string]*mtActor, len(dr.actors))
		for id, x := range dr.actors {
			actors[id] = x
		}
		dr.mu.Unlock()
		for id, x := range actors {
			if !ref.alive[id] && !isDone(x.done) {
				return fmt.Sprintf("%s lives; the reference ended it", id)
			}
		}
		for _, id := range ref.live() {
			x := actors[id]
			if x == nil {
				return fmt.Sprintf("%s never started; the reference started it", id)
			}
			if got, want := x.State(root), *ref.node[id]; got != want {
				return fmt.Sprintf("%s shows %+v, the reference %+v", id, got, want)
			}
		}
		return ""
	}
	move(0)
	if why := check(); why != "" {
		return trace, false, "at the start: " + why
	}
	for _, o := range ops {
		if o.reenter != "" {
			trace = append(trace, "reenter "+o.reenter)
			dr.mu.Lock()
			x := dr.actors[o.reenter]
			dr.mu.Unlock()
			if err := x.Send(root, mtSig{kind: mtReenter}); err != nil {
				return trace, false, fmt.Sprintf("Send to %s: %v", o.reenter, err)
			}
			ref.reenter(o.reenter)
			ref.seen["a sent re-entry"] = true
			move(0)
		} else {
			trace = append(trace, fmt.Sprintf("advance %d", o.advance))
			ref.advance(ref.now + o.advance)
			move(o.advance)
		}
		if why := check(); why != "" {
			return trace, false, why
		}
	}

	// The final stop: each stopped work that sleeps until its entry ends
	// releases slowly now, so the root's stop of its children waits for
	// the clock. A member task stops the root; the test's task advances.
	for id, ok := range ref.alive {
		n := ref.node[id]
		if p := mtPlanOf(seed, id, n.entry, n.depth); ok && !p.ret && p.rel > 0 && id != "r" {
			seen["a stop waited for a release"] = true
		}
	}
	dr.final.Store(true)
	trace = append(trace, "stop")
	var stopped atomic.Bool
	holder := rt.NewScope(root)
	h := rt.Go(&holder, "stopper", func(ct *rt.Task) (struct{}, error) {
		err := a.Stop(ct)
		stopped.Store(true)
		return struct{}{}, err
	})
	for n := 0; !stopped.Load(); n++ {
		// Each Advance(1) ends at least one release or timer that holds
		// the stop; far fewer than this many hold it.
		if n == 100000 {
			return trace, false, "the root's Stop did not return after 100000 Advances"
		}
		move(1)
	}
	if err := h.Stop(); err != nil {
		return trace, false, fmt.Sprintf("the root's Stop: %v", err)
	}
	dr.mu.Lock()
	defer dr.mu.Unlock()
	for id, x := range dr.actors {
		if !isDone(x.done) {
			return trace, false, fmt.Sprintf("%s lives after the root's Stop", id)
		}
	}
	return trace, false, ""
}
