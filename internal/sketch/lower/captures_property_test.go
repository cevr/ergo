package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The capture rule's seeded property (#197 C2; types.md, Function values
// 2 and 3; owner q60: "escaping let mut capture allowed only when no write
// follows"). Each seed writes a main over three let mut ints. Its
// statements write them; capture them in escaping literals appended to a
// slice and in tasks; write them inside an escaping literal; and nest in
// loops, ifs, a non-escaping block that its callee runs twice (each), and
// local frames (let h = func() { … }, only called). At the end it calls
// every literal, waits for every task and prints the ints. A let may bind
// a borrow of an int, which later statements write through (bump(pN, c)):
// each such use is a write of every int the borrow may point into
// (batch-15 C2 M1). The borrow is bound in each binding form (bLet…:
// &vK, another borrow, a tuple pattern, an if and a match value, a
// re-binding assignment, a tuple local taken apart by a let pattern or by
// a match clause), so the checker's fact must come from the value flow,
// not from one form (batch-15 C2 M1 repeat).
//
// The generator keeps the program as a tree and is the reference:
//
//   - the rule: a write of v is refused when it is inside an escaping
//     literal that captures v, or when, for some escaping literal or task
//     E that captures v, it runs after E: it starts after E ends, or a
//     loop, each block or local frame holds both; a write in a local frame
//     runs at each call of the frame (reasons below). The checker's S108
//     lines must be exactly the refused writes' lines;
//   - an interpreter that runs the tree twice: capturing by value (each
//     escaping literal and task holds a copy of what it reads, taken when
//     it is made) and by reference (it reads the variable when it runs).
//     On every admitted program both print the same lines: the rule is
//     sound. The seeds must also reach refused programs whose two runs
//     differ, one per reason, so each part of the rule refuses a program
//     that needs it.
//
// TestCaptureRuleModel checks both in process. TestCaptureLoweringAgrees
// builds the admitted programs of the seeds under both lowerings
// (captureLowering "" and "reference") and requires the interpreter's
// lines from each.
//
// A failure prints the seed, the Ergo source and both verdicts or outputs.
// Replay: go test -run '^TestCaptureRuleModel$' ./internal/sketch/lower -args -capture.seed=N

var captureSeed = flag.Uint64("capture.seed", 0, "run TestCaptureRuleModel and TestCaptureLoweringAgrees on this seed alone")

// The kinds of a generated statement.
const (
	cWrite        = iota // vK = vK + c
	cCapture             // fs = fs.Append(func() int { vA + vB + c })
	cCaptureWrite        // fs = fs.Append(func() int { vK = vK + c; vK })
	cTask                // let tN = task.Go() { vA + c }
	cLoop                // for _ in 0..2 { … }
	cIf                  // if vK > c { … } else { … }
	cEach                // each(2) { … }
	cLocal               // let hN = func() { writes }
	cCall                // hN()
	cBorrow              // let pN = <a borrow, in one of the binding forms below>: a write of what it borrows
	cBump                // bump(pN, c): a write of every int pN may point into
)

// The binding forms of a borrow (a cBorrow's form; batch-15 C2 M1 repeat).
const (
	bLet          = iota // let pN = &vK
	bChain               // let pN = pM
	bTuple               // let (pN, _) = (&vK, 0)
	bIf                  // let pN = if vA > c { &vK } else { &vJ }
	bMatch               // let pN = match vA { case 1: &vK  case _: &vJ }, one arm a line
	bRebind              // let mut pN = &vK, then pN = &vJ
	bTupleVia            // let pNt = (&vK, 0), then let (pN, _) = pNt: a tuple local that holds a borrow
	bClause              // let pNt = (&vK, 0), then match pNt { case (pN, _): bump(pN, c) }
	bSingleClause        // a non-tuple match subject binds the borrow by name (TypeSwitchStmt)
	bForms
)

var bFormName = []string{"let", "chain", "tuple", "if", "match", "rebind", "tuplevia", "clause", "singleclause"}

type cstmt struct {
	kind       int
	v, c       int   // the written or tested int, a constant
	reads      []int // the ints a capture or task reads
	form       int   // a borrow's binding form
	j, a       int   // a borrow's other int (if, match, rebind) and the tested one
	roots      []int // the ints a borrow may point into: the checker's flow-insensitive fact
	lines      []int // a borrow's source lines (match: each arm's; rebind: the assignment's)
	name       string
	body, els  []*cstmt
	line, end  int // the source lines of the statement and of its closing brace
	seq, after int // preorder numbers: the statement, and past its subtree
	regions    []*cstmt
	callee     *cstmt // a call's local frame; a bump's borrow
}

type captureGen struct {
	r      *rand.Rand
	src    strings.Builder
	line   int
	n      int // fresh names
	seq    int
	tasks  []*cstmt
	locals []*cstmt
	// borrows are the let-bound borrows in scope, outermost first.
	borrows []*cstmt
	bumped  map[*cstmt]bool
}

const capturePrelude = `func each(n int, f func()) {
	for _ in 0..n {
		f()
	}
}

func bump(p *int, c int) {
	*p = *p + c
}

func main() needs Console {
	let mut v0 = 0
	let mut v1 = 0
	let mut v2 = 0
	let mut fs = []func() int{}
	v0 = v0 + 1
	v1 = v1 + 1
	v2 = v2 + 1
	fs = fs.Append(func() int { 0 })
`

// captureProgram is seed's program: its tree, and its source.
func captureProgram(seed uint64) ([]*cstmt, string) {
	g := &captureGen{r: rand.New(rand.NewPCG(seed, 0x197c2))}
	body := g.block(0, nil, true)
	if len(g.tasks) > 0 {
		g.src.WriteString("import task\n\n")
	}
	g.src.WriteString(capturePrelude)
	g.line = strings.Count(g.src.String(), "\n")
	g.render(body, 1)
	g.src.WriteString("\tfor f in fs {\n\t\tConsole.Println(\"\\(f())\")\n\t}\n")
	for _, t := range g.tasks {
		fmt.Fprintf(&g.src, "\tConsole.Println(\"\\(%s.Wait())\")\n", t.name)
	}
	g.src.WriteString("\tConsole.Println(\"\\(v0) \\(v1) \\(v2)\")\n}\n")
	return body, g.src.String()
}

// block generates a statement list at depth; top says it is main's own
// list, where tasks live (a Task leaves its scope only by Wait).
func (g *captureGen) block(depth int, regions []*cstmt, top bool) []*cstmt {
	var out []*cstmt
	var locals []*cstmt
	mark := len(g.borrows)
	n := 1 + g.r.IntN(4)
	for range n {
		s := &cstmt{kind: g.r.IntN(11), v: g.r.IntN(3), c: 1 + g.r.IntN(5)}
		switch s.kind {
		case cBorrow:
			g.n++
			s.name = fmt.Sprintf("p%d", g.n)
			s.form = g.r.IntN(bForms)
			if len(g.borrows) > 0 && g.r.IntN(2) == 0 {
				s.form = bChain // a chain needs a borrow in scope: draw it as often as the rest
			}
			s.j = (s.v + 1 + g.r.IntN(2)) % 3
			s.a = g.r.IntN(3)
			switch s.form {
			case bChain:
				if len(g.borrows) == 0 {
					s.form = bLet
					s.roots = []int{s.v}
					break
				}
				// let pN = pM: a borrow passed on, a use of pM.
				s.callee = g.borrows[g.r.IntN(len(g.borrows))]
				s.v = s.callee.v
				s.roots = slices.Clone(s.callee.roots)
				if g.bumped == nil {
					g.bumped = map[*cstmt]bool{}
				}
				g.bumped[s.callee] = true
			case bLet, bTuple, bTupleVia, bClause, bSingleClause:
				s.roots = []int{s.v}
			default:
				s.roots = []int{s.v, s.j}
			}
			if s.form != bClause && s.form != bSingleClause { // clause names live in the clause alone
				g.borrows = append(g.borrows, s)
			}
		case cBump:
			if len(g.borrows) == 0 {
				s.kind = cWrite
				break
			}
			s.callee = g.borrows[g.r.IntN(len(g.borrows))]
			s.v = s.callee.v
			if g.bumped == nil {
				g.bumped = map[*cstmt]bool{}
			}
			g.bumped[s.callee] = true
		case cCapture:
			s.reads = []int{g.r.IntN(3)}
			if g.r.IntN(2) == 0 {
				s.reads = append(s.reads, g.r.IntN(3))
			}
		case cCaptureWrite:
			if g.r.IntN(3) != 0 {
				s.kind = cCapture // rarer: almost always refused
				s.reads = []int{s.v}
			}
		case cTask:
			if !top {
				s.kind = cWrite
				break
			}
			g.n++
			s.name = fmt.Sprintf("t%d", g.n)
			s.reads = []int{g.r.IntN(3)}
			g.tasks = append(g.tasks, s)
		case cLoop, cIf, cEach:
			if depth >= 2 {
				s.kind = cWrite
				break
			}
			inner := append(slices.Clone(regions), s)
			if s.kind == cIf {
				inner = regions
			}
			s.body = g.block(depth+1, inner, false)
			if s.kind == cIf {
				s.els = g.block(depth+1, regions, false)
			}
		case cLocal:
			g.n++
			s.name = fmt.Sprintf("h%d", g.n)
			for range 1 + g.r.IntN(2) {
				s.body = append(s.body, &cstmt{kind: cWrite, v: g.r.IntN(3), c: 1 + g.r.IntN(5)})
			}
			locals = append(locals, s)
		case cCall:
			if len(locals) == 0 {
				s.kind = cWrite
				break
			}
			s.callee = locals[g.r.IntN(len(locals))]
		}
		out = append(out, s)
	}
	// Every local frame is called at least once (an unread local is S110).
	for _, l := range locals {
		called := false
		for _, s := range out {
			called = called || callsLocal(s, l)
		}
		if !called {
			out = append(out, &cstmt{kind: cCall, callee: l})
		}
	}
	// Every borrow is used at least once (an unread local is S110).
	for _, b := range g.borrows[mark:] {
		if !g.bumped[b] {
			out = append(out, &cstmt{kind: cBump, callee: b, v: b.v, c: 1 + g.r.IntN(5)})
		}
	}
	g.borrows = g.borrows[:mark]
	return out
}

func callsLocal(s *cstmt, l *cstmt) bool {
	if s.callee == l {
		return true
	}
	for _, b := range [][]*cstmt{s.body, s.els} {
		for _, x := range b {
			if callsLocal(x, l) {
				return true
			}
		}
	}
	return false
}

func (g *captureGen) emit(depth int, format string, args ...any) int {
	g.line++
	g.src.WriteString(strings.Repeat("\t", depth))
	fmt.Fprintf(&g.src, format, args...)
	g.src.WriteString("\n")
	return g.line
}

func reads(vs []int, c int) string {
	var parts []string
	for _, v := range vs {
		parts = append(parts, fmt.Sprintf("v%d", v))
	}
	return strings.Join(parts, " + ") + fmt.Sprintf(" + %d", c)
}

// render writes the statements and numbers them, with the regions (loops,
// each blocks, local frames) around each.
func (g *captureGen) render(list []*cstmt, depth int) {
	g.renderIn(list, depth, nil)
}

func (g *captureGen) renderIn(list []*cstmt, depth int, regions []*cstmt) {
	for _, s := range list {
		g.seq++
		s.seq = g.seq
		s.regions = regions
		switch s.kind {
		case cWrite:
			s.line = g.emit(depth, "v%d = v%d + %d", s.v, s.v, s.c)
		case cCapture:
			s.line = g.emit(depth, "fs = fs.Append(func() int { %s })", reads(s.reads, s.c))
		case cCaptureWrite:
			s.line = g.emit(depth, "fs = fs.Append(func() int { v%d = v%d + %d; v%d })", s.v, s.v, s.c, s.v)
		case cTask:
			s.line = g.emit(depth, "let %s = task.Go() { %s }", s.name, reads(s.reads, s.c))
		case cLoop, cEach:
			if s.kind == cLoop {
				s.line = g.emit(depth, "for _ in 0..2 {")
			} else {
				s.line = g.emit(depth, "each(2) {")
			}
			g.renderIn(s.body, depth+1, append(slices.Clone(regions), s))
			s.end = g.emit(depth, "}")
		case cIf:
			s.line = g.emit(depth, "if v%d > %d {", s.v, s.c)
			g.renderIn(s.body, depth+1, regions)
			g.emit(depth, "} else {")
			g.renderIn(s.els, depth+1, regions)
			s.end = g.emit(depth, "}")
		case cLocal:
			s.line = g.emit(depth, "let %s = func() {", s.name)
			g.renderIn(s.body, depth+1, append(slices.Clone(regions), s))
			s.end = g.emit(depth, "}")
		case cCall:
			s.line = g.emit(depth, "%s()", s.callee.name)
		case cBorrow:
			switch s.form {
			case bChain:
				s.line = g.emit(depth, "let %s = %s", s.name, s.callee.name)
			case bLet:
				s.line = g.emit(depth, "let %s = &v%d", s.name, s.v)
			case bTuple:
				s.line = g.emit(depth, "let (%s, _) = (&v%d, 0)", s.name, s.v)
			case bIf:
				s.line = g.emit(depth, "let %s = if v%d > %d { &v%d } else { &v%d }", s.name, s.a, s.c, s.v, s.j)
			case bMatch:
				s.line = g.emit(depth, "let %s = match v%d {", s.name, s.a)
				g.emit(depth, "case 1:")
				k := g.emit(depth+1, "&v%d", s.v)
				g.emit(depth, "case _:")
				j := g.emit(depth+1, "&v%d", s.j)
				g.emit(depth, "}")
				s.lines = []int{k, j}
			case bRebind:
				s.line = g.emit(depth, "let mut %s = &v%d", s.name, s.v)
				s.lines = []int{g.emit(depth, "%s = &v%d", s.name, s.j)}
			case bTupleVia:
				s.line = g.emit(depth, "let %st = (&v%d, 0)", s.name, s.v)
				s.lines = []int{g.emit(depth, "let (%s, _) = %st", s.name, s.name)}
			case bClause:
				s.line = g.emit(depth, "let %st = (&v%d, 0)", s.name, s.v)
				m := g.emit(depth, "match %st {", s.name)
				g.emit(depth, "case (%s, _):", s.name)
				b := g.emit(depth+1, "bump(%s, %d)", s.name, s.c)
				g.emit(depth, "}")
				s.lines = []int{m, b}
			case bSingleClause:
				s.line = g.emit(depth, "let %st = &v%d", s.name, s.v)
				m := g.emit(depth, "match %st {", s.name)
				g.emit(depth, "case %s:", s.name)
				b := g.emit(depth+1, "bump(%s, %d)", s.name, s.c)
				g.emit(depth, "}")
				s.lines = []int{m, b}
			}
		case cBump:
			s.line = g.emit(depth, "bump(%s, %d)", s.callee.name, s.c)
		}
		s.after = g.seq + 1
	}
}

// walk calls f on every statement of the tree, in source order.
func walkC(list []*cstmt, f func(*cstmt)) {
	for _, s := range list {
		f(s)
		walkC(s.body, f)
		walkC(s.els, f)
	}
}

// wev is one write of the int v at a line by the statement s.
type wev struct {
	s       *cstmt
	v, line int
}

// writesOf is the writes of s: a borrow &vK is a write of vK where it is
// taken, and each use of a local that holds a borrow is a write of every
// int it may point into (its roots, flow-insensitive: the checker's fact).
func writesOf(s *cstmt) []wev {
	var out []wev
	all := func(b *cstmt, line int) {
		for _, v := range b.roots {
			out = append(out, wev{s, v, line})
		}
	}
	switch s.kind {
	case cWrite:
		out = append(out, wev{s, s.v, s.line})
	case cBump:
		all(s.callee, s.line)
	case cBorrow:
		switch s.form {
		case bChain:
			all(s.callee, s.line)
		case bLet, bTuple:
			out = append(out, wev{s, s.v, s.line})
		case bIf:
			out = append(out, wev{s, s.v, s.line}, wev{s, s.j, s.line})
		case bMatch:
			out = append(out, wev{s, s.v, s.lines[0]}, wev{s, s.j, s.lines[1]})
		case bRebind:
			out = append(out, wev{s, s.v, s.line})
			all(s, s.lines[0]) // &vJ, and the assignment uses pN
		case bTupleVia:
			out = append(out, wev{s, s.v, s.line}, wev{s, s.v, s.lines[0]}) // &vK, then a use of pNt
		case bClause, bSingleClause:
			// &vK, the subject pNt, and the bump through the clause's pN.
			out = append(out, wev{s, s.v, s.line}, wev{s, s.v, s.lines[0]}, wev{s, s.v, s.lines[1]})
		}
	}
	return out
}

// refused is the reference rule: the lines of the refused writes, with
// the reason of each ("inside", "after", "region", "local", and
// "alias-<form>" for a write through a borrow bound in that form).
func refused(tree []*cstmt) map[int]string {
	var captures, calls []*cstmt
	var writes []wev
	walkC(tree, func(s *cstmt) {
		switch s.kind {
		case cCapture, cTask:
			captures = append(captures, s)
		case cCaptureWrite:
			captures = append(captures, s)
		case cWrite, cBorrow, cBump:
			writes = append(writes, writesOf(s)...)
		case cCall:
			calls = append(calls, s)
		}
	})
	out := map[int]string{}
	shared := func(a, b []*cstmt) bool { return len(a) > 0 && len(b) > 0 && a[0] == b[0] }
	// follows reports why the event at seq with regions runs after e, or "".
	follows := func(e *cstmt, seq int, regions []*cstmt) string {
		if seq >= e.after {
			return "after"
		}
		if shared(e.regions, regions) {
			return "region"
		}
		return ""
	}
	for _, e := range captures {
		if e.kind == cCaptureWrite {
			if _, ok := out[e.line]; !ok {
				out[e.line] = "inside"
			}
		}
		captured := e.reads
		if e.kind == cCaptureWrite {
			captured = []int{e.v}
		}
		for _, wr := range writes {
			w := wr.s
			if !slices.Contains(captured, wr.v) {
				continue
			}
			if _, ok := out[wr.line]; ok {
				continue
			}
			var local *cstmt
			for _, r := range w.regions {
				if r.kind == cLocal {
					local = r
				}
			}
			if local == nil {
				if why := follows(e, w.seq, w.regions); why != "" {
					if w.kind == cBump {
						why = "alias-" + bFormName[w.callee.form]
					}
					out[wr.line] = why
				}
				continue
			}
			for _, c := range calls {
				if c.callee == local && follows(e, c.seq, c.regions) != "" {
					out[wr.line] = "local"
					break
				}
			}
		}
	}
	return out
}

// interpret runs the tree, capturing by value or by reference, and is
// the lines the program prints.
func interpret(tree []*cstmt, byValue bool) string {
	vars := []int{1, 1, 1}
	type closure struct {
		s    *cstmt
		snap []int
	}
	fs := []*closure{{}}
	var tasks []int
	target := map[*cstmt]int{} // the int each borrow points to when it runs
	var run func(list []*cstmt)
	run = func(list []*cstmt) {
		for _, s := range list {
			switch s.kind {
			case cWrite:
				vars[s.v] += s.c
			case cBump:
				vars[target[s.callee]] += s.c
			case cBorrow:
				switch s.form {
				case bChain:
					target[s] = target[s.callee]
				case bLet, bTuple, bTupleVia:
					target[s] = s.v
				case bIf:
					target[s] = s.j
					if vars[s.a] > s.c {
						target[s] = s.v
					}
				case bMatch:
					target[s] = s.j
					if vars[s.a] == 1 {
						target[s] = s.v
					}
				case bRebind:
					target[s] = s.j
				case bClause, bSingleClause:
					vars[s.v] += s.c
				}
			case cCapture, cCaptureWrite:
				fs = append(fs, &closure{s: s, snap: slices.Clone(vars)})
			case cTask:
				// A task reads when it is made in both runs: a task that
				// reads later races with the writes, and the rule refuses
				// every program where that could differ.
				t := s.c
				for _, v := range s.reads {
					t += vars[v]
				}
				tasks = append(tasks, t)
			case cLoop, cEach:
				run(s.body)
				run(s.body)
			case cIf:
				if vars[s.v] > s.c {
					run(s.body)
				} else {
					run(s.els)
				}
			case cCall:
				run(s.callee.body)
			}
		}
	}
	run(tree)
	var out strings.Builder
	for _, f := range fs {
		if f.s == nil {
			out.WriteString("0\n")
			continue
		}
		cells := vars
		if byValue {
			cells = f.snap
		}
		if f.s.kind == cCaptureWrite {
			cells[f.s.v] += f.s.c
			fmt.Fprintf(&out, "%d\n", cells[f.s.v])
			continue
		}
		t := f.s.c
		for _, v := range f.s.reads {
			t += cells[v]
		}
		fmt.Fprintf(&out, "%d\n", t)
	}
	for _, t := range tasks {
		fmt.Fprintf(&out, "%d\n", t)
	}
	fmt.Fprintf(&out, "%d %d %d\n", vars[0], vars[1], vars[2])
	return out.String()
}

func captureSeeds(n int) []uint64 {
	if *captureSeed != 0 {
		return []uint64{*captureSeed}
	}
	seeds := make([]uint64, n)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	return seeds
}

type holderProgram struct {
	name, src, want string
	escapes         bool
}

// holderPrograms extends the property's domain with the thing being
// captured: a borrow holder, not just its referent. The independent oracle
// is Borrows 1: holder mutability and its binding form cannot change whether
// a frame may keep the holder. The old generator never captured a holder.
// Mutants: c2-mutable-holder-capture-admitted,
// c2-escaping-holder-alias-erased; the clause forms kill c2-clause-unbound.
func holderPrograms() []holderProgram {
	var out []holderProgram
	for mut := range 3 {
		for tuple := range 2 {
			for binding := range 6 {
				for frame := range 5 {
					seed := uint64(1 + (((mut*2+tuple)*6+binding)*5 + frame))
					if *captureSeed != 0 && *captureSeed != seed {
						continue
					}
					r := rand.New(rand.NewPCG(seed, 0x197c204))
					delta := 1 + r.IntN(9)
					val := "&x"
					if tuple != 0 {
						val = "((&x, 0), 1)"
					}
					initial := strings.ReplaceAll(val, "&x", "&y")
					bound := val
					setup := ""
					switch binding {
					case 1:
						setup = "let alias = " + val + "\n"
						bound = "alias"
					case 2:
						bound = "if true { " + val + " } else { " + val + " }"
					case 3:
						bound = "match 1 {\ncase _: " + val + "\n}"
					case 4:
						setup = "let (alias, _) = (" + val + ", 0)\n"
						bound = "alias"
					case 5:
						bound = "with context {} { " + val + " }"
					}
					bind := setup + "let p = " + bound + "\n"
					if mut > 0 {
						first := bound
						if mut == 2 {
							first = initial
						}
						bind = setup + "let mut p = " + first + "\np = " + bound + "\n"
					}
					write := fmt.Sprintf("bump(p, %d)", delta)
					if tuple != 0 {
						write = fmt.Sprintf("let ((q, _), _) = p; bump(q, %d)", delta)
					}
					work := ""
					switch frame {
					case 0:
						work = write + "\n"
					case 1:
						work = "(func() { " + write + " })()\n"
					case 2:
						work = "each(1, func() { " + write + " })\n"
					case 3:
						work = "let local = func() { " + write + " }\nlocal()\n"
					}
					tail := work + "let get = func() int { x }\n(get, func() {})\n"
					if frame == 4 {
						tail = "let get = func() int { x }\n(get, func() { " + write + " })\n"
					}
					src := "func bump(p *int, n int) { *p = *p + n }\nfunc each(n int, f func()) { for _ in 0..n { f() } }\nfunc make() (func() int, func()) {\nlet mut x = 0\nlet mut y = 0\nlet _ = &y\n" + bind + tail + "}\nfunc main() needs Console { let (get, put) = make(); put(); Console.Println(\"\\(get())\") }\n"
					out = append(out, holderProgram{fmt.Sprintf("holder%d", seed), src, fmt.Sprintf("%d\n", delta), frame == 4})
				}
			}
		}
	}
	return out
}

func captureHolderModel(t *testing.T) {
	t.Helper()
	for _, p := range holderPrograms() {
		o := lowerSource(p.name, p.src, true)
		if o.err != nil || o.panicked != nil {
			t.Fatalf("%s: %v %v\n%s", p.name, o.err, o.panicked, p.src)
		}
		borrow, rootWrite := false, false
		for _, d := range o.diags {
			if d.Code != "S108" || (!strings.Contains(d.Message, "Borrows 1") && !strings.Contains(d.Message, "Function values 3")) {
				t.Fatalf("%s: unexpected %s\n%s", p.name, d, p.src)
			}
			borrow = borrow || strings.Contains(d.Message, "Borrows 1")
			rootWrite = rootWrite || strings.Contains(d.Message, "Function values 3")
		}
		if borrow != p.escapes || rootWrite != p.escapes || (!p.escapes && len(o.diags) != 0) {
			t.Fatalf("%s: Borrows 1 refusal %v, want %v; diagnostics %v\nReplay: go test -run '^TestCaptureRuleModel$' ./internal/sketch/lower -args -capture.seed=%s\n%s", p.name, borrow, p.escapes, o.diags, strings.TrimPrefix(p.name, "holder"), p.src)
		}
	}
}

// callAliasPrograms draws opaque borrow flow through calls. The optimization
// cannot assume Borrows 1 was proved at a generic instantiation. Mutants:
// c2-address-proof-ignored and c2-implicit-address-proof-ignored. The no-borrow
// control kills c2-fast-path-lost, without pinning a count of captures.
func callAliasPrograms() []holderProgram {
	var out []holderProgram
	for _, seed := range captureSeeds(24) {
		r := rand.New(rand.NewPCG(seed, 0x197c205))
		n := 1 + r.IntN(17)
		forms := []string{
			"let p = id(&x)",
			"let p = Box.pass(&x)",
			"let f = later(&x); let p = f()",
			"let mut p = &y; put(&p, &x)",
			"let p = id(id(&x))",
		}
		for form, bind := range forms {
			for before := range 2 {
				work := fmt.Sprintf("bump(p, %d)\n", n)
				get := "let get = func() int { x }\n"
				if before != 0 {
					get, work = work+get, ""
				}
				src := `type Box {}
func id[T](x T) T { x }
func pass[T](b Box, x T) T { x }
func later[T](x T) func() T { func() T { x } }
func put[T](out *T, x T) { *out = x }
func bump(p *int, n int) { *p = *p + n }
func make() func() int {
let mut x = 0
let mut y = 0
let _ = &y
` + bind + "\n" + get + work + "get\n}\nfunc main() needs Console { let get = make(); Console.Println(\"\\(get())\") }\n"
				out = append(out, holderProgram{fmt.Sprintf("call%d_%d_%d", seed, form, before), src, fmt.Sprintf("%d\n", n), false})
			}
		}
	}
	// Examples of both proof outcomes: no alias, and an implicit receiver
	// borrow. The latter is conservative even though this borrow ends early.
	out = append(out,
		holderProgram{"fast", "func make() func() int { let mut x = 0; x = 7; func() int { x } }\nfunc main() needs Console { let get = make(); Console.Println(\"\\(get())\") }", "7\n", false},
		holderProgram{"implicit", "type Box { n int }\nfunc Bump(b *Box) { b.n = 7 }\nfunc make() func() int { let mut x = Box{n: 0}; x.Bump(); func() int { x.n } }\nfunc main() needs Console { let get = make(); Console.Println(\"\\(get())\") }", "7\n", false})
	return out
}

func captureAliasModel(t *testing.T) {
	t.Helper()
	fast, reference := 0, 0
	for _, p := range callAliasPrograms() {
		o := lowerSource(p.name, p.src, true)
		if o.err != nil || o.panicked != nil || len(o.diags) != 0 || len(o.refused) != 0 {
			t.Fatalf("%s: %v %v %v %v\n%s", p.name, o.err, o.panicked, o.diags, o.refused, p.src)
		}
		found := false
		for _, vs := range o.pkg.Info.ValueCaptures {
			for _, v := range vs {
				if v.Name == "x" {
					found = true
				}
			}
		}
		if found != (p.name == "fast") {
			t.Fatalf("%s: by-value capture %v, want positive no-alias proof %v\n%s", p.name, found, p.name == "fast", p.src)
		}
		if found {
			fast++
		} else {
			reference++
		}
	}
	t.Logf("capture examples: %d fast, %d reference", fast, reference)
}

// TestCaptureRuleModel is the in-process half of the property (see above).
func TestCaptureRuleModel(t *testing.T) {
	captureAliasModel(t)
	captureHolderModel(t)
	reached := map[string]bool{}
	admitted := 0
	fast := 0
	for _, seed := range captureSeeds(4000) {
		tree, src := captureProgram(seed)
		want := refused(tree)
		o := lowerSource(fmt.Sprintf("cap%d", seed), src, true)
		if o.err != nil || o.panicked != nil {
			t.Fatalf("seed %d: %v %v\n%s", seed, o.err, o.panicked, src)
		}
		got := map[int]bool{}
		for _, d := range o.diags {
			if d.Code != "S108" || !strings.Contains(d.Message, "Function values 3") {
				t.Fatalf("seed %d: an unexpected diagnostic %s\nReplay: go test -run '^TestCaptureRuleModel$' ./internal/sketch/lower -args -capture.seed=%d\n%s", seed, d, seed, src)
			}
			got[d.Line] = true
		}
		var wantLines, gotLines []int
		for l := range want {
			wantLines = append(wantLines, l)
		}
		for l := range got {
			gotLines = append(gotLines, l)
		}
		sort.Ints(wantLines)
		sort.Ints(gotLines)
		if !slices.Equal(wantLines, gotLines) {
			t.Errorf("seed %d: the checker refuses the writes at lines %v, the reference at %v %v\nReplay: go test -run '^TestCaptureRuleModel$' ./internal/sketch/lower -args -capture.seed=%d\n%s", seed, gotLines, wantLines, want, seed, src)
			continue
		}
		value, ref := interpret(tree, true), interpret(tree, false)
		if len(want) == 0 {
			admitted++
			for _, vs := range o.pkg.Info.ValueCaptures {
				fast += len(vs)
			}
			if value != ref {
				t.Errorf("seed %d: an admitted program prints differently by value and by reference\nReplay: go test -run '^TestCaptureRuleModel$' ./internal/sketch/lower -args -capture.seed=%d\n%s\n--- by value\n%s--- by reference\n%s", seed, seed, src, value, ref)
			}
			continue
		}
		// A refused program whose two runs differ, by the reason of its
		// one refused write: that part of the rule is needed.
		if value != ref && len(want) == 1 {
			for _, why := range want {
				reached[why] = true
			}
		}
	}
	t.Logf("%d admitted programs; refused programs that need each reason: %v", admitted, reached)
	t.Logf("admitted generated examples took %d by-value capture fast paths", fast)
	if *captureSeed == 0 {
		if admitted == 0 {
			t.Errorf("no seed writes an admitted program")
		}
		if fast == 0 {
			t.Error("no admitted generated example takes the capture fast path")
		}
		whys := []string{"inside", "after", "region", "local"}
		for _, f := range bFormName {
			if f != "clause" && f != "singleclause" {
				// A clause's bump writes with its own &vK, so it is never
				// refused alone; the checker's lines must still equal the
				// reference's on it.
				whys = append(whys, "alias-"+f)
			}
		}
		for _, why := range whys {
			if !reached[why] {
				t.Errorf("no seed refuses a program by %q alone whose runs differ: %v", why, reached)
			}
		}
	}
}

// TestCaptureLoweringAgrees builds the admitted programs of the seeds with
// the escaping frames' captures by value and by reference, and requires
// the interpreter's lines from both.
//
// It builds Go because the claim is about what the emitted closures print
// when they run; the interpreter and the checker are in process already.
func TestCaptureLoweringAgrees(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	copies := false
	for _, seed := range captureSeeds(60) {
		tree, src := captureProgram(seed)
		if len(refused(tree)) > 0 {
			continue
		}
		want := interpret(tree, true)
		for _, mode := range []string{"", "reference"} {
			saved := captureLowering
			captureLowering = mode
			p := mustLower(t, fmt.Sprintf("capl%d%s", seed, mode), src)
			captureLowering = saved
			if mode == "" && strings.Contains(p.mainGo(), "v0_1 := v0") {
				copies = true
			}
			programs = append(programs, p)
			runs = append(runs, run{seed, src, want})
		}
	}
	if !copies && *captureSeed == 0 {
		t.Errorf("no admitted program captures a let mut by value")
	}
	for _, p := range append(holderPrograms(), callAliasPrograms()...) {
		if p.escapes {
			continue
		}
		// The only write is the generated bump, executed before get is
		// captured. Its argument is printed in the source: independent of
		// the capture lowering, both executions must agree.
		for _, mode := range []string{"", "reference"} {
			saved := captureLowering
			captureLowering = mode
			programs = append(programs, mustLower(t, p.name+mode, p.src))
			captureLowering = saved
			runs = append(runs, run{0, p.src, p.want})
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the capture programs do not build")
	}
	for i, p := range programs {
		out, stderr, code := runExe(t, exes[p.name])
		if out != runs[i].want || code != 0 {
			t.Errorf("seed %d, %s: the program and the interpreter differ\nReplay: go test -run '^TestCaptureLoweringAgrees$' ./internal/sketch/lower -args -capture.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- interpreter\n%s", runs[i].seed, p.name, runs[i].seed, runs[i].src, code, out, stderr, runs[i].want)
		}
	}
}
