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
// every literal, waits for every task and prints the ints.
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
)

type cstmt struct {
	kind       int
	v, c       int   // the written or tested int, a constant
	reads      []int // the ints a capture or task reads
	name       string
	body, els  []*cstmt
	line, end  int // the source lines of the statement and of its closing brace
	seq, after int // preorder numbers: the statement, and past its subtree
	regions    []*cstmt
	callee     *cstmt // a call's local frame
}

type captureGen struct {
	r      *rand.Rand
	src    strings.Builder
	line   int
	n      int // fresh names
	seq    int
	tasks  []*cstmt
	locals []*cstmt
}

const capturePrelude = `func each(n int, f func()) {
	for _ in 0..n {
		f()
	}
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
	n := 1 + g.r.IntN(4)
	for range n {
		s := &cstmt{kind: g.r.IntN(9), v: g.r.IntN(3), c: 1 + g.r.IntN(5)}
		switch s.kind {
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

// refused is the reference rule: the lines of the refused writes, with
// the reason of each ("inside", "after", "region", "local").
func refused(tree []*cstmt) map[int]string {
	var captures, writes, calls []*cstmt
	walkC(tree, func(s *cstmt) {
		switch s.kind {
		case cCapture, cTask:
			captures = append(captures, s)
		case cCaptureWrite:
			captures = append(captures, s)
		case cWrite:
			writes = append(writes, s)
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
		for _, w := range writes {
			if !slices.Contains(captured, w.v) {
				continue
			}
			if _, ok := out[w.line]; ok {
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
					out[w.line] = why
				}
				continue
			}
			for _, c := range calls {
				if c.callee == local && follows(e, c.seq, c.regions) != "" {
					out[w.line] = "local"
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
	var run func(list []*cstmt)
	run = func(list []*cstmt) {
		for _, s := range list {
			switch s.kind {
			case cWrite:
				vars[s.v] += s.c
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

// TestCaptureRuleModel is the in-process half of the property (see above).
func TestCaptureRuleModel(t *testing.T) {
	reached := map[string]bool{}
	admitted := 0
	for _, seed := range captureSeeds(300) {
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
	if *captureSeed == 0 {
		if admitted == 0 {
			t.Errorf("no seed writes an admitted program")
		}
		for _, why := range []string{"inside", "after", "region", "local"} {
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
