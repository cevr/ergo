package lower

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"iter"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// #199 stage 1 (runtime.md, I1): a frame whose footprint is not empty
// checks for a stop before each capability call and at the end of each
// loop iteration. Recursive or opaque impure frames also poll at entry, so
// recursive back-edges pass a check; a pure frame has none. The oracle is
// the source's shape, never
// Info.Footprints: each clause below states whether its frame is pure.

// i1Clause is one footprint clause: the statements that head a frame's
// body, and what they make of its footprint.
type i1Clause struct {
	name   string
	prefix string
	needs  bool   // the frame needs Probe
	param  string // a parameter the clause reads
	impure bool
	// site is the capability call the frame may make inside its loops,
	// "" when the clause gives it no Probe to call.
	site string
	// provider: admitted in an operation's implementation, whose
	// signature has no row and no free parameter.
	provider bool
}

var i1Clauses = []i1Clause{
	{name: "pure", provider: true},
	{name: "pure-call", prefix: "pureHelper()\n", provider: true},
	{name: "recursive-pure", prefix: "let _ = even(3)\n", provider: true},
	{name: "capability", prefix: "Probe.Tick()\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "transitive", prefix: "helper()\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "recursive", prefix: "ping(2)\n", needs: true, impure: true, site: "Probe.Mark()\n"},
	{name: "own-with", prefix: "with Local {\nProbe.Tick()\n}\n", impure: true, site: "with Local {\nProbe.Mark()\n}\n", provider: true},
	{name: "unknown-value", prefix: "cb()\n", param: "cb escaping func()", impure: true},
	{name: "host", prefix: "let _ = hostTrim(\"\")\n", impure: true, provider: true},
	{name: "task-start", prefix: "let t0 = task.Go() { 1 }\nt0.Stop()\n", impure: true, provider: true},
}

// i1Loop is one loop form: what precedes it, its head and the statements
// that open its body, for nesting depth d.
type i1Loop struct {
	name  string
	parts func(d int) (pre, head, open string)
	param string // a parameter the loop reads
	seq   bool   // iterating calls a Seq, a function value: the frame is opaque
}

var i1Loops = []i1Loop{
	{name: "range", parts: func(d int) (string, string, string) {
		return "", fmt.Sprintf("for x%d in 0..3 {", d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "slice", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let xs%d = []int{0, 1, 2}\n", d), fmt.Sprintf("for x%d in xs%d {", d, d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "map", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let m%d = Map[string, int]{\"a\": 0, \"b\": 1}\n", d), fmt.Sprintf("for x%d in m%d {", d, d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "seq", param: "s Seq[int]", seq: true, parts: func(d int) (string, string, string) {
		return "", fmt.Sprintf("for x%d in s {", d), fmt.Sprintf("let _ = x%d\n", d)
	}},
	{name: "cond", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), fmt.Sprintf("for x%d < 3 {", d), fmt.Sprintf("x%d += 1\n", d)
	}},
	{name: "emitting-cond", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), fmt.Sprintf("for below(x%d) {", d), fmt.Sprintf("x%d += 1\n", d)
	}},
	{name: "infinite", parts: func(d int) (string, string, string) {
		return fmt.Sprintf("let mut x%d = 0\n", d), "for {", fmt.Sprintf("if x%d == 3 {\nbreak\n}\nx%d += 1\n", d, d)
	}},
}

// i1Variant is what the innermost loop body does besides counting.
type i1Variant struct {
	name                          string
	cont, brk, fail, site, nested bool
}

var i1Variants = []i1Variant{
	{name: "plain"},
	{name: "continue", cont: true},
	{name: "break", brk: true},
	{name: "failure", fail: true},
	{name: "site", site: true, cont: true},
}

const i1Defs = `import task
extern go strings.TrimSpace func hostTrim(s string) string
type Probe {
	Tick(),
	Mark(),
}
type Silent {}
type Fault {}
func Tick(_ Silent) {}
func Mark(_ Silent) {}
let Local = context { Probe: Silent }
func helper() needs Probe { Probe.Tick() }
func pureHelper() {}
func below(n int) bool { n < 3 }
func guard(n int) fails Fault {
	if n > 100 {
		return Fault
	}
}
func even(n int) bool {
	if n == 0 {
		return true
	}
	odd(n - 1)
}
func odd(n int) bool {
	if n == 0 {
		return false
	}
	even(n - 1)
}
func ping(n int) needs Probe {
	if n > 0 {
		pong(n - 1)
	}
}
func pong(n int) needs Probe {
	Probe.Tick()
	ping(n)
}
func started() {
	let t = task.Go() { 1 }
	t.Stop()
}
`

// i1LoopShapes is the loop-shape axis every loop generator shares: each
// form alone, and each form holding the next form nested in its body. A new
// shape added here reaches the ordinary, block and law generators at once.
func i1LoopShapes() [][]i1Loop {
	var shapes [][]i1Loop
	for i, l := range i1Loops {
		shapes = append(shapes, []i1Loop{l}, []i1Loop{l, i1Loops[(i+1)%len(i1Loops)]})
	}
	return shapes
}

// i1ShapeParams names a shape's loop forms, outside in, and gives the
// parameter the first Seq form reads ("" when the shape holds none).
func i1ShapeParams(shape []i1Loop) (names []string, param string) {
	for _, l := range shape {
		names = append(names, l.name)
		if l.seq && param == "" {
			param = l.param
		}
	}
	return names, param
}

// i1Loops builds the loops of one frame: the given forms from the outside
// in, the innermost body doing v. It declares and counts sum.
func i1Body(prefix string, loops []i1Loop, v i1Variant, site string) string {
	last := len(loops) - 1
	inner := "sum += 1\n"
	if v.brk {
		inner = fmt.Sprintf("if x%d == 2 {\nbreak\n}\n", last) + inner
	}
	if v.cont {
		inner = fmt.Sprintf("if x%d == 1 {\ncontinue\n}\n", last) + inner
	}
	if v.fail {
		inner = fmt.Sprintf("try guard(x%d)\n", last) + inner
	}
	if v.site {
		inner = site + inner
	}
	for d := last; d >= 0; d-- {
		pre, head, open := loops[d].parts(d)
		inner = pre + head + "\n" + open + inner + "}\n"
	}
	return "let mut sum = 0\n" + prefix + inner
}

// i1Case is one generated program and, for the Go frames that hold its
// loops, whether each must poll: "decl" is a Go function, "lit" a closure.
type i1Case struct {
	name        string
	src         string
	want        map[string]bool
	site        bool            // the program calls Probe.Mark in a polling frame
	entries     map[string]bool // named Go frames whose entry rule is known from the source
	markerEntry *bool           // entry rule for the source frame declaring entryMarker
	markerScope bool            // that frame owns a scope, which it opens first
	markers     int             // source frames declaring entryMarker, when markerEntry is set
	testBuild   bool            // lowered as the test build (LowerTests), whose frame it is
}

// i1Lower lowers a generated program: its test build when its frame exists
// only there (a test body), its run build otherwise.
func i1Lower(t *testing.T, c i1Case) *lowered {
	t.Helper()
	if c.testBuild {
		probeLog("lower-test", probeSum([]byte(c.src)))
		return lowerTestBuild(t, "i1", c.src)
	}
	return mustLower(t, "i1", c.src)
}

// i1UserDecls drops the test build's harness from the Go declarations, so
// the poll checks read the program's frames only: the harness is fixed Go
// that takes no Env, runs no Ergo code and owns loops no source holds.
func i1UserDecls(decls []ast.Decl) []ast.Decl {
	harness := map[string]bool{}
	for _, h := range testHelpers {
		harness[h.base] = true
	}
	var out []ast.Decl
	for _, d := range decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				name = strings.TrimPrefix(types.ExprString(fd.Recv.List[0].Type), "*")
			}
			if harness[name] {
				continue
			}
		}
		out = append(out, d)
	}
	return out
}

// i1Cases generates every frame class × clause × loop form × variant the
// rules admit. A class that cannot hold a clause or a form skips it: a
// provider method has no row and no free parameter.
func i1Cases() []i1Case {
	var out []i1Case
	for _, cl := range i1Clauses {
		for _, shape := range i1LoopShapes() {
			var params []string
			if cl.param != "" {
				params = append(params, cl.param)
			}
			names, seqParam := i1ShapeParams(shape)
			seq := seqParam != ""
			if seq {
				params = append(params, seqParam)
			}
			impure := cl.impure || seq
			for _, v := range i1Variants {
				if v.site && cl.site == "" {
					continue
				}
				body := i1Body(cl.prefix, shape, v, cl.site)
				row, try := "", ""
				if v.fail {
					row, try = " fails Fault", "try "
				}
				if cl.needs {
					row += " needs Probe"
				}
				ps := strings.Join(params, ", ")
				name := fmt.Sprintf("%s/%s/%s", cl.name, strings.Join(names, "+"), v.name)
				add := func(class, src string, want map[string]bool, testBuild bool) {
					out = append(out, i1Case{name: class + "/" + name, src: i1Defs + src + "func main() {}\n", want: want, site: v.site, testBuild: testBuild})
				}
				for _, frame := range i1FrameKinds {
					if !frame.params && (!cl.provider || seq || v.fail || ps != "" || row != "") {
						continue
					}
					add(frame.name, frame.program(ps, row, try, body+"sum\n"), map[string]bool{frame.goClass: impure}, frame.testBuild)
				}
				if cl.name == "pure" && !seq && !v.fail && !v.site {
					// Nested frames of opposite footprints: an impure
					// function's pure literal, and a pure function that
					// makes an impure literal and never runs it.
					add("impure-parent", "func subject() int needs Probe {\nProbe.Tick()\nlet local = func() int {\n"+body+"sum\n}\n"+body+"sum + local()\n}\n", map[string]bool{"decl": true, "lit": false}, false)
					add("pure-parent", "func subject() int {\nlet _ = func() int needs Probe {\nProbe.Tick()\n"+body+"sum\n}\n"+body+"sum\n}\n", map[string]bool{"decl": false, "lit": true}, false)
				}
			}
		}
	}
	return out
}

// i1Wraps are the frame classes a nested frame can sit in: each takes the
// statements of the frame inside, which end in an int, and gives the
// statements of the frame around it, which end in an int. A with region
// runs in the frame around it; the others are frames of their own.
var i1Wraps = i1NestingFrames()

// i1NestCases generates the frame-nesting axis (I1.2: a point checks its
// own frame's task, at every depth): every sequence of two or three frame
// classes from i1Wraps inside a declaration, so a literal in a literal, a
// literal in a task body, a task in a literal, a task in a task and a with
// region in a nested literal all occur, at depth 3 and 4. The innermost
// frame holds the loops, over each clause a frame of any class can hold,
// each variant and, in turn, each loop form.
func i1NestCases() []i1Case {
	var seqs [][]int
	for a := range i1Wraps {
		for b := range i1Wraps {
			seqs = append(seqs, []int{a, b})
			for c := range i1Wraps {
				seqs = append(seqs, []int{a, b, c})
			}
		}
	}
	var out []i1Case
	k := 0
	for _, seq := range seqs {
		var names []string
		class := "decl"
		trailing := false
		for _, w := range seq {
			names = append(names, i1Wraps[w].name)
			if i1Wraps[w].frame {
				class = "lit"
			}
			trailing = trailing || i1Wraps[w].name == "trailing"
		}
		for _, cl := range i1Clauses {
			if cl.needs || cl.param != "" {
				// A row on every enclosing frame is the rows grid's business.
				continue
			}
			for _, v := range i1Variants {
				if v.fail || (v.site && cl.site == "") {
					continue
				}
				l := i1Loops[k%len(i1Loops)]
				k++
				params := ""
				if l.seq {
					params = l.param
				}
				inner := i1Body(cl.prefix, []i1Loop{l}, v, cl.site) + "sum\n"
				for d := len(seq) - 1; d >= 0; d-- {
					inner = i1Wraps[seq[d]].wrap(d, inner)
				}
				name := fmt.Sprintf("nest/%s/%s/%s/%s", strings.Join(names, ">"), cl.name, l.name, v.name)
				for _, frame := range i1FrameKinds {
					if !frame.params && (params != "" || !cl.provider) {
						continue
					}
					src := frame.program(params, "", "", inner)
					if trailing && frame.name != "trailing" {
						src = "func run(f func() int) int { f() }\n" + src
					}
					goClass := class
					if class == "decl" {
						goClass = frame.goClass
					}
					out = append(out, i1Case{name: "nest-" + frame.name + "/" + name, src: i1Defs + src + "func main() {}\n", want: map[string]bool{goClass: cl.impure || l.seq}, site: v.site, testBuild: frame.testBuild})
				}
			}
		}
	}
	return out
}

// i1AllCases is the generated domain of the I1 property, in order.
func i1AllCases() []i1Case {
	// Ordinary trailing blocks and with bodies use the reviewer's grid.
	cases := append(append(i1Cases(), i1BlockCases()...), i1RecursionCases()...)
	cases = append(append(cases, i1EntryCases()...), i1NestCases()...)
	return append(cases, i1LawCases()...)
}

// i1Domain is i1AllCases, generated once per test process: each part and
// the coverage check range over it. No reader changes a case.
var i1Domain = sync.OnceValue(i1AllCases)

// i1Split is the I1 property in parts: each part lowers its share of the
// generated programs, each as a subtest named by its case, and checks the
// facts and the polls of each.
var i1Split = newSplit(split[i1Case]{
	name:  "TestRuntime199I1Property",
	parts: i1Parts,
	short: i1Short,
	// The pin: the domain of the unsplit property at f79be34b
	// (request.md, leg 1), re-pinned by std-align S2b when the extern
	// shape's LiveScheduler came from NewLiveScheduler and its written
	// rows listed Ambient (the same 17131 cases, two source texts changed),
	// and re-pinned by #162 law-run-1 when the law body joined the frame
	// catalog (i1FrameKinds "top-law"): the 17131 cases unchanged plus 1516
	// cases of that frame (209 top-law/, 1303 nest-top-law/, 4
	// entry-top-law/). Without that frame kind the domain is the old pin
	// exactly. Batch-12 integration joined that with lw-pure-prelude's
	// re-pin (the host effect became a user extern call, hostTrim, because
	// the prelude's Append is pure; the same cases): 18647 cases, digest
	// measured on the merged head.
	count:  18647,
	digest: "58b557c59845afb2b0874e863e2505fb31b2423a59bfbe00bb2d75b1d7e49c0a",
	cases: func(yield func(i1Case) bool) {
		for _, c := range i1Domain() {
			if !yield(c) {
				return
			}
		}
	},
	key: i1Key,
	run: func(t *testing.T, cases iter.Seq[i1Case]) {
		for c := range cases {
			t.Run(c.name, func(t *testing.T) {
				o := i1Lower(t, c)
				i1CheckFacts(t, o)
				i1CheckPolls(t, o, c)
			})
		}
	},
})

const (
	// i1Parts: the 17 131 cases took 170 to 180 s in one test at load
	// 30 to 46 (measured at f79be34b), so a part takes about 8 s; the
	// 18 647 of #162 about 9 s.
	i1Parts = 24
	// i1Short: under -short a part checks every 50th case of its share.
	i1Short = 50
)

// i1Key is a case's name, its source and what it asserts.
func i1Key(c i1Case) string {
	marker := "-"
	if c.markerEntry != nil {
		marker = fmt.Sprint(*c.markerEntry)
	}
	return fmt.Sprintf("%s\n%s\nwant %v site %v entries %v marker %s scope %v markers %d test %v",
		c.name, c.src, c.want, c.site, c.entries, marker, c.markerScope, c.markers, c.testBuild)
}

func TestRuntime199I1PropertyPart0(t *testing.T)  { i1Split.Part(t, 0) }
func TestRuntime199I1PropertyPart1(t *testing.T)  { i1Split.Part(t, 1) }
func TestRuntime199I1PropertyPart2(t *testing.T)  { i1Split.Part(t, 2) }
func TestRuntime199I1PropertyPart3(t *testing.T)  { i1Split.Part(t, 3) }
func TestRuntime199I1PropertyPart4(t *testing.T)  { i1Split.Part(t, 4) }
func TestRuntime199I1PropertyPart5(t *testing.T)  { i1Split.Part(t, 5) }
func TestRuntime199I1PropertyPart6(t *testing.T)  { i1Split.Part(t, 6) }
func TestRuntime199I1PropertyPart7(t *testing.T)  { i1Split.Part(t, 7) }
func TestRuntime199I1PropertyPart8(t *testing.T)  { i1Split.Part(t, 8) }
func TestRuntime199I1PropertyPart9(t *testing.T)  { i1Split.Part(t, 9) }
func TestRuntime199I1PropertyPart10(t *testing.T) { i1Split.Part(t, 10) }
func TestRuntime199I1PropertyPart11(t *testing.T) { i1Split.Part(t, 11) }
func TestRuntime199I1PropertyPart12(t *testing.T) { i1Split.Part(t, 12) }
func TestRuntime199I1PropertyPart13(t *testing.T) { i1Split.Part(t, 13) }
func TestRuntime199I1PropertyPart14(t *testing.T) { i1Split.Part(t, 14) }
func TestRuntime199I1PropertyPart15(t *testing.T) { i1Split.Part(t, 15) }
func TestRuntime199I1PropertyPart16(t *testing.T) { i1Split.Part(t, 16) }
func TestRuntime199I1PropertyPart17(t *testing.T) { i1Split.Part(t, 17) }
func TestRuntime199I1PropertyPart18(t *testing.T) { i1Split.Part(t, 18) }
func TestRuntime199I1PropertyPart19(t *testing.T) { i1Split.Part(t, 19) }
func TestRuntime199I1PropertyPart20(t *testing.T) { i1Split.Part(t, 20) }
func TestRuntime199I1PropertyPart21(t *testing.T) { i1Split.Part(t, 21) }
func TestRuntime199I1PropertyPart22(t *testing.T) { i1Split.Part(t, 22) }
func TestRuntime199I1PropertyPart23(t *testing.T) { i1Split.Part(t, 23) }

// TestRuntime199I1PropertyDomain: the generated domain holds every frame
// class on the ordinary and the nesting axes, and each law case in both
// builds (the checks the unsplit property made before its cases).
func TestRuntime199I1PropertyDomain(t *testing.T) {
	cases := i1AllCases()
	classes := map[string]int{}
	for _, c := range cases {
		classes[strings.SplitN(c.name, "/", 2)[0]]++
	}
	for _, frame := range i1FrameKinds {
		if classes[frame.name] == 0 || classes["nest-"+frame.name] == 0 {
			t.Errorf("frame class %s missing from ordinary or nesting axis", frame.name)
		}
	}
	for _, class := range []string{"impure-parent", "pure-parent", "with-body", "recursion", "law"} {
		if classes[class] == 0 {
			t.Errorf("no generated case of the frame class %s", class)
		}
	}
	// Each law case is lowered in both builds: a member law of a type with
	// no provider, which neither build runs (types.md, Laws), so neither
	// holds its loops. The top-law frame class covers a law the test
	// build runs.
	lawBuilds := map[bool]int{}
	for _, c := range cases {
		if strings.HasPrefix(c.name, "law/") {
			lawBuilds[c.testBuild]++
		}
	}
	if lawBuilds[false] == 0 || lawBuilds[false] != lawBuilds[true] {
		t.Errorf("law cases: %d in the run build, %d in the test build; want the same nonzero count", lawBuilds[false], lawBuilds[true])
	}
	// The law cases span the shared loop-shape axis × each variant a law
	// admits × each placement, in each build: no shape, nesting, failure
	// literal or build drops out of the law domain unnoticed.
	lawNames := map[string]bool{}
	for _, c := range cases {
		lawNames[c.name] = true
	}
	for _, build := range []string{"run", "test"} {
		for _, shape := range i1LoopShapes() {
			names, _ := i1ShapeParams(shape)
			for _, v := range i1Variants {
				for _, p := range i1LawPlacements {
					if name := "law/" + build + "/" + p.name + "/" + strings.Join(names, "+") + "/" + v.name; !v.site && !lawNames[name] {
						t.Errorf("no law case %s", name)
					}
				}
			}
		}
	}
	t.Logf("%d generated programs: %v", len(cases), classes)
}

// TestRuntime199I1Primitives: the runtime's blocking primitives keep their
// own interruption points (a subtest of the unsplit property).
func TestRuntime199I1Primitives(t *testing.T) { i1CheckPrimitives(t) }

// TestRuntime199I1Live: the live witnesses build and stop (a subtest of
// the unsplit property).
func TestRuntime199I1Live(t *testing.T) {
	needsBuild(t)
	i1Live(t)
}

// Adapted from the round 1 reviewer's zz_ext_test.go: block frames, generated
// over the same clauses, loop forms and exits as i1Cases.
//   - trailing: the block of a call of a program function (a BlockStmt
//     frame that is not a task body);
//   - with-body: the loops run inside a with block, in the enclosing frame.
func i1BlockCases() []i1Case {
	var out []i1Case
	for _, cl := range i1Clauses {
		for _, shape := range i1LoopShapes() {
			var params []string
			if cl.param != "" {
				params = append(params, cl.param)
			}
			names, seqParam := i1ShapeParams(shape)
			seq := seqParam != ""
			if seq {
				params = append(params, seqParam)
			}
			impure := cl.impure || seq
			for _, v := range i1Variants {
				if v.site && cl.site == "" {
					continue
				}
				body := i1Body(cl.prefix, shape, v, cl.site)
				row := ""
				if v.fail {
					row = " fails Fault"
				}
				if cl.needs {
					row += " needs Probe"
				}
				ps := strings.Join(params, ", ")
				name := fmt.Sprintf("%s/%s/%s", cl.name, strings.Join(names, "+"), v.name)
				out = append(out, i1Case{name: "with-body/" + name, src: i1Defs + "func subject(" + ps + ") int" + row + " {\nwith Local {\n" + body + "sum\n}\n}\nfunc main() {}\n", want: map[string]bool{"decl": impure}, site: v.site})
			}
		}
	}
	return out
}

// i1LawCases put each loop shape and variant in a law, in its body and in a
// function literal in it (stage E3b), and lower each program as the run
// build and as the test build. A law body is checked as if it ran, so the
// checker gives it and its literal footprints (i1CheckFacts), but neither
// build lowers a law (types.md, Laws 11): its want is empty, so a Go loop
// from a law, in either build, fails i1CheckPolls, as a law loop counted as
// a source loop does.
func i1LawCases() []i1Case {
	var out []i1Case
	for _, build := range []struct {
		name string
		test bool
	}{{"run", false}, {"test", true}} {
		out = append(out, i1LawLoops(build.name, build.test)...)
	}
	return out
}

// i1LawPlacements are the places in a law that can hold loops: the law
// body, and a function literal the law declares and calls. A literal whose
// loops try a failure declares the failure in its row, and the law tries
// the call, as a law tries a fallible tail (types.md, Laws 8).
var i1LawPlacements = []struct {
	name  string
	place func(body string, fail bool) string
}{
	{name: "body", place: func(body string, _ bool) string { return body + "sum >= 0\n" }},
	{name: "literal", place: func(body string, fail bool) string {
		row, try := "", ""
		if fail {
			row, try = " fails Fault", "try "
		}
		return "let f = func() int" + row + " {\n" + body + "sum\n}\n" + try + "f() >= 0\n"
	}},
}

// i1LawLoops is one build's law cases: the shared loop-shape axis
// (i1LoopShapes: one loop, and two nested loops) × every variant a law
// admits × every placement (i1LawPlacements). A law has no capability, so
// the site variant, which calls one, has no law case.
func i1LawLoops(build string, testBuild bool) []i1Case {
	var out []i1Case
	for _, shape := range i1LoopShapes() {
		names, ps := i1ShapeParams(shape)
		for _, v := range i1Variants {
			if v.site {
				continue
			}
			body := i1Body("", shape, v, "")
			for _, p := range i1LawPlacements {
				src := "type Lawful {\n\tStep() int,\n\n\tlaw Loops(" + ps + ") {\n" + p.place(body, v.fail) + "},\n}\n"
				name := "law/" + build + "/" + p.name + "/" + strings.Join(names, "+") + "/" + v.name
				out = append(out, i1Case{name: name, src: i1Defs + src + "func main() {}\n", want: map[string]bool{}, testBuild: testBuild})
			}
		}
	}
	return out
}

// i1EntryEffects are the entry axis's footprints: what follows the marker
// in the frame's body, and whether the frame then polls at entry. Starting
// a task and stopping it calls the Task's operations, which are opaque, and
// makes the frame own a scope.
var i1EntryEffects = []struct {
	name, body   string
	entry, scope bool
}{
	{name: "pure"},
	{name: "capability", body: "with Local { Probe.Tick() }\n"},
	{name: "opaque", body: "let _ = hostTrim(\"\")\n", entry: true},
	{name: "scope", body: "let t0 = task.Go() { 1 }\nt0.Stop()\n", entry: true, scope: true},
}

// i1EntryCases separates entry placement from loop/capability polls, over
// every frame class of the shared catalog (i1FrameKinds) × every entry
// effect, so a new frame class, a test body included, gets an entry
// obligation with no list to extend. The marker is the first source
// statement, so a later capability checkpoint cannot stand in for an entry
// poll. A frame that starts a task owns a scope: its entry poll follows the
// scope's opening and precedes the source body. A class whose program
// writes its body more than once (an initializer) marks that many frames.
func i1EntryCases() []i1Case {
	var out []i1Case
	for _, effect := range i1EntryEffects {
		body := "let entryMarker = 0\nlet _ = entryMarker\n" + effect.body + "1\n"
		for _, frame := range i1FrameKinds {
			src := frame.program("", "", "", body)
			want := effect.entry
			out = append(out, i1Case{
				name:        "entry-" + frame.name + "/" + effect.name,
				src:         i1Defs + src + "func main() {}\n",
				markerEntry: &want,
				markerScope: effect.scope,
				markers:     strings.Count(src, "entryMarker = 0"),
				testBuild:   frame.testBuild,
			})
		}
	}
	return out
}

// i1Graph is one recursion shape. decls declares its frames with an
// effect's row and clause; call starts the recursion at %d. Each recursive
// path doubles the work (fib), so an unstopped live run never ends.
type i1Graph struct {
	name  string
	decls func(row, clause string) string
	call  string
	// frames are the Go frames (Recv.name for a method) that poll at entry
	// when the effect is impure, or always when the shape is opaque.
	frames []string
	// opaque: the shape calls a function value, so its frames, and a
	// subject that calls them, poll at entry whatever the effect.
	opaque bool
	// provider: the recursion runs in a provider method, which has no row.
	// The subject reaches it through a capability, and its entry is not
	// asserted.
	provider bool
	// ambient: the shape makes a host provider, so a written row of its
	// frames and of the subject that calls them lists Ambient.
	ambient bool
}

// row is the written row of a frame that calls the shape's frames.
func (g i1Graph) row(row string) string {
	if g.ambient && row != "" {
		return row + " | Ambient"
	}
	return row
}

func i1Fib(name, call1, call2 string) func(row, clause string) string {
	return func(row, clause string) string {
		return "func " + name + " int" + row + " {\n" + clause + "if n < 2 { return n }\n" + call1 + " + " + call2 + "\n}\n"
	}
}

var i1Graphs = append([]i1Graph{
	{name: "self", decls: i1Fib("left(n int)", "left(n - 1)", "left(n - 2)"), call: "left(%d)", frames: []string{"left"}},
	{name: "mutual", decls: func(row, clause string) string {
		return i1Fib("left(n int)", "right(n - 1)", "right(n - 2)")(row, clause) + "func right(n int) int" + row + " { left(n) }\n"
	}, call: "left(%d)", frames: []string{"left", "right"}},
	// A three-member component where only one member has the clause.
	{name: "three-cycle", decls: func(row, clause string) string {
		return i1Fib("left(n int)", "mid(n - 1)", "mid(n - 2)")(row, clause) + "func mid(n int) int" + row + " { right(n) }\nfunc right(n int) int" + row + " { left(n) }\n"
	}, call: "left(%d)", frames: []string{"left", "mid", "right"}},
	{name: "value-reentry", decls: func(row, clause string) string {
		return "func left(n int) int" + row + " {\n" + clause + "if n < 2 { return n }\nlet next = left\nnext(n - 1) + next(n - 2)\n}\n"
	}, call: "left(%d)", frames: []string{"left"}, opaque: true},
	// A method recursing by a dot call on its receiver.
	{name: "method", decls: func(row, clause string) string {
		return "type Box { n int }\n" + i1Fib("left(b Box, n int)", "b.left(n - 1)", "b.left(n - 2)")(row, clause)
	}, call: "let b = Box { n: 1 }\nb.left(%d)", frames: []string{"Box.left"}},
	{name: "generic", decls: i1Fib("left[T](x T, n int)", "left(x, n - 1)", "left(x, n - 2)"), call: "left(1, %d)", frames: []string{"left"}},
	// The recursive calls run in a with body of the frame.
	{name: "with-body", decls: func(row, clause string) string {
		return "func left(n int) int" + row + " {\n" + clause + "if n < 2 { return n }\nwith Local { left(n - 1) + left(n - 2) }\n}\n"
	}, call: "left(%d)", frames: []string{"left"}},
	// Through a literal called where it is written: an edge, not a value.
	{name: "lit-in-place", decls: i1Fib("left(n int)", "func() int { left(n - 1) }()", "left(n - 2)"), call: "left(%d)", frames: []string{"left"}},
	// Through a user function's trailing block, which run calls as a value.
	{name: "trailing", decls: func(row, clause string) string {
		return "func run(cb func() int" + row + ") int" + row + " { cb() }\n" + i1Fib("left(n int)", "run() { left(n - 1) }", "run() { left(n - 2) }")(row, clause)
	}, call: "left(%d)", frames: []string{"left", "run"}, opaque: true},
	// A provider method recursing by a dot call on its receiver.
	{name: "provider-self", decls: func(row, clause string) string {
		return "type Worker { Run(n int) int, }\ntype Impl {}\n" + i1Fib("Run(i Impl, n int)", "i.Run(n - 1)", "i.Run(n - 2)")(row, clause) + "let Work = context { Worker: Impl }\n"
	}, call: "with Work { Worker.Run(%d) }", frames: []string{"Impl.Run"}, provider: true},
}, i1CallGraphs()...)

// i1OpCycle recurses through a provider operation: a capability call is no
// call-graph edge, so neither frame polls at entry, and the capability
// call's own point is what stops it (the live witness is red under a
// mutant that drops that point, not under one that drops entry polls).
const i1OpCycle = `type Worker { Run(n int) int, }
type Impl {}
func Run(_ Impl, n int) int {
	with Work { left(n) }
}
let Work = context { Worker: Impl }
func left(n int) int needs Worker {
	if n < 2 { return n }
	Worker.Run(n - 1) + Worker.Run(n - 2)
}
`

// i1Effects are a recursion's footprint clauses, on a branch that never
// runs: a capability named in the row, a capability discharged by the
// frame's own with (an empty row), and an opaque host call.
var i1Effects = []struct{ name, row, clause string }{
	{name: "pure"},
	{name: "capability", row: " needs Probe", clause: "if n < 0 { Probe.Tick() }\n"},
	{name: "own-with", clause: "if n < 0 { with Local { Probe.Tick() } }\n"},
	{name: "opaque", clause: "if n < 0 { let _ = hostTrim(\"\") }\n"},
	{name: "scope", clause: "if n < 0 { let t0 = task.Go() { 1 }; t0.Stop() }\n"},
}

// i1RecursionCases varies the call graph independently of the footprint:
// a pure recursive SCC stays pure; a capability on an unexecuted branch
// still makes recursion impure; an opaque value can re-enter its caller.
// EntryPoll is not inherited by a nonrecursive, transparent caller.
func i1RecursionCases() []i1Case {
	var out []i1Case
	for _, g := range i1Graphs {
		for _, e := range i1Effects {
			if g.provider && e.row != "" {
				continue
			}
			want := e.name != "pure" || g.opaque
			entries := map[string]bool{}
			for _, f := range g.frames {
				entries[f] = want
			}
			if !g.provider {
				entries["subject"] = e.name == "opaque" || e.name == "scope" || g.opaque
			}
			src := g.decls(e.row, e.clause) + "func subject() int" + g.row(e.row) + " {\n" + fmt.Sprintf(g.call, 3) + "\n}\nfunc main() {}\n"
			out = append(out, i1Case{name: "recursion/" + g.name + "/" + e.name, src: i1Defs + src, entries: entries, want: map[string]bool{"decl": want}})
		}
	}
	out = append(out, i1Case{name: "recursion/op-cycle/capability", src: i1Defs + i1OpCycle + "func subject() int {\nwith Work { left(3) }\n}\nfunc main() {}\n", entries: map[string]bool{"left": false, "Impl.Run": false}})
	return out
}

// i1GoFrame names a Go function: Recv.name for a method.
func i1GoFrame(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// i1CheckFacts walks the checked tree: every frame key is a known frame
// class, and every loop's frame has a footprint the lowering reads, so a
// newly admitted frame form fails here first.
func i1CheckFacts(t *testing.T, o *lowered) {
	t.Helper()
	info := o.pkg.Info
	i1RequireFrameClasses(t, o)
	for _, call := range info.Calls {
		if call.Kind < 0 || call.Kind >= check.CallKindCount || call.Kind == check.CallPrelude {
			t.Errorf("checker produced retired or unknown call kind %d", call.Kind)
		}
	}
	for key := range info.Footprints {
		switch key.(type) {
		case *syntax.FuncDecl, *syntax.FuncLit, *syntax.BlockStmt, *syntax.ContextDecl, *syntax.VarSpec, *syntax.TestDecl, *syntax.LawDecl:
		default:
			t.Errorf("a footprint keyed by an unknown frame form %T", key)
		}
	}
	var in func(n, frame syntax.Node)
	in = func(n, frame syntax.Node) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			switch x := m.(type) {
			case *syntax.FuncLit:
				if x != frame {
					in(x, x)
					return false
				}
			case *syntax.CallExpr:
				if c := info.Calls[x]; x.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
					in(x.Fun, frame)
					for _, a := range x.Args {
						in(a, frame)
					}
					in(x.Trailing, x.Trailing)
					return false
				}
			case *syntax.ForStmt, *syntax.RangeStmt:
				if _, ok := info.Footprints[frame]; !ok {
					t.Errorf("a loop at %d in a frame %T with no footprint", m.Pos(), frame)
				}
			}
			return true
		})
	}
	for _, d := range o.file.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			in(d, d)
		case *syntax.ContextDecl:
			in(d, d)
		case *syntax.TestDecl:
			in(d, d)
		case *syntax.TypeDecl:
			// A law body is a frame of its own, checked as if it ran
			// (types.md, Laws 9), so its loops have a footprint too.
			if it, ok := d.Spec.(*syntax.InterfaceType); ok {
				for _, law := range it.Laws {
					in(law, law)
				}
			}
		case *syntax.VarDecl:
			for _, s := range d.Specs {
				in(s, s)
			}
		}
	}
}

// i1CheckPolls reads the emitted Go: each loop's frame is the innermost Go
// function or closure around it; an impure frame's loop body ends with
// the check of the frame's own task, which every continue reaches, a pure
// frame's has none, and the Go
// loops are exactly the source's (no loop is lost and none is generated).
// Each capability call Mark follows the check of the task it is passed.
func i1CheckPolls(t *testing.T, o *lowered, c i1Case) {
	t.Helper()
	code := o.mainGo()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.testBuild {
		f.Decls = i1UserDecls(f.Decls)
	}
	// The test build lowers a top-level law, which runs once or over
	// generated values (types.md, Laws; #162 law-run-1); a member law runs
	// per provider, and no generated program declares one. The run build
	// lowers no law.
	topLaws := map[*syntax.LawDecl]bool{}
	for _, d := range o.file.Decls {
		if law, ok := d.(*syntax.LawDecl); ok {
			topLaws[law] = true
		}
	}
	source := 0
	syntax.Inspect(o.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ForStmt, *syntax.RangeStmt:
			source++
		case *syntax.LawDecl:
			return c.testBuild && topLaws[n]
		}
		return true
	})
	goLoops, marks := 0, 0
	entries := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if want, ok := c.entries[i1GoFrame(fd)]; ok {
			entries++
			list := fd.Body.List
			if len(list) >= 2 && i1OpensScope(list[0], list[1], i1EnvParam(fd.Type)) {
				list = list[2:]
			}
			got := len(list) > 0 && isOwnCheck(list[0], i1EnvParam(fd.Type))
			if got != want {
				t.Errorf("%s entry poll = %v, want %v\nGo:\n%s", i1GoFrame(fd), got, want, stripDirectives(code))
			}
		}
	}
	if entries != len(c.entries) {
		t.Errorf("found %d of %d expected entry frames", entries, len(c.entries))
	}
	var stack []ast.Node
	marked := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		var env *ast.Ident
		for i := len(stack) - 1; i >= 0; i-- {
			if fd, ok := stack[i].(*ast.FuncDecl); ok {
				env = i1EnvParam(fd.Type)
				break
			}
			if fl, ok := stack[i].(*ast.FuncLit); ok {
				env = i1EnvParam(fl.Type)
				break
			}
		}
		var body *ast.BlockStmt
		switch n := n.(type) {
		case *ast.FuncDecl:
			if c.markerEntry != nil && i1HasEntryMarker(n.Body) {
				marked++
				i1CheckEntry(t, n.Body, env, *c.markerEntry, c.markerScope)
			}
		case *ast.FuncLit:
			if c.markerEntry != nil && i1HasEntryMarker(n.Body) {
				marked++
				i1CheckEntry(t, n.Body, env, *c.markerEntry, c.markerScope)
			}
		case *ast.ExprStmt:
			// Check every point kind, including entries and capability
			// sites. Resolve with's plain Env copies back to this frame's
			// parameter; a captured parent parameter has a different object.
			if isCheckOf(n, "") && !isOwnCheck(n, env) {
				t.Errorf("poll %s does not check its frame's own env parameter\nGo:\n%s", types.ExprString(stmtCall(n, "Check")), stripDirectives(code))
			}
		case *ast.ForStmt:
			body = n.Body
		case *ast.RangeStmt:
			body = n.Body
		case *ast.BlockStmt:
			for i, s := range n.List {
				// A call site passes its frame's task; the conversion's
				// adapter method, which passes an Env, is no call site.
				if call := stmtCall(s, "Mark"); call != nil && len(call.Args) == 1 && isTaskOf(call.Args[0]) {
					marks++
					if !isFrameTask(call.Args[0], env) || i == 0 || !isOwnCheck(n.List[i-1], env) {
						t.Errorf("the capability call %s does not follow its own frame's Check\nGo:\n%s", types.ExprString(call), stripDirectives(code))
					}
				}
			}
		}
		if body == nil {
			return true
		}
		goLoops++
		class := "decl"
		for i := len(stack) - 2; i >= 0; i-- {
			if _, ok := stack[i].(*ast.FuncLit); ok {
				class = "lit"
				break
			}
			if _, ok := stack[i].(*ast.FuncDecl); ok {
				break
			}
		}
		want, ok := c.want[class]
		if !ok {
			t.Errorf("a Go loop in a %s frame, which holds no source loop\nGo:\n%s", class, stripDirectives(code))
			return true
		}
		// The back-edge poll ends the iteration: the body's last statement
		// is the frame's Check, labelled when a continue jumps to it. Each
		// path that continues reaches it: no Go continue of this loop
		// remains, and every goto names its label. It never heads the body,
		// where it would separate the delivered element from the statement
		// that receives it (spec, stage 5a O1).
		polls, continues := 0, 0
		var gotos []string
		ast.Inspect(body, func(m ast.Node) bool {
			switch m := m.(type) {
			case *ast.FuncLit, *ast.ForStmt, *ast.RangeStmt:
				return false
			case *ast.ExprStmt:
				if isCheckOf(m, "") {
					polls++
				}
			case *ast.BranchStmt:
				switch m.Tok {
				case token.CONTINUE:
					continues++
				case token.GOTO:
					gotos = append(gotos, m.Label.Name)
				}
			}
			return true
		})
		end, label := ast.Stmt(nil), ""
		if len(body.List) > 0 {
			end = body.List[len(body.List)-1]
			if l, ok := end.(*ast.LabeledStmt); ok {
				end, label = l.Stmt, l.Label.Name
			}
		}
		switch {
		case want && (end == nil || !isOwnCheck(end, env)):
			t.Errorf("an impure %s frame's loop body does not end with its task's Check\nGo:\n%s", class, stripDirectives(code))
		case want && len(body.List) > 1 && isOwnCheck(body.List[0], env):
			t.Errorf("an impure %s frame's loop body starts with its task's Check, before the body receives the element\nGo:\n%s", class, stripDirectives(code))
		case want && continues > 0:
			t.Errorf("an impure %s frame's loop has %d continues, which skip its end-of-iteration Check\nGo:\n%s", class, continues, stripDirectives(code))
		case !want && polls > 0:
			t.Errorf("a pure %s frame's loop body has %d checks\nGo:\n%s", class, polls, stripDirectives(code))
		case !want && label != "":
			t.Errorf("a pure %s frame's loop body labels its end %s\nGo:\n%s", class, label, stripDirectives(code))
		}
		for _, g := range gotos {
			if g != label {
				t.Errorf("a %s frame's loop body jumps to %s, not to its end-of-iteration Check %q\nGo:\n%s", class, g, label, stripDirectives(code))
			}
		}
		return true
	})
	if c.markerEntry != nil && (c.markers < 1 || marked != c.markers) {
		t.Errorf("found %d marked entry frames, want %d (at least 1)", marked, c.markers)
	}
	if goLoops != source {
		t.Errorf("the Go has %d loops for %d source loops\nGo:\n%s", goLoops, source, stripDirectives(code))
	}
	if c.site && marks == 0 {
		t.Errorf("no capability call Mark in the Go")
	}
}

func i1HasEntryMarker(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	for _, s := range body.List {
		if d, ok := s.(*ast.DeclStmt); ok {
			if g, ok := d.Decl.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if v, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range v.Names {
							if id.Name == "entryMarker" {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

// i1CheckEntry: the marked frame's entry poll checks its own task and is
// its first statement, or, in a frame that owns a scope, the first after
// the scope's opening (NewScope of the frame's task and its deferred End),
// so it precedes every operand of the source body.
func i1CheckEntry(t *testing.T, body *ast.BlockStmt, env *ast.Ident, want, scope bool) {
	t.Helper()
	list := body.List
	if scope {
		if len(list) < 2 || !i1OpensScope(list[0], list[1], env) {
			t.Errorf("the marked frame owns a scope but does not open it first")
			return
		}
		list = list[2:]
	}
	got := len(list) > 0 && isOwnCheck(list[0], env)
	if got != want {
		t.Errorf("marked frame entry poll = %v, want %v (scope %v)", got, want, scope)
	}
}

// i1OpensScope reports the frame scope's opening: fs := NewScope(ENV.task)
// of the frame's own task, then defer fs.End(...).
func i1OpensScope(open, end ast.Stmt, env *ast.Ident) bool {
	as, ok := open.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !isFrameTask(call.Args[0], env) {
		return false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "NewScope" {
		return false
	}
	scope, ok := as.Lhs[0].(*ast.Ident)
	if !ok {
		return false
	}
	d, ok := end.(*ast.DeferStmt)
	if !ok {
		return false
	}
	sel, ok := d.Call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "End" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == scope.Name
}

// stmtCall is the call of the method name that s makes as a statement.
func stmtCall(s ast.Stmt, name string) *ast.CallExpr {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
		return call
	}
	return nil
}

// isCheckOf reports the statement task.Check(), of the task expression
// task when it is not "".
func isCheckOf(s ast.Stmt, task string) bool {
	call := stmtCall(s, "Check")
	if call == nil || len(call.Args) != 0 {
		return false
	}
	recv := call.Fun.(*ast.SelectorExpr).X
	return isTaskOf(recv) && (task == "" || types.ExprString(recv) == task)
}

// isTaskOf reports an environment's task, ENV.task.
func isTaskOf(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == envTask
}

func i1EnvParam(ft *ast.FuncType) *ast.Ident {
	for _, p := range ft.Params.List {
		ptr, ok := p.Type.(*ast.StarExpr)
		if !ok || len(p.Names) != 1 {
			continue
		}
		if name, ok := ptr.X.(*ast.Ident); ok && name.Name == "Env" {
			return p.Names[0]
		}
	}
	return nil
}

func isOwnCheck(s ast.Stmt, env *ast.Ident) bool {
	call := stmtCall(s, "Check")
	return call != nil && len(call.Args) == 0 && isFrameTask(call.Fun.(*ast.SelectorExpr).X, env)
}

// isFrameTask compares binding identities, not spelling. A with region
// copies its frame's Env, without changing the task. Only those local
// plain copies may resolve back to the innermost Env parameter.
func isFrameTask(x ast.Expr, env *ast.Ident) bool {
	if env == nil || env.Obj == nil || !isTaskOf(x) {
		return false
	}
	x = x.(*ast.SelectorExpr).X
	seen := map[*ast.Object]bool{}
	for {
		switch n := x.(type) {
		case *ast.ParenExpr:
			x = n.X
		case *ast.UnaryExpr:
			if n.Op != token.AND {
				return false
			}
			x = n.X
		case *ast.Ident:
			if n.Obj == env.Obj {
				return true
			}
			if n.Obj == nil || seen[n.Obj] {
				return false
			}
			seen[n.Obj] = true
			decl, ok := n.Obj.Decl.(*ast.AssignStmt)
			if !ok || decl.Tok != token.DEFINE || len(decl.Lhs) != 1 || len(decl.Rhs) != 1 {
				return false
			}
			copy, ok := decl.Rhs[0].(*ast.StarExpr)
			if !ok {
				return false
			}
			x = copy.X
		default:
			return false
		}
	}
}

// i1CheckPrimitives: the runtime's blocking primitives keep their own
// interruption points, whatever their caller polls (runtime.md, I1).
func i1CheckPrimitives(t *testing.T) {
	var src []byte
	for _, f := range Runtime() {
		if strings.HasSuffix(f.Path, "/queue.go") {
			src = f.Data
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "queue.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil {
			continue
		}
		switch fd.Name.Name {
		case "Push", "Pop", "Values":
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Check" {
						found[fd.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	for _, name := range []string{"Push", "Pop", "Values"} {
		if !found[name] {
			t.Errorf("the queue's %s has no interruption point of its own", name)
		}
	}
}

// The live witnesses: a task runs an unbounded loop or recursion in an
// impure frame of each class, its owner stops it, and the program ends.
// Without the frame's poll the loop never ends (a hang) and the recursion
// overflows the stack. The continue witness takes every back-edge through
// a continue; the order witness shows a capability call's operands run
// once, in source order, before the operation.

const i1LiveDefs = `import queue
import task
extern go strings.TrimSpace func hostTrim(s string) string
type Probe {
	Tick(),
}
type Silent {}
func Tick(_ Silent) {}
let Local = context { Probe: Silent }
func nonnegative(n int) bool { n >= 0 }
`

const i1LiveMain = `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() { work(ready) }
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 {
		delay += 1
	}
	t.Stop()
	Console.Println("stopped")
}
`

// The frame's clause follows its loop, so the loop's own poll is the only
// interruption point the frame reaches once it runs; a counting loop in
// main before the stop lets the task enter its loop first, so a missing
// poll hangs instead of losing a race to an earlier one.
var i1LiveLoops = map[string]string{
	"infinite": "let mut c = 0\nfor {\nc += 1\n}\n",
	"range":    "let mut c = 0\nfor i in 0..1000000000000 {\nc += i\n}\n",
	"continue": "let mut c = 0\nfor i in 0..1000000000000 {\nif i >= 0 {\ncontinue\n}\nc += i\n}\n",
	"cond":     "let mut c = 0\nfor c >= 0 {\nc += 1\n}\n",
	// Adapted from the reviewer's cap-in-cond probe in zz_review_test.go.
	// The condition helper is pure so it cannot hide a missing loop poll.
	"emitting-cond": "let mut c = 0\nfor nonnegative(c) {\nc += 1\n}\n",
}

func i1LivePrograms() map[string]string {
	out := map[string]string{}
	// Adapted from the reviewer's child-direct witness: stopping the child
	// must work while the parent remains live (M-d polls the wrong task).
	out["child-direct"] = i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() {
		try ready.Push(1)
		let mut c = 0
		for { c += 1 }
		with Local { Probe.Tick() }
		c
	}
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 { delay += 1 }
	t.Stop()
	Console.Println("stopped")
}
`
	work := func(decls, call string) string {
		return i1LiveDefs + decls + "func work(ready queue.Queue[int]) int fails queue.Closed {\n\ttry ready.Push(1)\n" + call + "}\n" + i1LiveMain
	}
	// The grandchild is stopped directly; its parent and grandparent run on
	// and return 2 and 3 (M1 of round 2: depth 3 polls its own task).
	out["grandchild-direct"] = i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let g = task.Go() {
		let p = task.Go() {
			let c = task.Go() {
				try ready.Push(1)
				let mut n = 0
				for { n += 1 }
				with Local { Probe.Tick() }
				n
			}
			try ready.Pop()
			let mut delay = 0
			for delay < 20000000 { delay += 1 }
			c.Stop()
			2
		}
		let r = try p.Wait()
		r + 1
	}
	let v = try g.Wait()
	if v == 3 {
		Console.Println("stopped")
	} else {
		Console.Println("the parent or grandparent stopped")
	}
}
`
	// A child's literal recurses through a variable: no loop, no capability
	// call and no declaration, so the literal's entry poll of its own task
	// is the only point the stop can reach.
	out["child-entry"] = i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() {
		try ready.Push(1)
		let mut f = func(n int) int { n }
		f = func(n int) int {
			if n < 2 { return n }
			f(n - 1) + f(n - 2)
		}
		f(90)
	}
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 { delay += 1 }
	t.Stop()
	Console.Println("stopped")
}
`
	// A child's loop in a with region polls the region's copy of the
	// child's environment, not the parent's.
	out["child-with"] = i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() {
		try ready.Push(1)
		with Local {
			let mut c = 0
			for { c += 1 }
			Probe.Tick()
			c
		}
	}
	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 { delay += 1 }
	t.Stop()
	Console.Println("stopped")
}
`
	// Each loop frame's clause is transparent and nonrecursive, so the
	// frame has no entry poll (i1CheckNoPollBeforeLoop asserts it).
	for kind, loop := range i1LiveLoops {
		out["decl-"+kind] = work("func spin() int {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\n", "spin()\n")
		out["literal-"+kind] = work("", "let local = func() int {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\nlocal()\n")
		out["task-"+kind] = work("", "let u = task.Go() {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\nu.Wait()\n")
		out["provider-"+kind] = work("type Worker {\n\tRun(n int) int,\n}\ntype Impl {}\nfunc Run(_ Impl, n int) int {\n"+loop+"with Local {\nProbe.Tick()\n}\nc\n}\nlet Work = context { Worker: Impl }\n", "with Work {\nWorker.Run(1)\n}\n")
	}
	out["capability-recursion"] = work("func spinCap() needs Probe {\nProbe.Tick()\nspinCap()\n}\n", "with Local {\nspinCap()\n}\n0\n")
	out["capability-mutual"] = work("func ping() needs Probe {\npong()\n}\nfunc pong() needs Probe {\nProbe.Tick()\nping()\n}\n", "with Local {\nping()\n}\n0\n")
	// Each recursion shape, impure by a clause on a branch that never runs:
	// no recursive path calls Probe, so only an entry poll stops the work
	// (recursion-self is the round 1 reviewer's impure-recursion witness).
	// A provider method has no row: its clause is its own with.
	for _, g := range i1Graphs {
		e := i1Effects[1]
		if g.provider {
			e = i1Effects[2]
		}
		if g.opaque {
			// The shape alone must force entry checks. A separate
			// capability effect could hide an opacity regression.
			e = i1Effects[0]
		}
		if g.name == "op-value" {
			// An operation value is the only re-entry edge. Its own
			// with gives an empty row and no reachable capability poll.
			e = i1Effects[2]
		}
		program := work(g.decls(e.row, e.clause), "with Local {\n"+fmt.Sprintf(g.call, 90)+"\n}\n")
		if g.ambient {
			program = strings.Replace(program, "func main() fails queue.Closed needs Console {", "func main() fails queue.Closed needs Console | Ambient {", 1)
		}
		out["recursion-"+g.name] = program
	}
	// Impure only by its own with: the row is empty.
	own := i1Effects[2]
	out["recursion-own-with"] = work(i1Graphs[0].decls(own.row, own.clause), fmt.Sprintf(i1Graphs[0].call, 90)+"\n")
	out["recursion-op-cycle"] = work(i1OpCycle, "with Work { left(90) }\n")
	// No capability is reached until after the unbounded loop. Its
	// footprint must travel leaf -> bridge -> spin across two SCC edges.
	out["transitive-components"] = work(`func leaf() needs Probe { Probe.Tick() }
func bridge() needs Probe { leaf() }
func spin() int needs Probe {
	let mut c = 0
	for { c += 1 }
	bridge()
	c
}
`, "with Local { spin() }\n")
	out["order"] = `type Pair {
	Join(a string, b string) string,
}
type Glue {}
func Join(_ Glue, a string, b string) string { a + b }
let Joined = context { Pair: Glue }
func step(s string) string needs Console {
	Console.Println(s)
	s
}
func main() needs Console {
	with Joined {
		Console.Println(Pair.Join(step("a"), step("b")))
	}
}
`
	return out
}

// i1CheckNoPollBeforeLoop: in a loop witness, the Go frame that holds the
// witness loop checks nothing before the loop, so the loop's own poll is
// the only point the frame reaches once it runs (main's counting loop is
// not a witness).
func i1CheckNoPollBeforeLoop(t *testing.T, o *lowered) {
	t.Helper()
	code := o.mainGo()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	check := func(body *ast.BlockStmt) {
		for i, s := range body.List {
			switch s.(type) {
			case *ast.ForStmt, *ast.RangeStmt:
				frames++
				for _, before := range body.List[:i] {
					if isCheckOf(before, "") {
						t.Errorf("the witness loop's frame checks before its loop\nGo:\n%s", stripDirectives(code))
					}
				}
				return
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil && n.Name.Name != "main_1" {
				check(n.Body)
			}
		case *ast.FuncLit:
			check(n.Body)
		}
		return true
	})
	if frames != 1 {
		t.Errorf("found %d witness loop frames, want 1\nGo:\n%s", frames, stripDirectives(code))
	}
}

func i1Live(t *testing.T) {
	progs := i1LivePrograms()
	var lowered []*lowered
	names := map[string]string{}
	i := 0
	for name, src := range progs {
		pkg := fmt.Sprintf("live%d", i)
		i++
		names[name] = pkg
		o := mustLower(t, pkg, src)
		for _, class := range []string{"decl-", "literal-", "task-", "provider-"} {
			if strings.HasPrefix(name, class) {
				t.Run("no-entry/"+name, func(t *testing.T) { i1CheckNoPollBeforeLoop(t, o) })
			}
		}
		lowered = append(lowered, o)
	}
	exes := buildAll(t, lowered, true)
	if exes == nil {
		return
	}
	for name, pkg := range names {
		want := "stopped\n"
		if name == "order" {
			want = "a\nb\nab\n"
		}
		exe := exes[pkg]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			probeRan(exe)
			run := exec.CommandContext(ctx, exe)
			var combined bytes.Buffer
			run.Stdout, run.Stderr = &combined, &combined
			err := runProgram(t, run)
			out := combined.Bytes()
			if ctx.Err() != nil {
				t.Fatalf("the program did not end within 60s: the stop never reached the frame\n%s", progs[name])
			}
			if err != nil || string(out) != want {
				t.Fatalf("run: %v\n%s\nwant %q\n%s", err, firstLines(string(out), 20), want, progs[name])
			}
		})
	}
}
