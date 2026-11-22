package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// flowTemplates are the S108 messages of the ownership flow that
// compiler/check/diag reports (OWN-WALK; resources R4, R10, R11, R15-R17,
// R20, R21, R42, R43; ownership.go): a use after a hand-off or a maybe
// hand-off, a hand-off from a block passed to a call, a lent parameter
// handed off, a lend and a hand-off in one call, a dropped or `_` resource,
// a replace from a block, a package let handed off or named by a with's
// resource provider, a for-in's lent element, an element or a field
// handed off, a field taken in place (used again, or its record used
// whole) and a yielded root;
// and each of those on a value of a type parameter marked own, which
// names the mark, and such a value dropped at the end of its scope
// (MODES-OWN-BODY; takes.go ownSite).
var flowTemplates = []*regexp.Regexp{
	regexp.MustCompile(`^\S+ was handed off at \d+:\d+ \(.*\) and cannot be used after that \(resources R15, R20\)`),
	regexp.MustCompile(`^\S+ may have been handed off at \d+:\d+ \(.*\) on one path, and cannot be used after that \(resources R21\)`),
	regexp.MustCompile(`^.+ cannot hand off \S+ \(.*\): \S+ belongs to the enclosing function, which releases it`),
	regexp.MustCompile(`^\S+ is lent to \S+, so it cannot hand \S+ off \(.*\) \(resources R10, R17\)`),
	regexp.MustCompile(`^\S+ is lent to \S+ here and handed off at \d+:\d+ \(.*\) in the same call \(resources R17\)`),
	regexp.MustCompile(`^the .+ this produces is discarded, and a resource has one owner`),
	regexp.MustCompile(`^the .+ bound to _ is discarded, and a resource has one owner`),
	regexp.MustCompile(`^\S+ belongs to the enclosing function; .+ cannot replace it \(resources R4\)`),
	regexp.MustCompile(`^\S+ is a package let, which has no owner to hand it off`),
	regexp.MustCompile(`^this resource provider names the package let \S+, which has no owner to hand it to the with`),
	regexp.MustCompile(`^the yielded resource is rooted `),
	regexp.MustCompile(`^a field of \S+ was taken at \d+:\d+, so \S+ cannot be used whole \(resources R39, Gap 38\)`),
	regexp.MustCompile(`^\S+ was taken in place \(on this path or another\) and cannot be (used after that|taken again)`),
	regexp.MustCompile(`^\S+ has its own Close, which runs before its fields are released`),
	regexp.MustCompile(`^.+ cannot take \S+ \(.*\): \S+ belongs to the enclosing function`),
	regexp.MustCompile(`^\S+ is lent to \S+, so its field \S+ cannot be taken`),
	regexp.MustCompile(`^\S+ is lent from \S+, so it cannot be handed off \(.*\) \(resources R35, Gap 36\)`),
	regexp.MustCompile(`^\S+\[…\] cannot be handed off: it is an element of \S+, lent by the index`),
	regexp.MustCompile(`^\S+ cannot be handed off: it is a field of \S+; a field cannot be moved out of its record`),
	regexp.MustCompile(`, and .+ is marked own, so its value has one owner as a resource's has \(types\.md, Generics 9\)$`),
	regexp.MustCompile(`^\S+ is lent and handed off \(.*\); write: `),
}

// goFlow is the Go checker's diagnostics of ds that compiler/check/diag's
// flow reports, "FILE OFFSET LENGTH S108 MESSAGE" in the checker's order.
func goFlow(file string, ds []check.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Code != check.CodeType {
			continue
		}
		for _, re := range flowTemplates {
			if re.MatchString(d.Message) {
				out = append(out, fmt.Sprintf("%s %d %d %s %s", file, d.Offset, d.Length, d.Code, d.Message))
				break
			}
		}
	}
	return out
}

// TestCompilerFlowAgreesWithChecker is OWN-WALK's agreement: the
// ownership flow's S108s compiler/check/diag reports (exprdump -corpus,
// its W lines) are the Go checker's, text, position and order, over
//   - testdata/flow: a fixture per message, and the forms the walk reads
//     (a value if, match and catch, a with's block, a loop's fixpoint, a
//     short circuit, a lent parameter's signature to write); each file's
//     "//want: flow" lines are the Go checker's, so a fixture that drifts
//     from the rule fails too;
//   - a seeded property: a body drawn from the Go checker's model grammar
//     (ownership_model_test.go ofGen, its core: lets, lends, Closes, takes,
//     assignments, temporaries and drops, a lend and a take in one call,
//     if, match, loops with break and continue, return, try, a short
//     circuit, a block passed to run, a catch, a value if or match and a
//     package let handed off).
//
// Every corpus folder's W lines are among the Go checker's diagnostics
// (TestCompilerExprAgreesWithChecker).
//
// A failing seed prints the source, both outputs and the replay command.
//
// It builds Go because exprdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerFlowAgreesWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	var cases []ownCase
	add := func(name, src string, seed uint32) []string {
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("%s: does not parse: %v\n%s", name, ds, src)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		want := goFlow("p.ergo", ds)
		cases = append(cases, ownCase{name: name, src: src, want: want, ext: ext, foreign: foreign, seed: seed})
		return want
	}
	fixtures, err := filepath.Glob(filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "flow", "*.ergo"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no testdata/flow fixture: %v", err)
	}
	for _, p := range fixtures {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		want := add(rel(p), src, 0)
		var lines []int
		for _, w := range want {
			off, _ := strconv.Atoi(strings.Fields(w)[1])
			l, _ := lineCol(src, off)
			lines = append(lines, l)
		}
		var marked []int
		for i, l := range strings.Split(src, "\n") {
			if strings.Contains(l, "//want: flow") {
				marked = append(marked, i+1)
			}
		}
		if fmt.Sprint(lines) != fmt.Sprint(marked) {
			t.Errorf("%s: the Go checker's flow S108s are on lines %v, the fixture marks %v", rel(p), lines, marked)
		}
	}
	seeds, reported := 0, 0
	for seed := base; seed < base+300; seed++ {
		if len(add(fmt.Sprintf("seed %d", seed), flowProgram(seed), seed)) > 0 {
			reported++
		}
		seeds++
	}
	if reported == 0 || reported == seeds {
		t.Fatalf("the Go checker reports a flow S108 in %d of %d seeds: the draw reaches one side only", reported, seeds)
	}
	tmp := t.TempDir()
	var manifest []string
	for i, c := range cases {
		dir := filepath.Join(tmp, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f, e, fo := filepath.Join(dir, "p.ergo"), filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, f, []string{strings.TrimSuffix(c.src, "\n")})
		writeLines(t, e, c.ext)
		writeLines(t, fo, c.foreign)
		manifest = append(manifest, strings.Join([]string{"main", e, fo, f}, " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, exprDump(t, ctx), "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(cases) && strings.HasPrefix(l, "W "):
			got[at] = append(got[at], l[2:])
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	failed := 0
	for i, c := range cases {
		if strings.Join(c.want, "\n") == strings.Join(got[i], "\n") {
			continue
		}
		failed++
		if failed > 5 {
			continue
		}
		msg := fmt.Sprintf("%s: flow diagnostics differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		if c.seed != 0 {
			msg += fmt.Sprintf("\n--- source\n%s\nreplay: %s=%d go test -count=1 -run '^TestCompilerFlowAgreesWithChecker$' ./internal/selfhostcheck (the first seed is the failing one)", c.src, seedVariable, c.seed)
		}
		t.Error(msg)
	}
	if failed > 0 {
		t.Errorf("%d of %d cases differ", failed, len(cases))
	}
	t.Logf("%d fixtures and %d seeds (%d with a flow S108) compared", len(fixtures), seeds, reported)
}

// flowPrelude is the declarations every drawn body uses: the Go model's
// (ownership_model_test.go ofPrelude).
const flowPrelude = `import queue

type Res { id int } implements Closer, Closes, Store

type Bad {}

func Close(r Res) {}

func open(n int) Res { Res{id: n} }

func lend(r Res) {}

func held(r Res) bool { true }

func keep(r own Res) {}

func take(r own Res) int { 0 }

func both(r Res, n int) {}

func bothInt(n int, m int) {}

func fail(n int) int fails Bad {
	if n > 5 {
		return Bad
	}
	n
}

type Worse {}

func failBW(n int) int fails Bad | Worse {
	if n > 5 {
		return Bad
	}
	if n > 3 {
		return Worse
	}
	n
}

func failTake(n int, r own Res) int fails Bad {
	if n > 5 {
		keep(r)
		return Bad
	}
	keep(r)
	n
}

func cond(n int) bool { n > 2 }

func run(f func()) { f() }

type Box { r Res, n int }

type Empty {}

type Held = Res | Empty

type Closes { ..Closer }

type Store { Get() int }

func Get(r Res) int { r.id }

func lendBox(b Box) {}

func keepBox(b own Box) {}

func lendCloser(c Closes) {}

let shared = Res{id: 0}

func newQueue() queue.Queue[Res] { queue.New[Res](2) }

`

// The statements of a drawn body (ofGen's core).
const (
	fDecl = iota
	fDeclMove
	fLend
	fClose
	fKeep
	fAssign
	fAssignMove
	fTemp
	fDiscard
	fDiscardLet
	fBoth
	fIf
	fMatch
	fLoop
	fBreak
	fContinue
	fReturn
	fTry
	fShort
	fRun
	fCatch
	fValue
	fShared
	// Aggregates, containers, unions, conversions, with providers and
	// queues (ofGen's A4 kinds): a Box{r Res, n int}, a []Res, a Held =
	// Res | Empty, a queue.Queue[Res].
	fBoxNew     // let bN = Box{r: Y, n: 1}
	fBoxLend    // lendBox(B)
	fBoxKeep    // keepBox(B)
	fFieldClose // B.r.Close(): a field taken in place
	fFieldLend  // lend(B.r)
	fFieldKeep  // keep(B.r): a field moved out (R39)
	fListNew    // let lN = []Res{Y}
	fIndexLend  // lend(L[0])
	fIndexClose // L[0].Close(): a take by index (R35)
	fForIn      // for eN in L { keep(eN) } or { lend(eN) }: lent elements
	fHeldNew    // let hN Held = Y
	fMatchHeld  // match H { case Res as vN: keep(vN) or lend(vN); case Empty: }
	fWithMove   // with context { Store: Y } { … }: Y moves into the with
	fConvert    // lendCloser(Y): a conversion to an operation type moves
	fLitLend    // lendBox(Box{r: Y, n: 0}): a lent literal moves its parts
	fWithShared // with context { Store: shared } { … }: a package let
	fQueueNew   // let uN = newQueue()
	fQueueLoop  // for eN in U.Values() { … }: owned elements (Q7)
	fTail
)

// Where a value if or match stands, and what its arms yield (ofGen's v*).
const (
	vfLetBool = iota
	vfShort
	vfLend
	vfKeep
	vfLetRes
	vfDiscard
	vfForms
)

// A value arm's tail (ofGen's t*).
const (
	tfCond = iota
	tfTake
	tfName
	tfOpen
	tfNested
)

type flowStmt struct {
	kind       int
	x, y       string
	z          string // the binding of a for-in or a match arm
	alt        bool   // keep, not lend, the binding
	mut        bool
	op         string
	left, temp bool
	form       int
	match      bool
	tk         int
	body, els  []*flowStmt
}

type flowVar struct {
	name  string
	mut   bool
	depth int
	kind  string // "" a Res, "box", "list", "held", "queue"
}

type flowGen struct {
	r          *rand.Rand
	n          int
	vars       []flowVar
	bare, lead bool
	// generic draws a generic body whose values are of a type parameter
	// marked own (MODES-OWN-BODY): flowGenericPrelude, the core kinds.
	generic bool
}

// flowProgram is the program of seed: flowPrelude, then a drawn body; a
// third of the seeds draw a generic body over [Res own] instead.
func flowProgram(seed uint32) string {
	g := &flowGen{r: rand.New(rand.NewPCG(uint64(seed), 0x666c6f77)), generic: seed%3 == 0}
	return flowPrint(g.program(5), g.bare, g.generic)
}

// The forms of a try statement (flowStmt.form of an fTry).
const (
	tryOne      = iota // let _ = try fail(n)
	tryTwo             // let _ = try fail(fail(n)): two failures in one try
	tryTakes           // let _ = try failTake(fail(n), Y): Y handed off between them
	tryCatchArg        // try failTake(try failBW(n) catch { case Worse: 0 }, Y)
	tryCatchArm        // let _ = try failBW(n) catch { case Worse: … 0 }
	tryForms
)

// flowGenericPrelude is the declarations a generic body uses: the core
// callees over an own type parameter. keep hands its value back, so a
// keep in the body drops a value of the caller's T (not refused, plan
// 1.3) and the prelude itself has no diagnostic.
const flowGenericPrelude = `type Bad {}

func lend[T own](r T) {}

func held[T own](r T) bool { true }

func keep[T own](r own T) T { r }

func take[T own](r own T) int {
	let _ = keep(r)
	0
}

func both[T own](r T, n int) {}

func fail(n int) int fails Bad {
	if n > 5 {
		return Bad
	}
	n
}

type Worse {}

func failBW(n int) int fails Bad | Worse {
	if n > 5 {
		return Bad
	}
	if n > 3 {
		return Worse
	}
	n
}

func failTake[T own](n int, r own T) int fails Bad {
	if n > 5 {
		let _ = keep(r)
		return Bad
	}
	let _ = keep(r)
	n
}

func cond(n int) bool { n > 2 }

func run(f func()) { f() }

`

// flowGenericOff is the kinds a generic body does not draw: a Close, a
// package let, and the aggregates, containers, unions, conversions, with
// providers and queues of Res.
var flowGenericOff = map[int]bool{fClose: true, fShared: true, fBoxNew: true, fBoxLend: true, fBoxKeep: true, fFieldClose: true, fFieldLend: true, fFieldKeep: true, fListNew: true, fIndexLend: true, fIndexClose: true, fForIn: true, fHeldNew: true, fMatchHeld: true, fWithMove: true, fConvert: true, fLitLend: true, fWithShared: true, fQueueNew: true, fQueueLoop: true}

func (g *flowGen) program(size int) []*flowStmt {
	g.vars = []flowVar{{name: "p", depth: 1}, {name: "q", depth: 1}}
	var body []*flowStmt
	if g.r.IntN(3) == 0 {
		g.bare, g.lead, g.vars = true, true, nil
		body = []*flowStmt{g.stmt(1, 0, false)}
	}
	body = append(body, g.block(size, 1, 0, false)...)
	// A let mut never assigned is S110: write it as a let.
	assigned := map[string]bool{}
	var walk func([]*flowStmt, func(*flowStmt))
	walk = func(list []*flowStmt, f func(*flowStmt)) {
		for _, s := range list {
			f(s)
			walk(s.body, f)
			walk(s.els, f)
		}
	}
	walk(body, func(s *flowStmt) {
		if s.kind == fAssign || s.kind == fAssignMove {
			assigned[s.x] = true
		}
	})
	walk(body, func(s *flowStmt) { s.mut = s.mut && assigned[s.x] })
	return body
}

func (g *flowGen) pick() string { return g.pickKind("") }

// hasRes is whether a live Res name is there to pick.
func (g *flowGen) hasRes() bool {
	for _, v := range g.vars {
		if v.kind == "" {
			return true
		}
	}
	return false
}

// pickKind is a live name of a kind, or "" when there is none.
func (g *flowGen) pickKind(kind string) string {
	var names []string
	for _, v := range g.vars {
		if v.kind == kind {
			names = append(names, v.name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return names[g.r.IntN(len(names))]
}

func (g *flowGen) block(size, depth, nest int, inLoop bool) []*flowStmt {
	mark := len(g.vars)
	defer func() { g.vars = g.vars[:mark] }()
	var out []*flowStmt
	for range 1 + g.r.IntN(size) {
		out = append(out, g.stmt(depth, nest, inLoop))
	}
	return out
}

func (g *flowGen) fresh() string {
	g.n++
	return fmt.Sprintf("r%d", g.n)
}

func (g *flowGen) value(form, depth, nest int, inLoop bool) *flowStmt {
	s := &flowStmt{kind: fValue, form: form, match: g.r.IntN(2) == 0}
	s.body = g.arm(form, depth, nest, inLoop)
	s.els = g.arm(form, depth, nest, inLoop)
	return s
}

func (g *flowGen) arm(form, depth, nest int, inLoop bool) []*flowStmt {
	mark := len(g.vars)
	defer func() { g.vars = g.vars[:mark] }()
	var out []*flowStmt
	for range g.r.IntN(3) {
		out = append(out, g.stmt(depth, nest+1, inLoop))
	}
	t := &flowStmt{kind: fTail}
	if form == vfLetBool || form == vfShort {
		t.tk = []int{tfCond, tfTake, tfNested}[g.r.IntN(3)]
		if t.tk == tfNested && nest >= 2 {
			t.tk = tfTake
		}
		if t.tk == tfTake && !g.hasRes() {
			t.tk = tfCond
		}
	} else {
		t.tk = []int{tfName, tfOpen}[g.r.IntN(2)]
		if !g.hasRes() {
			t.tk = tfOpen
		}
	}
	switch t.tk {
	case tfTake, tfName:
		t.y = g.pick()
	case tfNested:
		t.body = []*flowStmt{g.value(vfLetBool, depth, nest+1, inLoop)}
	}
	return append(out, t)
}

func (g *flowGen) stmt(depth, nest int, inLoop bool) *flowStmt {
	for {
		k := g.r.IntN(fTail)
		if g.lead {
			g.lead = false
			k = []int{fIf, fMatch, fLoop, fCatch, fValue}[g.r.IntN(5)]
		}
		if g.generic && flowGenericOff[k] {
			continue
		}
		switch k {
		case fDeclMove, fLend, fClose, fKeep, fBoth, fShort, fBoxNew, fListNew, fHeldNew, fWithMove, fConvert, fLitLend:
			if !g.hasRes() {
				continue
			}
		}
		switch k {
		case fValue:
			if nest >= 3 {
				continue
			}
			form := g.r.IntN(vfForms)
			s := g.value(form, depth, nest, inLoop)
			if form == vfLetRes {
				s.x = g.fresh()
				g.vars = append(g.vars, flowVar{name: s.x, depth: depth})
			}
			return s
		case fBreak, fContinue, fReturn:
			continue
		case fIf, fMatch, fLoop, fRun, fCatch:
			if nest >= 3 {
				continue
			}
		case fTry:
			if depth > 1 {
				continue
			}
		case fAssign, fAssignMove:
			var muts []string
			for _, v := range g.vars {
				if v.mut && v.depth == depth {
					muts = append(muts, v.name)
				}
			}
			if len(muts) == 0 {
				continue
			}
			s := &flowStmt{kind: k, x: muts[g.r.IntN(len(muts))]}
			if k == fAssignMove {
				s.y = g.pick()
				if s.y == s.x {
					continue
				}
			}
			return s
		}
		s := &flowStmt{kind: k}
		switch k {
		case fTry:
			// One failing call, two in one try, a value handed off
			// between two failures, and a try around a catch that passes
			// on what its arms do not handle, inside a call or with an
			// arm of its own (the Go checker's onFailure and catch's
			// Partial).
			s.form = g.r.IntN(tryForms)
			if (s.form == tryTakes || s.form == tryCatchArg) && !g.hasRes() {
				s.form = tryTwo
			}
			if s.form == tryTakes || s.form == tryCatchArg {
				s.y = g.pick()
			}
			if s.form == tryCatchArm {
				if nest >= 3 {
					s.form = tryOne
				} else {
					s.body = g.block(3, depth, nest+1, inLoop)
				}
			}
		case fDecl, fDeclMove:
			if k == fDeclMove {
				s.y = g.pick()
			}
			s.x = g.fresh()
			s.mut = k == fDecl && g.r.IntN(3) == 0
			g.vars = append(g.vars, flowVar{name: s.x, mut: s.mut, depth: depth})
		case fLend, fClose, fKeep, fBoth:
			s.y = g.pick()
			if k == fBoth {
				s.temp = g.r.IntN(3) == 0
				s.left = !s.temp && g.r.IntN(2) == 0
			}
		case fShort:
			s.y = g.pick()
			s.op = []string{"&&", "||"}[g.r.IntN(2)]
			switch g.r.IntN(4) {
			case 0:
				s.left = true
			case 1:
				s.temp = true
			}
		case fIf:
			s.body = g.block(4, depth, nest+1, inLoop)
			if g.r.IntN(2) == 0 {
				s.body = append(s.body, g.leave(depth, inLoop))
			}
			if g.r.IntN(2) == 0 {
				s.els = g.block(4, depth, nest+1, inLoop)
			}
		case fMatch:
			s.body = g.block(3, depth, nest+1, inLoop)
			s.els = g.block(3, depth, nest+1, inLoop)
		case fLoop:
			s.body = g.block(4, depth, nest+1, true)
		case fCatch:
			s.body = g.block(3, depth, nest+1, inLoop)
		case fRun:
			s.body = g.block(3, depth+1, nest+1, false)
		case fQueueNew:
			g.n++
			s.x = fmt.Sprintf("u%d", g.n)
			g.vars = append(g.vars, flowVar{name: s.x, depth: depth, kind: "queue"})
		case fQueueLoop:
			if s.y = g.pickKind("queue"); s.y == "" {
				continue
			}
		case fBoxNew, fListNew, fHeldNew:
			s.y = g.pick()
			kind := map[int]string{fBoxNew: "box", fListNew: "list", fHeldNew: "held"}[k]
			g.n++
			s.x = fmt.Sprintf("%c%d", kind[0], g.n)
			g.vars = append(g.vars, flowVar{name: s.x, depth: depth, kind: kind})
		case fBoxLend, fBoxKeep, fFieldClose, fFieldLend, fFieldKeep:
			if s.y = g.pickKind("box"); s.y == "" {
				continue
			}
		case fIndexLend, fIndexClose, fForIn:
			if s.y = g.pickKind("list"); s.y == "" {
				continue
			}
		case fMatchHeld:
			if s.y = g.pickKind("held"); s.y == "" {
				continue
			}
		case fWithShared:
			s.y = "shared"
		case fWithMove, fConvert, fLitLend:
			s.y = g.pick()
		}
		if k == fForIn || k == fMatchHeld || k == fQueueLoop {
			g.n++
			prefix := "e"
			if k == fMatchHeld {
				prefix = "v"
			}
			s.z = fmt.Sprintf("%s%d", prefix, g.n)
			s.alt = g.r.IntN(2) == 0
		}
		return s
	}
}

func (g *flowGen) leave(depth int, inLoop bool) *flowStmt {
	ks := []int{}
	if depth == 1 {
		ks = append(ks, fReturn)
	}
	if inLoop {
		ks = append(ks, fBreak, fContinue)
	}
	if len(ks) == 0 {
		return &flowStmt{kind: fTemp}
	}
	return &flowStmt{kind: ks[g.r.IntN(len(ks))]}
}

// flowPrint writes body as the function body of `body` after the prelude
// (ofPrint's core). A bare body's function has no resource parameters.
func flowPrint(body []*flowStmt, bare, generic bool) string {
	var b strings.Builder
	if generic {
		b.WriteString(flowGenericPrelude)
	} else {
		b.WriteString(flowPrelude)
	}
	emit := func(depth int, s string) {
		b.WriteString(strings.Repeat("\t", depth))
		b.WriteString(s)
		b.WriteString("\n")
	}
	k := 0
	var value func(s *flowStmt, depth int, prefix, suffix string)
	var walk func(list []*flowStmt, depth int)
	walk = func(list []*flowStmt, depth int) {
		for _, s := range list {
			switch s.kind {
			case fDecl:
				k++
				m := ""
				if s.mut {
					m = "mut "
				}
				emit(depth, fmt.Sprintf("let %s%s = open(%d)", m, s.x, k))
			case fDeclMove:
				emit(depth, fmt.Sprintf("let %s = %s", s.x, s.y))
			case fLend:
				emit(depth, "lend("+s.y+")")
			case fClose:
				emit(depth, s.y+".Close()")
			case fKeep:
				emit(depth, "keep("+s.y+")")
			case fShared:
				emit(depth, "keep(shared)")
			case fBoxNew, fListNew, fHeldNew, fWithMove, fConvert, fLitLend, fWithShared:
				p := map[int]string{
					fBoxNew:     fmt.Sprintf("let %s = Box{r: ", s.x),
					fListNew:    fmt.Sprintf("let %s = []Res{", s.x),
					fHeldNew:    fmt.Sprintf("let %s Held = ", s.x),
					fWithMove:   "with context { Store: ",
					fWithShared: "with context { Store: ",
					fConvert:    "lendCloser(",
					fLitLend:    "lendBox(Box{r: ",
				}[s.kind]
				q := map[int]string{fBoxNew: ", n: 1}", fListNew: "}", fHeldNew: "", fWithMove: " } {", fWithShared: " } {", fConvert: ")", fLitLend: ", n: 0})"}[s.kind]
				emit(depth, p+s.y+q)
				if s.kind == fWithMove || s.kind == fWithShared {
					emit(depth+1, "let _ = Store.Get()")
					emit(depth, "}")
				}
			case fBoxLend, fBoxKeep, fFieldLend, fFieldKeep, fIndexLend:
				p := map[int]string{fBoxLend: "lendBox(", fBoxKeep: "keepBox(", fFieldLend: "lend(", fFieldKeep: "keep(", fIndexLend: "lend("}[s.kind]
				q := map[int]string{fBoxLend: ")", fBoxKeep: ")", fFieldLend: ".r)", fFieldKeep: ".r)", fIndexLend: "[0])"}[s.kind]
				emit(depth, p+s.y+q)
			case fFieldClose:
				emit(depth, s.y+".r.Close()")
			case fIndexClose:
				emit(depth, s.y+"[0].Close()")
			case fQueueNew:
				emit(depth, fmt.Sprintf("let %s = newQueue()", s.x))
			case fQueueLoop, fForIn:
				use := "lend("
				if s.alt {
					use = "keep("
				}
				over := s.y
				if s.kind == fQueueLoop {
					over += ".Values()"
				}
				emit(depth, fmt.Sprintf("for %s in %s {", s.z, over))
				emit(depth+1, use+s.z+")")
				emit(depth, "}")
			case fMatchHeld:
				use := "lend("
				if s.alt {
					use = "keep("
				}
				emit(depth, "match "+s.y+" {")
				emit(depth, "case Res as "+s.z+":")
				emit(depth+1, use+s.z+")")
				emit(depth, "case Empty:")
				emit(depth, "}")
			case fAssign:
				k++
				emit(depth, fmt.Sprintf("%s = open(%d)", s.x, k))
			case fAssignMove:
				emit(depth, s.x+" = "+s.y)
			case fTemp:
				k++
				emit(depth, fmt.Sprintf("lend(open(%d))", k))
			case fDiscard:
				k++
				emit(depth, fmt.Sprintf("open(%d)", k))
			case fDiscardLet:
				k++
				emit(depth, fmt.Sprintf("let _ = open(%d)", k))
			case fBoth:
				prefix, arg := "both(", s.y
				if s.left && !generic {
					prefix, arg = "bothInt(", arg+".id"
				}
				if s.temp {
					prefix += "if cond(n) { "
					arg += " } else { open(0) }"
				}
				emit(depth, prefix+arg+", take("+s.y+"))")
			case fIf:
				emit(depth, "if cond(n) {")
				walk(s.body, depth+1)
				if s.els != nil {
					emit(depth, "} else {")
					walk(s.els, depth+1)
				}
				emit(depth, "}")
			case fMatch:
				emit(depth, "match n {")
				emit(depth, "case 0:")
				walk(s.body, depth+1)
				emit(depth, "case _:")
				walk(s.els, depth+1)
				emit(depth, "}")
			case fLoop:
				emit(depth, "for _ in 0..n {")
				walk(s.body, depth+1)
				emit(depth, "}")
			case fBreak:
				emit(depth, "break")
			case fContinue:
				emit(depth, "continue")
			case fReturn:
				emit(depth, "return")
			case fTry:
				switch s.form {
				case tryOne:
					emit(depth, "let _ = try fail(n)")
				case tryTwo:
					emit(depth, "let _ = try fail(fail(n))")
				case tryTakes:
					emit(depth, "let _ = try failTake(fail(n), "+s.y+")")
				case tryCatchArg:
					emit(depth, "let _ = try failTake(try failBW(n) catch {")
					emit(depth, "case Worse:")
					emit(depth+1, "0")
					emit(depth, "}, "+s.y+")")
				case tryCatchArm:
					emit(depth, "let _ = try failBW(n) catch {")
					emit(depth, "case Worse:")
					walk(s.body, depth+1)
					emit(depth+1, "0")
					emit(depth, "}")
				}
			case fShort:
				switch {
				case s.temp:
					k++
					emit(depth, fmt.Sprintf("let _ = cond(n) %s held(open(%d))", s.op, k))
				case s.left:
					emit(depth, "let _ = take("+s.y+") > 0 "+s.op+" cond(n)")
				default:
					emit(depth, "let _ = cond(n) "+s.op+" take("+s.y+") > 0")
				}
			case fRun:
				emit(depth, "run() {")
				walk(s.body, depth+1)
				emit(depth, "}")
			case fCatch:
				emit(depth, "let _ = fail(n) catch {")
				emit(depth, "case Bad:")
				walk(s.body, depth+1)
				emit(depth+1, "0")
				emit(depth, "}")
			case fValue:
				prefix, suffix := "", ""
				switch s.form {
				case vfLetBool, vfDiscard:
					prefix = "let _ = "
				case vfShort:
					prefix, suffix = "let _ = cond(n) && (", ")"
				case vfLend:
					prefix, suffix = "lend(", ")"
				case vfKeep:
					prefix, suffix = "keep(", ")"
				case vfLetRes:
					prefix = "let " + s.x + " = "
				}
				value(s, depth, prefix, suffix)
			case fTail:
				switch s.tk {
				case tfCond:
					emit(depth, "cond(n)")
				case tfTake:
					emit(depth, "take("+s.y+") > 0")
				case tfName:
					emit(depth, s.y)
				case tfOpen:
					k++
					emit(depth, fmt.Sprintf("open(%d)", k))
				case tfNested:
					value(s.body[0], depth, "", "")
				}
			}
		}
	}
	value = func(s *flowStmt, depth int, prefix, suffix string) {
		if !s.match {
			emit(depth, prefix+"if cond(n) {")
			walk(s.body, depth+1)
			emit(depth, "} else {")
			walk(s.els, depth+1)
			emit(depth, "}"+suffix)
			return
		}
		emit(depth, prefix+"match n {")
		emit(depth, "case 0:")
		walk(s.body, depth+1)
		emit(depth, "case _:")
		walk(s.els, depth+1)
		emit(depth, "}"+suffix)
	}
	switch {
	case generic && bare:
		emit(0, "func body[Res own](n int, open func(int) Res) fails Bad {")
	case generic:
		emit(0, "func body[Res own](n int, p Res, q own Res, open func(int) Res) fails Bad {")
	case bare:
		emit(0, "func body(n int) fails Bad {")
	default:
		emit(0, "func body(n int, p Res, q own Res) fails Bad {")
	}
	walk(body, 1)
	emit(0, "}")
	b.WriteString("\nfunc main() {}\n")
	return b.String()
}
