package sched

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

// The value executor: a small program of tasks on queues, inboxes and
// Refs, run step by step as the explored runtime runs one (runtime.md,
// Explored schedules): the root task "0" starts its children, does its own
// operations and waits for each child; a child's first step is its start
// and its last its end; a push to a full queue, a pop of an empty one and
// a wait for a running child park the task until the event comes, and its
// next step is then a resume. Handles are numbered in the order the run
// first names them, as rt numbers them. The tests compare the search with
// a naive enumeration on it, in process, with no Go build.

// mop is one operation of a program: a push of v, a pop, a close, a read
// or a write of v, on handle h (queues first, then Refs).
type mop struct {
	kind string
	h, v int
}

// prog is a generated program.
type prog struct {
	caps []int // each queue's capacity; 0 is an unbounded inbox
	refs int
	root []mop
	kids [][]mop
}

func (p prog) handleName(h int) string {
	if h < len(p.caps) {
		if p.caps[h] == 0 {
			return "inbox" + strconv.Itoa(h)
		}
		return fmt.Sprintf("queue%d(cap %d)", h, p.caps[h])
	}
	return "ref" + strconv.Itoa(h-len(p.caps))
}

func (p prog) String() string {
	var b strings.Builder
	words := func(ops []mop) string {
		var w []string
		for _, o := range ops {
			s := o.kind + " " + p.handleName(o.h)
			if o.kind == "push" || o.kind == "write" {
				s += " " + strconv.Itoa(o.v)
			}
			w = append(w, s)
		}
		return "[" + strings.Join(w, "; ") + "]"
	}
	fmt.Fprintf(&b, "root %s", words(p.root))
	for i, k := range p.kids {
		fmt.Fprintf(&b, "; task 0.%d %s", i, words(k))
	}
	return b.String()
}

// genProg draws a program of 2 to 4 tasks, each with up to maxOps
// operations, on up to 3 handles (at least one queue).
func genProg(r *rand.Rand, maxOps int) prog {
	var p prog
	for range 1 + r.IntN(2) {
		p.caps = append(p.caps, r.IntN(3))
	}
	p.refs = r.IntN(2)
	n := len(p.caps) + p.refs
	v := 0
	op := func() mop {
		h := r.IntN(n)
		v++
		if h >= len(p.caps) {
			if r.IntN(2) == 0 {
				return mop{"read", h, 0}
			}
			return mop{"write", h, v}
		}
		switch x := r.IntN(10); {
		case x < 5:
			return mop{"push", h, v}
		case x < 9:
			return mop{"pop", h, 0}
		default:
			return mop{"close", h, 0}
		}
	}
	// More tasks get fewer operations each, so the naive reference ends.
	kids := 1 + r.IntN(3)
	most := max(1, maxOps+1-kids)
	for range kids {
		var ops []mop
		for range 1 + r.IntN(most) {
			ops = append(ops, op())
		}
		p.kids = append(p.kids, ops)
	}
	if r.IntN(2) == 0 {
		p.root = []mop{op()}
	}
	return p
}

// instr is one step of a task's plan: start, spawn (of kid), an operation,
// join (of kid), end.
type instr struct {
	kind string
	op   mop
	kid  int
}

type vtask struct {
	path   string
	plan   []instr
	pc     int
	parked bool
	done   bool
	log    []string
	kids   []int // the task indexes of the children it started
}

type vqueue struct {
	vals   []int
	cap    int
	closed bool
}

type vstate struct {
	p       prog
	tasks   []*vtask
	queues  []*vqueue
	refs    []int
	handles map[string]int
}

func newState(p prog) *vstate {
	s := &vstate{p: p, refs: make([]int, p.refs), handles: map[string]int{}}
	for _, c := range p.caps {
		s.queues = append(s.queues, &vqueue{cap: c})
	}
	root := &vtask{path: "0"}
	for i := range p.kids {
		root.plan = append(root.plan, instr{kind: "spawn", kid: i})
	}
	for _, o := range p.root {
		root.plan = append(root.plan, instr{kind: o.kind, op: o})
	}
	for i := range p.kids {
		root.plan = append(root.plan, instr{kind: "join", kid: i})
	}
	root.plan = append(root.plan, instr{kind: "end"})
	s.tasks = []*vtask{root}
	return s
}

// handle numbers the thing a step touches, in the order the run names it.
func (s *vstate) handle(key string) int {
	n, ok := s.handles[key]
	if !ok {
		n = len(s.handles) + 1
		s.handles[key] = n
	}
	return n
}

// on is the key of what t's next step touches.
func (s *vstate) on(t *vtask) string {
	in := t.plan[t.pc]
	switch in.kind {
	case "start", "end":
		return "t" + t.path
	case "spawn":
		return "t" + t.path
	case "join":
		return "t" + t.path + "." + strconv.Itoa(in.kid)
	}
	if in.op.h < len(s.queues) {
		return "q" + strconv.Itoa(in.op.h)
	}
	return "r" + strconv.Itoa(in.op.h-len(s.queues))
}

func (s *vstate) pending(t *vtask) Step {
	kind := t.plan[t.pc].kind
	if t.parked {
		kind = "resume"
	}
	return Step{Task: t.path, Kind: kind, Handle: s.handle(s.on(t))}
}

// ready reports whether t's current instruction can complete now.
func (s *vstate) ready(t *vtask) bool {
	in := t.plan[t.pc]
	switch in.kind {
	case "push":
		q := s.queues[in.op.h]
		return q.closed || q.cap == 0 || len(q.vals) < q.cap
	case "pop":
		q := s.queues[in.op.h]
		return q.closed || len(q.vals) > 0
	case "join":
		return s.tasks[t.kids[in.kid]].done
	}
	return true
}

func (s *vstate) enabled(t *vtask) bool { return !t.done && (!t.parked || s.ready(t)) }

// exec runs t's next step: its instruction, or the resume of a parked
// one; a step that cannot complete parks t.
func (s *vstate) exec(t *vtask) {
	if !s.ready(t) {
		t.parked = true
		return
	}
	t.parked = false
	in := t.plan[t.pc]
	switch in.kind {
	case "spawn":
		kid := &vtask{path: t.path + "." + strconv.Itoa(len(t.kids))}
		kid.plan = append(kid.plan, instr{kind: "start"})
		for _, o := range s.p.kids[in.kid] {
			kid.plan = append(kid.plan, instr{kind: o.kind, op: o})
		}
		kid.plan = append(kid.plan, instr{kind: "end"})
		t.kids = append(t.kids, len(s.tasks))
		s.tasks = append(s.tasks, kid)
	case "push":
		q := s.queues[in.op.h]
		if q.closed {
			t.log = append(t.log, "push closed")
		} else {
			q.vals = append(q.vals, in.op.v)
		}
	case "pop":
		q := s.queues[in.op.h]
		if len(q.vals) > 0 {
			t.log = append(t.log, "pop "+strconv.Itoa(q.vals[0]))
			q.vals = q.vals[1:]
		} else {
			t.log = append(t.log, "pop closed")
		}
	case "close":
		s.queues[in.op.h].closed = true
	case "read":
		t.log = append(t.log, "read "+strconv.Itoa(s.refs[in.op.h-len(s.queues)]))
	case "write":
		s.refs[in.op.h-len(s.queues)] = in.op.v
	case "end":
		t.done = true
		return
	}
	t.pc++
}

// value is the run's observable result: the handles, each task's log, and
// the tasks a deadlock left.
func (s *vstate) value() string {
	var b strings.Builder
	for i, q := range s.queues {
		fmt.Fprintf(&b, "q%d=%v closed=%v; ", i, q.vals, q.closed)
	}
	fmt.Fprintf(&b, "refs=%v; ", s.refs)
	for _, t := range s.tasks {
		fmt.Fprintf(&b, "%s%v", t.path, t.log)
		if !t.done {
			b.WriteString("(waits)")
		}
		b.WriteString(" ")
	}
	return b.String()
}

// vexec is the value executor of one program.
type vexec struct{ p prog }

func (x vexec) Run(pk Picker) Outcome {
	s := newState(x.p)
	cur := s.tasks[0]
	for step := 0; ; step++ {
		c := Choice{Current: -1, Step: step}
		var cands []*vtask
		for _, t := range s.tasks {
			if s.enabled(t) {
				if t == cur {
					c.Current = len(cands)
				}
				cands = append(cands, t)
				c.Enabled = append(c.Enabled, s.pending(t))
			}
		}
		if len(cands) == 0 {
			o := Outcome{Value: s.value()}
			for _, t := range s.tasks {
				if !t.done {
					o.Deadlock = append(o.Deadlock, s.pending(t))
				}
			}
			if o.Deadlock != nil {
				o.Failure, o.Abandoned = "deadlock", true
			}
			return o
		}
		k, err := pk.Pick(c)
		if err != nil {
			return Outcome{Stopped: err.Error(), Value: s.value(), Abandoned: true}
		}
		cur = cands[k]
		s.exec(cur)
	}
}

// naive enumerates every schedule of x with at most bound preemptions (no
// bound below zero), with no reduction, and returns the set of outcome
// values and the number of schedules; nil when there are more than limit.
func naive(x Executor, bound, limit int) (map[string]bool, int) {
	type nframe struct {
		allowed []int
		i       int
	}
	var path []nframe
	seen := map[string]bool{}
	runs := 0
	for {
		n, pre := 0, 0
		pk := pickFunc(func(c Choice) (int, error) {
			if n < len(path) {
				k := path[n].allowed[path[n].i]
				n++
				pre += cost(c, k)
				return k, nil
			}
			var allowed []int
			for k := range c.Enabled {
				if bound < 0 || pre+cost(c, k) <= bound {
					allowed = append(allowed, k)
				}
			}
			path = append(path, nframe{allowed: allowed})
			n++
			pre += cost(c, allowed[0])
			return allowed[0], nil
		})
		o := x.Run(pk)
		runs++
		if runs > limit {
			return nil, runs
		}
		seen[o.Value] = true
		for len(path) > 0 {
			top := &path[len(path)-1]
			top.i++
			if top.i < len(top.allowed) {
				break
			}
			path = path[:len(path)-1]
		}
		if len(path) == 0 {
			return seen, runs
		}
	}
}

type pickFunc func(Choice) (int, error)

func (f pickFunc) Pick(c Choice) (int, error) { return f(c) }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
