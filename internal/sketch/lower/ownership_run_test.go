package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestOwnershipRunModel is the model-based run test of the release
// lowering (#197 L8 B1; resources R5-R10, R21, R41, R43; plan section 3,
// the single-task part of the Quint machine ownership.qnt). It generates
// admitted bodies over: an acquisition (open, or give, whose callee
// returns its owned local, R10), a lend, a move by let, an explicit Close,
// a call of a taking function, a reassignment of a let mut (R41), a
// temporary (R43), if/else, a for loop with break and continue (R6), a
// try of a failing call (R7), a defect in the body or in a Close (R9),
// and a stop at a generated point of a task, reached by a queue handshake
// (no fixed waits). A Go reference model of the frame's list (the Quint
// machine) runs the same body and predicts the trace of opens, uses and
// closes and the exit. Each seed is one program; the programs of a run are
// linked into one binary (buildAll). A failing seed prints the Ergo
// source, both traces and the replay command (ERGO_SEED=n).
//
// It builds Go because its subject is the emitted scope code on its
// failure, defect and stop paths: go/types cannot run them, and Ergo has
// no reference interpreter yet (test-speed T8).
//
// Mutants it catches that nothing else does (internal/sketch/lower
// mutants.json, L8-B1): release at function end, not iteration end; a
// statement's temporaries not released; a moved local still released; a
// reassignment does not release the old value; a returned local released
// by its frame; the branch releases skipped.
func TestOwnershipRunModel(t *testing.T) {
	needsBuild(t)
	seeds, first := 48, 1
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ERGO_SEED %q: %v", s, err)
		}
		first, seeds = n, 1
	}
	type runCase struct {
		name, src, want string
	}
	var cases []runCase
	var programs []*lowered
	for seed := first; seed < first+seeds; seed++ {
		src, want := genOwnRun(seed)
		name := fmt.Sprintf("ownrun_%d", seed)
		cases = append(cases, runCase{name, src, want})
		programs = append(programs, mustLower(t, name, src))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for i, c := range cases {
		cmd := exec.Command(exes[c.name])
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := runProgram(t, cmd)
		got := stdout.String()
		if err != nil {
			got += "defect\n"
		}
		if got != c.want {
			t.Errorf("seed %d (ERGO_SEED=%d replays)\n%s\ngot:\n%s\nwant:\n%s\nstderr:\n%s", first+i, first+i, numbered(c.src), got, c.want, stderr.String())
		}
	}
}

// numbered is src with line numbers.
func numbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d  %s\n", i+1, l)
	}
	return b.String()
}

// orKind is a generated statement's kind.
type orKind uint8

const (
	orOpen   orKind = iota // let v = open(id, bad), or give(id, bad); mut marks let mut
	orUse                  // use(v)
	orMove                 // let v = w
	orClose                // v.Close()
	orKeep                 // keep(v): the callee takes v and releases it at its end
	orAssign               // v = open(id, bad), v a let mut (R41)
	orTemp                 // use(open(id, bad)), a temporary (R43)
	orIf                   // if n > cond { body } else { els }
	orFor                  // for i in 0..loops { body }, with breakAt and contAt
	orFail                 // try fail(cond): fails when n > cond
	orBoom                 // boom(cond): a defect when n > cond
	orStop                 // the stop point: push to q, wait on hang
	orAgg                  // a value built of parts, a later one may leave (ownership_parts_test.go)
	orReturn               // if n > cond { return n }
	orQLoop                // for rV in qK.Values() { … }: each element owned (Q7)
)

type orStmt struct {
	kind      orKind
	v, w      int // variables
	id        int
	bad, give bool
	mut       bool
	cond      int
	body, els []orStmt
	loops     int
	breakAt   int // -1 for none
	contAt    int // -1 for none
	agg       *orAggStmt
	q         int       // orQLoop: the queue's parameter number
	elems     []orEntry // orQLoop: the queue's elements, in push order
}

// orGen generates an admitted body: it tracks each variable's state as
// the checker does (owned, moved, maybe) and uses only owned ones.
type orGen struct {
	r        *rand.Rand
	nextV    int
	nextID   int
	state    map[int]int // 0 owned, 1 moved, 2 maybe
	mut      map[int]bool
	scopes   [][]int
	stop     bool // the case has a stop point (top level, once)
	depth    int
	queues   []orStmt // the queue loops, by queue number (no stop case has one)
	stopCase bool
	n        int // run's argument
}

func (g *orGen) owned() []int {
	var out []int
	for _, s := range g.scopes {
		for _, v := range s {
			if g.state[v] == 0 {
				out = append(out, v)
			}
		}
	}
	return out
}

func (g *orGen) declare(mut bool) int {
	v := g.nextV
	g.nextV++
	g.state[v] = 0
	g.mut[v] = mut
	g.scopes[len(g.scopes)-1] = append(g.scopes[len(g.scopes)-1], v)
	return v
}

func (g *orGen) id() int { g.nextID++; return g.nextID }

func (g *orGen) bad() bool { return g.r.IntN(7) == 0 }

func (g *orGen) block(n int) []orStmt {
	g.scopes = append(g.scopes, nil)
	g.depth++
	var out []orStmt
	for i := 0; i < n; i++ {
		out = append(out, g.stmt())
	}
	g.depth--
	g.scopes = g.scopes[:len(g.scopes)-1]
	return out
}

// snapshot and join are the generator's view of a branch's states.
func (g *orGen) snapshot() map[int]int {
	m := map[int]int{}
	for k, v := range g.state {
		m[k] = v
	}
	return m
}

func (g *orGen) join(a, b map[int]int) {
	for k := range g.state {
		x, y := a[k], b[k]
		switch {
		case x == y:
			g.state[k] = x
		default:
			g.state[k] = 2
		}
	}
}

func (g *orGen) stmt() orStmt {
	owned := g.owned()
	var muts []int
	for _, s := range g.scopes {
		for _, v := range s {
			if g.mut[v] && g.state[v] != 2 {
				muts = append(muts, v)
			}
		}
	}
	for {
		switch k := orKind(g.r.IntN(15)); k {
		case orAgg:
			return orStmt{kind: orAgg, agg: g.aggregate()}
		case orReturn:
			if g.r.IntN(3) != 0 {
				continue
			}
			return orStmt{kind: orReturn, cond: g.r.IntN(5)}
		case orQLoop:
			if g.stopCase || g.depth > 2 {
				continue
			}
			return g.queueLoop()
		case orOpen:
			mut := g.r.IntN(3) == 0
			s := orStmt{kind: orOpen, id: g.id(), bad: g.bad(), give: g.r.IntN(3) == 0, mut: mut}
			s.v = g.declare(mut)
			return s
		case orUse, orClose, orKeep, orMove:
			if len(owned) == 0 {
				continue
			}
			w := owned[g.r.IntN(len(owned))]
			switch k {
			case orUse:
				return orStmt{kind: orUse, w: w}
			case orMove:
				g.state[w] = 1
				s := orStmt{kind: orMove, w: w}
				s.v = g.declare(false)
				return s
			default:
				g.state[w] = 1
				return orStmt{kind: k, w: w}
			}
		case orAssign:
			if len(muts) == 0 {
				continue
			}
			v := muts[g.r.IntN(len(muts))]
			g.state[v] = 0
			return orStmt{kind: orAssign, v: v, id: g.id(), bad: g.bad()}
		case orTemp:
			return orStmt{kind: orTemp, id: g.id(), bad: g.bad()}
		case orIf:
			if g.depth > 2 {
				continue
			}
			s := orStmt{kind: orIf, cond: g.r.IntN(4)}
			before := g.snapshot()
			s.body = g.block(1 + g.r.IntN(3))
			then := g.snapshot()
			g.state = before
			g.state = g.snapshot()
			if g.r.IntN(2) == 0 {
				s.els = g.block(1 + g.r.IntN(2))
			}
			g.join(then, g.state)
			return s
		case orFor:
			if g.depth > 2 {
				continue
			}
			s := orStmt{kind: orFor, loops: 1 + g.r.IntN(3), breakAt: -1, contAt: -1}
			if g.r.IntN(2) == 0 {
				s.breakAt = g.r.IntN(s.loops)
			}
			if g.r.IntN(2) == 0 {
				s.contAt = g.r.IntN(s.loops)
			}
			// The body hands off nothing from outside (a second iteration
			// would use it after the hand-off): it sees no outer variable.
			saved := g.scopes
			g.scopes = nil
			s.body = g.block(1 + g.r.IntN(3))
			g.scopes = saved
			return s
		case orFail, orBoom:
			if g.r.IntN(3) != 0 {
				continue
			}
			return orStmt{kind: k, cond: g.r.IntN(5)}
		case orStop:
			if !g.stop || g.depth != 1 {
				continue
			}
			g.stop = false
			return orStmt{kind: orStop}
		}
	}
}

// genOwnRun is the program of one seed and its expected output.
func genOwnRun(seed int) (string, string) {
	for sub := 0; ; sub++ {
		r := rand.New(rand.NewPCG(uint64(seed), uint64(sub)))
		stop := r.IntN(3) == 0
		n := 1 + r.IntN(4)
		g := &orGen{r: r, state: map[int]int{}, mut: map[int]bool{}, stop: stop, stopCase: stop, n: n}
		body := g.block(3 + r.IntN(6))
		if stop && g.stop {
			continue // no stop point was placed
		}
		m := &orModel{n: n, queues: g.queues}
		want, reached := m.run(body, stop)
		if stop && !reached {
			continue // the body ended before its stop point
		}
		return orSource(body, n, stop, g.queues), want
	}
}

const orPrelude = `type Res { id int, bad bool }

type Bad { n int }

func Close(r Res) needs Console {
	Console.Println("close \(r.id)")
	if r.bad {
		Unreachable("close \(r.id)")
	}
}

func open(id int, bad bool) Res needs Console {
	Console.Println("open \(id)")
	Res{id: id, bad: bad}
}

func give(id int, bad bool) Res needs Console {
	let r = open(id, bad)
	use(r)
	return r
}

func use(r Res) needs Console {
	Console.Println("use \(r.id)")
}

func keep(r Res) needs Console takes r {
	Console.Println("keep \(r.id)")
}

func fail(n int, c int) fails Bad {
	if n > c {
		return Bad{n: n}
	}
}

func boom(n int, c int) {
	if n > c {
		Unreachable("boom")
	}
}

`

func orSource(body []orStmt, n int, stop bool, queues []orStmt) string {
	var b strings.Builder
	if stop {
		b.WriteString("import queue\nimport task\n\n")
	} else if len(queues) > 0 {
		b.WriteString("import queue\n\n")
	}
	b.WriteString(orPrelude)
	b.WriteString(orPartsPrelude)
	if len(queues) > 0 {
		b.WriteString(orQueuePrelude)
	}
	var qparams, qargs string
	for k := range queues {
		qparams += fmt.Sprintf(", q%d queue.Queue[Res]", k)
		qargs += fmt.Sprintf(", q%d", k)
	}
	if stop {
		b.WriteString("func run(n int, q queue.Queue[int], hang queue.Queue[int]) int fails Bad | queue.Closed needs Console {\n")
	} else {
		b.WriteString("func run(n int" + qparams + ") int fails Bad needs Console {\n")
	}
	orWrite(&b, body, 1, orAssigned(body, map[int]bool{}))
	b.WriteString("\tn\n}\n\n")
	if stop {
		fmt.Fprintf(&b, `func main() needs Console {
	let q = queue.New[int](1)
	let hang = queue.New[int](1)
	let t = task.Go() { run(%d, q, hang) }
	let _ = q.Pop() catch {
	case queue.Closed:
		0
	}
	t.Stop()
	Console.Println("stopped")
}
`, n)
	} else {
		b.WriteString("func main() needs Console {\n")
		// Each queue loop's queue is main's: filled and ended before run,
		// lent to run, and what run leaves in it is released at main's end.
		for k, q := range queues {
			fmt.Fprintf(&b, "\tlet q%d = queue.New[Res](%d)\n", k, len(q.elems))
			for _, e := range q.elems {
				fmt.Fprintf(&b, "\tfill(q%d, %d, %v)\n", k, e.id, e.bad)
			}
			fmt.Fprintf(&b, "\tq%d.End()\n", k)
		}
		fmt.Fprintf(&b, `	let v = run(%d%s) catch {
	case Bad:
		Console.Println("failure")
		-1
	}
	Console.Println("value \(v)")
}
`, n, qargs)
	}
	return b.String()
}

// orAssigned collects the variables a body reassigns: the only ones its
// let writes mut.
func orAssigned(list []orStmt, into map[int]bool) map[int]bool {
	for _, s := range list {
		if s.kind == orAssign {
			into[s.v] = true
		}
		orAssigned(s.body, into)
		orAssigned(s.els, into)
	}
	return into
}

func orWrite(b *strings.Builder, list []orStmt, depth int, muts map[int]bool) {
	ind := strings.Repeat("\t", depth)
	for _, s := range list {
		switch s.kind {
		case orOpen:
			let := "let"
			if muts[s.v] {
				let = "let mut"
			}
			fn := "open"
			if s.give {
				fn = "give"
			}
			fmt.Fprintf(b, "%s%s r%d = %s(%d, %v)\n", ind, let, s.v, fn, s.id, s.bad)
		case orUse:
			fmt.Fprintf(b, "%suse(r%d)\n", ind, s.w)
		case orMove:
			fmt.Fprintf(b, "%slet r%d = r%d\n", ind, s.v, s.w)
		case orClose:
			fmt.Fprintf(b, "%sr%d.Close()\n", ind, s.w)
		case orKeep:
			fmt.Fprintf(b, "%skeep(r%d)\n", ind, s.w)
		case orAssign:
			fmt.Fprintf(b, "%sr%d = open(%d, %v)\n", ind, s.v, s.id, s.bad)
		case orTemp:
			fmt.Fprintf(b, "%suse(open(%d, %v))\n", ind, s.id, s.bad)
		case orIf:
			fmt.Fprintf(b, "%sif n > %d {\n", ind, s.cond)
			orWrite(b, s.body, depth+1, muts)
			if s.els != nil {
				fmt.Fprintf(b, "%s} else {\n", ind)
				orWrite(b, s.els, depth+1, muts)
			}
			fmt.Fprintf(b, "%s}\n", ind)
		case orFor:
			i := fmt.Sprint("i", depth)
			if s.contAt < 0 && s.breakAt < 0 {
				i = "_"
			}
			fmt.Fprintf(b, "%sfor %s in 0..%d {\n", ind, i, s.loops)
			if s.contAt >= 0 {
				fmt.Fprintf(b, "%s\tif %s == %d {\n%s\t\tcontinue\n%s\t}\n", ind, i, s.contAt, ind, ind)
			}
			orWrite(b, s.body, depth+1, muts)
			if s.breakAt >= 0 {
				fmt.Fprintf(b, "%s\tif %s == %d {\n%s\t\tbreak\n%s\t}\n", ind, i, s.breakAt, ind, ind)
			}
			fmt.Fprintf(b, "%s}\n", ind)
		case orFail:
			fmt.Fprintf(b, "%stry fail(n, %d)\n", ind, s.cond)
		case orBoom:
			fmt.Fprintf(b, "%sboom(n, %d)\n", ind, s.cond)
		case orStop:
			fmt.Fprintf(b, "%stry q.Push(1)\n%slet _ = try hang.Pop()\n", ind, ind)
		case orAgg:
			s.agg.write(b, ind)
		case orReturn:
			fmt.Fprintf(b, "%sif n > %d {\n%s\treturn n\n%s}\n", ind, s.cond, ind, ind)
		case orQLoop:
			orWriteQueueLoop(b, s, depth, muts)
		}
	}
}

// orModel is the reference: run's frame list as a stack of entries, the
// single-task part of ownership.qnt. A scope end inside the frame
// releases its own entries, last first, and a defect there leaves the
// frame; leaving the frame releases every entry left, last first, each
// after an earlier defect.
type orModel struct {
	n      int
	out    []string
	stack  []*orEntry
	vars   map[int]*orEntry
	scope  map[int]int // variable -> its declaring scope
	nextSc int
	defect bool
	queues []orStmt    // the queue loops' queues, by number
	left   [][]orEntry // what each queue still holds
}

type orEntry struct {
	id    int
	bad   bool
	scope int
	// parts is a tuple's or record's parts, released last first by its
	// generated release (B2), each after an earlier defect (R9).
	parts []*orEntry
}

// orExit is how a statement list ends.
type orExit uint8

const (
	exNormal orExit = iota
	exBreak
	exContinue
	exFail   // a typed failure leaves the frame
	exDefect // a defect leaves the frame
	exStop   // the stop point: the task is stopped there
	exReturn // a return leaves the frame with its value
)

func (m *orModel) say(f string, a ...any) { m.out = append(m.out, fmt.Sprintf(f, a...)) }

// release ends one entry: it reports a defect.
func (m *orModel) release(e *orEntry) bool {
	if e.parts != nil {
		bad := false
		for i := len(e.parts) - 1; i >= 0; i-- {
			bad = m.release(e.parts[i]) || bad
		}
		return bad
	}
	m.say("close %d", e.id)
	if e.bad {
		m.defect = true
		return true
	}
	return false
}

func (m *orModel) push(e *orEntry) { m.stack = append(m.stack, e) }

func (m *orModel) remove(e *orEntry) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i] == e {
			m.stack = append(m.stack[:i], m.stack[i+1:]...)
			return
		}
	}
}

// endScope releases the entries of scope sc, last first; a defect leaves
// the frame at once.
func (m *orModel) endScope(sc int) bool {
	for i := len(m.stack) - 1; i >= 0; i-- {
		e := m.stack[i]
		if e.scope != sc {
			continue
		}
		m.stack = append(m.stack[:i], m.stack[i+1:]...)
		if m.release(e) {
			return true
		}
	}
	return false
}

func (m *orModel) acquire(v, id int, bad bool, sc int) {
	e := &orEntry{id: id, bad: bad, scope: sc}
	m.push(e)
	m.vars[v] = e
	m.scope[v] = sc
}

// run executes the body and returns the expected output, and whether the
// stop point was reached.
func (m *orModel) run(body []orStmt, stop bool) (string, bool) {
	m.vars, m.scope = map[int]*orEntry{}, map[int]int{}
	// main fills each queue first.
	for _, q := range m.queues {
		for _, e := range q.elems {
			m.say("open %d", e.id)
		}
		m.left = append(m.left, append([]orEntry(nil), q.elems...))
	}
	x := m.block(body)
	reached := x == exStop
	// The frame's End: every entry left, last first.
	for i := len(m.stack) - 1; i >= 0; i-- {
		m.release(m.stack[i])
	}
	m.stack = nil
	switch {
	case m.defect || x == exDefect:
	case stop:
		m.say("stopped")
	case x == exFail:
		m.say("failure")
		m.say("value -1")
	default:
		m.say("value %d", m.n)
	}
	// main's End: each queue, last made first, releases what it still
	// holds, last entered first.
	for k := len(m.left) - 1; k >= 0; k-- {
		for i := len(m.left[k]) - 1; i >= 0; i-- {
			m.release(&m.left[k][i])
		}
	}
	if m.defect || x == exDefect {
		m.say("defect")
	}
	return strings.Join(m.out, "\n") + "\n", reached
}

func (m *orModel) block(list []orStmt) orExit {
	m.nextSc++
	sc := m.nextSc
	x := m.stmts(list, sc)
	if x == exNormal && m.endScope(sc) {
		return exDefect
	}
	return x
}

func (m *orModel) stmts(list []orStmt, sc int) orExit {
	for _, s := range list {
		if x := m.stmt(s, sc); x != exNormal {
			return x
		}
	}
	return exNormal
}

func (m *orModel) stmt(s orStmt, sc int) orExit {
	switch s.kind {
	case orOpen:
		m.say("open %d", s.id)
		if s.give {
			// give owns its local and returns it: its frame ends with
			// nothing else to release.
			m.say("use %d", s.id)
		}
		m.acquire(s.v, s.id, s.bad, sc)
	case orUse:
		m.say("use %d", m.vars[s.w].id)
	case orMove:
		// The old entry ends and the new owner registers its own, on top.
		e := m.vars[s.w]
		m.vars[s.w] = nil
		m.remove(e)
		m.acquire(s.v, e.id, e.bad, sc)
	case orClose:
		e := m.vars[s.w]
		m.vars[s.w] = nil
		m.remove(e)
		if m.release(e) {
			return exDefect
		}
	case orKeep:
		e := m.vars[s.w]
		m.vars[s.w] = nil
		m.remove(e)
		m.say("keep %d", e.id)
		if m.release(e) { // keep's frame releases it at its end
			return exDefect
		}
	case orAssign:
		m.say("open %d", s.id)
		ne := &orEntry{id: s.id, bad: s.bad, scope: m.scope[s.v]}
		m.push(ne) // the new value is owned before the old one is released
		old := m.vars[s.v]
		m.vars[s.v] = ne
		if old != nil {
			m.remove(old)
			if m.release(old) {
				return exDefect
			}
		}
	case orTemp:
		m.say("open %d", s.id)
		e := &orEntry{id: s.id, bad: s.bad, scope: -1}
		m.push(e)
		m.say("use %d", s.id)
		m.remove(e)
		if m.release(e) {
			return exDefect
		}
	case orIf:
		if m.n > s.cond {
			return m.block(s.body)
		}
		if s.els != nil {
			return m.block(s.els)
		}
	case orFor:
		for i := 0; i < s.loops; i++ {
			if i == s.contAt {
				continue
			}
			m.nextSc++
			isc := m.nextSc
			x := m.stmts(s.body, isc)
			if x != exNormal {
				return x
			}
			if i == s.breakAt {
				if m.endScope(isc) {
					return exDefect
				}
				break
			}
			if m.endScope(isc) {
				return exDefect
			}
		}
	case orFail:
		if m.n > s.cond {
			return exFail
		}
	case orBoom:
		if m.n > s.cond {
			return exDefect
		}
	case orStop:
		return exStop
	case orAgg:
		return m.aggregate(s.agg, sc)
	case orReturn:
		if m.n > s.cond {
			return exReturn
		}
	case orQLoop:
		return m.queueLoop(s)
	}
	return exNormal
}
