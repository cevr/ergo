package v1

import (
	"errors"
	"fmt"
	"hash/fnv"
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

// The children of a machine's entries (actor.go rule 13; runtime.md,
// Machines and actors 14) against a reference tree, the Quint model
// docs/research/actor-machine/children.qnt run on the real runner. Every
// actor of a case runs one machine: each entry's work starts the children
// a seeded plan gives that entry (down to depth 3, two kinds, so two
// children of one kind are common), reports that it started them, and
// waits for its interruption. Generated operations re-enter any live
// actor at random points, make a child Emit (a re-entry with an output to
// its parent) or end with Done, hold a parent in a step while its child's
// output is queued and then re-enter it (so the output is stale), defect
// a child, and stop the root. Waits wait for events, never for a
// wall-clock bound.
//
// Checked at every point: a work that ends (its entry ended) belongs to a
// child whose parent is still live and still in the entry that started it
// (ChildInsideEntry, ChildrenStopFirst); a parent's next entry finds the
// old entry's children ended; a stepped child output carries the entry it
// is stepped in (NoLateChildOutput). Checked against the reference: the
// order in which entries end (children last started first, each child's
// children first), each parent's count of stepped outputs and of dropped
// ones, that a Send to a child of an ended entry fails Stopped (an
// address acquired in one entry does not reach the next:
// AcquireSeesItsEntry), that a Changes loop on a child ends when the root
// ends (HostClosedEndsWaiters), and that a child's defect ends every
// ancestor and the root's Stop raises it.

// The signals of the children's machine.
const (
	kReenter = iota // Enter: a new entry
	kEmit           // Emit: a new entry, and an output to the parent
	kDone           // Done: an output to the parent, and the end
	kOut            // a child's output, as the parent's signal
	kHold           // the step waits for the test, then re-enters
	kBoom           // the step's defect
)

const childBoomText = "child boom"

// knode is an actor's state: its id (a path from the root), its entry
// count, the parent's entry that owns it, its depth and the outputs it
// stepped in this entry.
type knode struct {
	id     string
	entry  int
	pentry int
	depth  int
	got    int
}

type ksig struct {
	kind   int
	from   string
	pentry int
}

type kdec struct {
	k   int
	s   knode
	out ksig
}

type kactor = Actor[knode, ksig, kdec]

// kspec is one planned child: its kind (the site it starts at).
type kspec struct{ kind string }

// kdriver runs one case.
type kdriver struct {
	seed    int64
	sl      releaser
	held    chan struct{}
	release chan struct{}
	ready   chan string // "id@entry" once an entry's work started its children

	mu     sync.Mutex
	actors map[string]*kactor  // every started actor, by id
	owned  map[string][]string // "parent@entry" -> the children it started, in order
	opened map[string]int      // id -> the entry whose work started last
	parent map[string]string   // child id -> parent id
	log    []string            // "end id@entry", in order
	fail   string
}

func (dr *kdriver) failf(format string, args ...any) {
	dr.mu.Lock()
	if dr.fail == "" {
		dr.fail = fmt.Sprintf(format, args...)
	}
	dr.mu.Unlock()
}

// kidsFor is the plan of the entry: the children its work starts.
func kidsFor(seed int64, id string, entry, depth int) []kspec {
	if depth >= 3 {
		return nil
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%d %s %d", seed, id, entry)
	r := rand.New(rand.NewSource(int64(h.Sum64())))
	n := r.Intn(3)
	if depth == 0 {
		n++
	}
	out := make([]kspec, n)
	for i := range out {
		out[i] = kspec{kind: []string{"editor", "list"}[r.Intn(2)]}
	}
	return out
}

func childID(parent string, entry, i int) string { return fmt.Sprintf("%s/%d.%d", parent, entry, i) }

func (dr *kdriver) step(_ *rt.Task, s knode, e ksig) (kdec, error) {
	n := s
	n.entry, n.got = s.entry+1, 0
	switch e.kind {
	case kReenter:
		return kdec{k: decideEnter, s: n}, nil
	case kEmit:
		return kdec{k: decideEmit, s: n, out: ksig{kind: kOut, from: s.id, pentry: s.pentry}}, nil
	case kDone:
		return kdec{k: decideDone, out: ksig{kind: kOut, from: s.id, pentry: s.pentry}}, nil
	case kOut:
		if e.pentry != s.entry {
			dr.failf("%s stepped an output of %s for entry %d in entry %d", s.id, e.from, e.pentry, s.entry)
		}
		st := s
		st.got++
		return kdec{k: decideStay, s: st}, nil
	case kHold:
		dr.held <- struct{}{}
		<-dr.release
		return kdec{k: decideEnter, s: n}, nil
	default:
		panic(childBoomText)
	}
}

// work is every entry's work: it starts the planned children, says so,
// and waits for the end of its entry.
func (dr *kdriver) work(t *rt.Task, s knode, _ func(ksig)) {
	key := fmt.Sprintf("%s@%d", s.id, s.entry)
	dr.mu.Lock()
	// The old entry's children have ended before this one opens.
	for _, c := range dr.owned[fmt.Sprintf("%s@%d", s.id, s.entry-1)] {
		select {
		case <-dr.actors[c].done:
		default:
			dr.fail = cmpOr(dr.fail, fmt.Sprintf("entry %s opened while %s of the entry before lives", key, c))
		}
	}
	dr.opened[s.id] = s.entry
	dr.mu.Unlock()
	for i, sp := range kidsFor(dr.seed, s.id, s.entry, s.depth) {
		id := childID(s.id, s.entry, i)
		c := MachineChild(Site(sp.kind), t, dr.sl, knode{id: id, pentry: s.entry, depth: s.depth + 1},
			dr.step, nil, func(knode) time.Duration { return noTimer },
			func(s knode) knode { return s }, func(knode) bool { return true }, dr.work,
			func(d kdec) int { return d.k }, knext,
			func(_ *rt.Task, d kdec, deliver func(ksig)) {
				if d.k == decideEmit || d.k == decideDone {
					deliver(d.out)
				}
			}, 4)
		dr.mu.Lock()
		dr.actors[id] = c
		dr.owned[key] = append(dr.owned[key], id)
		dr.parent[id] = s.id
		dr.mu.Unlock()
	}
	dr.ready <- key
	<-t.Done()
	dr.mu.Lock()
	dr.log = append(dr.log, "end "+key)
	if p, ok := dr.parent[s.id]; ok && dr.actors[p] != nil {
		select {
		case <-dr.actors[p].done:
			dr.fail = cmpOr(dr.fail, fmt.Sprintf("%s ended after its parent %s", key, p))
		default:
		}
		if dr.opened[p] != s.pentry {
			dr.fail = cmpOr(dr.fail, fmt.Sprintf("%s (of %s's entry %d) ended in %s's entry %d", key, p, s.pentry, p, dr.opened[p]))
		}
	}
	dr.mu.Unlock()
	panic(rt.Interrupted)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func knext(s knode, d kdec) knode {
	switch d.k {
	case decideEnter, decideStay, decideEmit:
		return d.s
	}
	return s
}

// kref is the reference tree.
type kref struct {
	seed    int64
	entry   map[string]int
	depth   map[string]int
	alive   map[string]bool
	kids    map[string][]string // id -> the current entry's children, in start order
	parent  map[string]string
	got     map[string]int
	dropped map[string]int
	log     []string
	readies []string // the entries a step opens, whose work starts
	dead    []string // the actors a step ended
}

// open opens id's current entry: its work starts the planned children,
// whose first entries open too.
func (r *kref) open(id string) {
	e := r.entry[id]
	r.readies = append(r.readies, fmt.Sprintf("%s@%d", id, e))
	r.kids[id] = nil
	for i := range kidsFor(r.seed, id, e, r.depth[id]) {
		c := childID(id, e, i)
		r.entry[c], r.depth[c], r.alive[c], r.parent[c] = 0, r.depth[id]+1, true, id
		r.kids[id] = append(r.kids[id], c)
		r.open(c)
	}
}

// exit ends id's current entry: its live children end, last started
// first, then its work.
func (r *kref) exit(id string) {
	ks := r.kids[id]
	for i := len(ks) - 1; i >= 0; i-- {
		if r.alive[ks[i]] {
			r.end(ks[i])
		}
	}
	r.log = append(r.log, fmt.Sprintf("end %s@%d", id, r.entry[id]))
}

func (r *kref) end(id string) {
	r.exit(id)
	r.alive[id] = false
	r.dead = append(r.dead, id)
}

func (r *kref) reenter(id string) {
	r.exit(id)
	r.entry[id]++
	r.got[id] = 0
	r.open(id)
}

func (r *kref) live(nonRoot bool) []string {
	var out []string
	for id, ok := range r.alive {
		if ok && (!nonRoot || id != "r") {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// kop is one generated operation.
type kop struct {
	kind string // reenter, emit, done, hold, boom, watch
	id   string
}

// runChildrenCase runs one case of n operations; it returns what broke.
func runChildrenCase(seed int64, r *rand.Rand, n int, seen map[string]bool) (ops []kop, why string) {
	task := rt.Root("children driver")
	dr := &kdriver{
		seed: seed, sl: releaser{make(chan sleep)}, held: make(chan struct{}), release: make(chan struct{}),
		ready: make(chan string, 1024), actors: map[string]*kactor{}, owned: map[string][]string{},
		opened: map[string]int{}, parent: map[string]string{},
	}
	ref := &kref{seed: seed, entry: map[string]int{"r": 0}, depth: map[string]int{"r": 0}, alive: map[string]bool{"r": true},
		kids: map[string][]string{}, parent: map[string]string{}, got: map[string]int{}, dropped: map[string]int{}}
	root := MachineStart(Site("root"), task, dr.sl, knode{id: "r"}, dr.step, nil,
		func(knode) time.Duration { return noTimer }, func(s knode) knode { return s }, func(knode) bool { return true },
		dr.work, func(d kdec) int { return d.k }, knext, func(*rt.Task, kdec) {}, 4)
	dr.mu.Lock()
	dr.actors["r"] = root
	dr.mu.Unlock()
	ref.open("r")
	var waiters []chan struct{}
	defer func() {
		// Leave nothing running, also after a failure.
		dr.mu.Lock()
		all := make([]*kactor, 0, len(dr.actors))
		for _, a := range dr.actors {
			all = append(all, a)
		}
		dr.mu.Unlock()
		for _, a := range all {
			_ = a.halt()
		}
		for _, w := range waiters {
			<-w
		}
	}()
	actor := func(id string) *kactor {
		dr.mu.Lock()
		defer dr.mu.Unlock()
		return dr.actors[id]
	}
	// settle waits for the entries the reference opened, then compares.
	settle := func(what string) string {
		got := []string{}
		for range ref.readies {
			got = append(got, <-dr.ready)
		}
		slices.Sort(got)
		want := slices.Clone(ref.readies)
		slices.Sort(want)
		ref.readies = nil
		if !slices.Equal(got, want) {
			return fmt.Sprintf("%s: entries opened %v, want %v", what, got, want)
		}
		dr.mu.Lock()
		log, fail := slices.Clone(dr.log), dr.fail
		dr.mu.Unlock()
		if fail != "" {
			return what + ": " + fail
		}
		if !slices.Equal(log, ref.log) {
			return fmt.Sprintf("%s: ends\n  %v\nwant\n  %v", what, log, ref.log)
		}
		for _, id := range ref.dead {
			a := actor(id)
			select {
			case <-a.done:
			default:
				return fmt.Sprintf("%s: %s lives after its end", what, id)
			}
			if err := a.Send(task, ksig{kind: kReenter}); !errors.As(err, new(ActorStopped)) {
				return fmt.Sprintf("%s: a Send to %s of an ended entry gave %v, want Stopped", what, id, err)
			}
		}
		ref.dead = nil
		return ""
	}
	// counts reads p's state through its inbox: every output queued before
	// is stepped or dropped by then.
	counts := func(p string) string {
		a := actor(p)
		s := a.State(task)
		if s.got != ref.got[p] || a.Dropped() != ref.dropped[p] {
			return fmt.Sprintf("%s stepped %d outputs and dropped %d, want %d and %d", p, s.got, a.Dropped(), ref.got[p], ref.dropped[p])
		}
		return ""
	}
	if why := settle("start"); why != "" {
		return nil, why
	}
	for range n {
		live := ref.live(false)
		kids := ref.live(true)
		var o kop
		switch k := r.Intn(10); {
		case k < 3 || len(kids) == 0:
			o = kop{kind: "reenter", id: live[r.Intn(len(live))]}
		case k < 5:
			o = kop{kind: "emit", id: kids[r.Intn(len(kids))]}
		case k < 6:
			o = kop{kind: "done", id: kids[r.Intn(len(kids))]}
		case k < 8:
			o = kop{kind: "hold", id: kids[r.Intn(len(kids))]}
		default:
			o = kop{kind: "watch", id: kids[r.Intn(len(kids))]}
		}
		ops = append(ops, o)
		switch o.kind {
		case "reenter":
			if _, err := actor(o.id).Ask(task, ksig{kind: kReenter}); err != nil {
				return ops, fmt.Sprintf("reenter %s: %v", o.id, err)
			}
			ref.reenter(o.id)
			seen["reenter"] = true
			if len(ref.dead) > 0 {
				seen["a re-entry stopped children"] = true
			}
		case "emit", "done":
			p := ref.parent[o.id]
			kind := kEmit
			if o.kind == "done" {
				kind = kDone
			}
			c := actor(o.id)
			if _, err := c.Ask(task, ksig{kind: kind}); err != nil {
				return ops, fmt.Sprintf("%s %s: %v", o.kind, o.id, err)
			}
			if o.kind == "emit" {
				ref.reenter(o.id)
			} else {
				<-c.done
				ref.end(o.id)
				seen["a child ended by Done"] = true
			}
			ref.got[p]++
			if why := counts(p); why != "" {
				return ops, fmt.Sprintf("%s %s: %s", o.kind, o.id, why)
			}
		case "hold":
			// The parent is held in a step that re-enters it; the child's
			// output is queued behind it, so it is stale when stepped.
			p := ref.parent[o.id]
			if err := actor(p).Send(task, ksig{kind: kHold}); err != nil {
				return ops, fmt.Sprintf("hold %s: %v", p, err)
			}
			<-dr.held
			if _, err := actor(o.id).Ask(task, ksig{kind: kEmit}); err != nil {
				return ops, fmt.Sprintf("emit %s under a held parent: %v", o.id, err)
			}
			ref.reenter(o.id)
			// The child's new entry starts its children before the parent's
			// re-entry ends it, so the reference's plan holds.
			if why := settle(fmt.Sprintf("emit %s under a held parent", o.id)); why != "" {
				dr.release <- struct{}{}
				return ops, why
			}
			dr.release <- struct{}{}
			ref.reenter(p)
			ref.dropped[p]++
			if why := counts(p); why != "" {
				return ops, fmt.Sprintf("hold %s: %s", o.id, why)
			}
			seen["a stale child output"] = true
		case "watch":
			// A Changes loop on a child: it ends when the child ends.
			w := make(chan struct{})
			waiters = append(waiters, w)
			c := actor(o.id)
			go func() {
				defer close(w)
				for range c.Changes(rt.Root("watcher")) {
				}
			}()
		}
		if why := settle(fmt.Sprintf("%s %s", o.kind, o.id)); why != "" {
			return ops, why
		}
		for id, ok := range ref.alive {
			if ok && ref.depth[id] == 3 {
				seen["depth 3"] = true
			}
		}
	}
	// The end: a child's defect, which ends every ancestor, or a Stop.
	if kids := ref.live(true); len(kids) > 0 && r.Intn(3) == 0 {
		id := kids[r.Intn(len(kids))]
		ops = append(ops, kop{kind: "boom", id: id})
		_ = actor(id).Send(task, ksig{kind: kBoom})
		// The child, then each ancestor up to the root, ends. A child pushes
		// its defect to its parent before its done closes, so an Ask sent
		// to the parent after that is queued behind the defect and fails
		// Stopped; an Ask that a parent steps proves the defect was lost
		// (no wait on the root that such a loss would hang). The probe is
		// an output that Stays, so it stops no child (a re-entry's stop of
		// the dead child would raise the defect itself).
		for x := id; x != ""; x = ref.parent[x] {
			<-actor(x).done
			ref.end(x)
			if p := ref.parent[x]; p != "" {
				if _, err := actor(p).Ask(task, ksig{kind: kOut, from: "probe", pentry: -1}); !errors.As(err, new(ActorStopped)) {
					return ops, fmt.Sprintf("boom %s: %s answered %v after its child %s ended by a defect, want Stopped", id, p, err, x)
				}
			}
		}
		var raised any
		func() {
			defer func() { raised = recover() }()
			_ = root.Stop()
		}()
		var text strings.Builder
		if x, ok := raised.(*exit); ok {
			report(&text, x.err)
		}
		if !strings.Contains(text.String(), childBoomText) {
			return ops, fmt.Sprintf("boom %s: the root's Stop raised %v, want the child's defect", id, raised)
		}
		seen["a child's defect ended the root"] = true
	} else {
		ops = append(ops, kop{kind: "stop", id: "r"})
		if err := root.Stop(); err != nil {
			return ops, fmt.Sprintf("stop: %v", err)
		}
		ref.end("r")
	}
	if why := settle("end"); why != "" {
		return ops, why
	}
	for _, w := range waiters {
		<-w
	}
	waiters = nil
	return ops, ""
}

// TestActorChildrenAgreeWithTree runs generated cases (seed 1, or
// ERGO_ACTOR_SEED) until each kind below is reached, and at least 200. A
// failure prints the seed, the case and what broke.
func TestActorChildrenAgreeWithTree(t *testing.T) {
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
	want := []string{"reenter", "a re-entry stopped children", "a child ended by Done", "a stale child output",
		"depth 3", "a child's defect ended the root"}
	n := 0
	for ; n < 200 || slices.ContainsFunc(want, func(w string) bool { return !seen[w] }); n++ {
		if n == 5000 {
			for _, w := range want {
				if !seen[w] {
					t.Fatalf("seed %d: %d cases reach no %q", seed, n, w)
				}
			}
		}
		caseSeed := seed*100003 + int64(n)
		ops, why := runChildrenCase(caseSeed, r, 1+r.Intn(8), seen)
		if why != "" {
			t.Fatalf("seed %d, case %d (plan seed %d): %s\nops %+v", seed, n, caseSeed, why, ops)
		}
	}
	t.Logf("seed %d: %d cases", seed, n)
}
