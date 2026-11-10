package check

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOwnershipFlowModel (#197 L8 A2) is a model-based property of the
// ownership flow pass (ownership.go). It generates function bodies over a
// small grammar: acquire, a lend call, a move by let, an assignment (of a
// new value or a local), Close, a call of a function with a written takes,
// a lend and a take of one value in one call, a temporary, a discarded
// resource (as a statement and bound to _), if/else, match, for with break
// and continue, return, the failure edge of a try, a take in either
// operand of && or || (the right one runs on some paths only), an if or
// match used as a value (a let value, an operand of &&, a lent and a
// taken argument, a dropped value; arms with statements of their own and a
// tail that takes, names or makes a value, or nests another), and a
// trailing block (a frame of its own) that uses or hands off the
// enclosing locals; with a lent and a taken parameter. A reference abstract interpreter in the
// test, written from resources R4-R21 and R41-R43, computes the verdict
// (S108 positions) and the release plan (what each scope end, statement,
// assignment and exit edge releases, with drop flags); both must equal the
// checker's. A loop body runs in the reference to its plain fixpoint, with
// no memo. ERGO_SEED=<n> replays one seed; a failure prints the Ergo source
// and both results.
//
// Mutants it kills that no other test does (ledger: ownership.go): a join
// of owned and moved is owned; the loop body visited once; assignment
// does not release the old value; a temporary has no owner; a written
// takes call does not move; a lend counts as a move; a hand-off inside a
// trailing block admitted (the scope.OnExit rule); a lend and a take in
// one call admitted; a lent parameter handed off; a local held only for
// its release reported S110; the right operand of && or || walked as a
// path every run takes (batch-15 A2 M1: the value leaked when it did not
// run); the arms of a value if or match skipped, an arm's value moved
// wherever the value goes, and the temporary of a lent one owned on every
// path or on none (batch-15 A2 M1 repeat).
func TestOwnershipFlowModel(t *testing.T) {
	seeds := make([]uint64, 0, 400)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(400) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		ofYieldProjections(t, seed)
		ofYieldLifetimes(t, seed)
		g := &ofGen{r: rand.New(rand.NewPCG(seed, 0x197a2))}
		body := g.program(12)
		src, first := ofPrint(body)
		wantD, wantR := ofReference(body)
		c, diags := checkFor(src)
		var gotD []string
		for _, d := range diags {
			gotD = append(gotD, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Code))
		}
		gotR := ofReleases(c, first)
		slices.Sort(gotD)
		slices.Sort(wantD)
		if !slices.Equal(gotD, wantD) || !slices.Equal(gotR, wantR) {
			var full []string
			for _, d := range diags {
				full = append(full, d.String())
			}
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestOwnershipFlowModel ./internal/sketch/check)\nsource:\n%s\nchecker diagnostics: %v\nmodel diagnostics:   %v\n%s\nchecker releases: %v\nmodel releases:   %v",
				seed, seed, ofNumbered(src), gotD, wantD, strings.Join(full, "\n"), gotR, wantR)
		}
	}
}

// ofYieldProjections varies the result's root owner, not just the tail's
// spelling. A part of a block-local owner cannot be released safely by
// closing the part alone, so it must be refused; an enclosing root stays
// lent. This kills a2-yield-projection-root-ignored.
func ofYieldProjections(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x197a205))
	for origin := range 5 {
		arm := []string{
			"let b = box()\nb.R",
			"box().R",
			"outer.R",
			"let b = box()\n(with context {} { b }).R",
			"(if cond(n) { outer } else { outer }).R",
		}[origin]
		var x string
		switch rng.IntN(5) {
		case 0:
			x = "if cond(n) {\n" + arm + "\n} else { open(0) }"
		case 1:
			x = "match n {\ncase 0:\n" + arm + "\ncase _: open(0)\n}"
		case 2:
			x = "match (n, n) {\ncase (_, _):\n" + arm + "\n}"
		case 3:
			x = "with context {} {\n" + arm + "\n}"
		case 4:
			x = "tryOpen(n) catch {\ncase Bad:\n" + arm + "\n}"
		}
		src := ofPrelude + "type Box { R Res }\nfunc box() Box { Box{R: open(1)} }\nfunc tryOpen(n int) Res fails Bad { if n > 0 { return Bad }; open(n) }\nfunc body(n int, outer Box) { lend(" + x + ") }\nfunc main() {}\n"
		_, ds := checkFor(src)
		want := origin == 0 || origin == 1 || origin == 3
		if (len(ds) > 0) != want {
			t.Fatalf("projection seed %d origin %d: diagnostics %v, want refusal %v\nReplay: ERGO_SEED=%d go test -run '^TestOwnershipFlowModel$' ./internal/sketch/check\n%s", seed, origin, ds, want, seed, src)
		}
		for _, d := range ds {
			if d.Code != "S108" || !strings.Contains(d.Message, "owner ends before its consumer") {
				t.Fatalf("projection seed %d: unexpected %v\n%s", seed, ds, src)
			}
		}
	}
}

// ofYieldLifetimes executes the release plan, not the abstract lattice's
// prediction of that plan. Tokens retain their acquisition identity across
// result expressions. A consumer must use a live token, and every acquisition
// must be released once. This kills a2-yield-local-not-transferred: the old
// model agreed with the early arm release (batch-15 r3 M1).
func ofYieldLifetimes(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x197a204))
	next := 0
	consumer := rng.IntN(4) // lend, take, bind, short-circuit operand
	var value func(int) string
	value = func(depth int) string {
		if depth == 0 {
			if (consumer == 0 || consumer == 3) && rng.IntN(3) == 0 {
				return "outer"
			}
			if rng.IntN(2) == 0 {
				return "open(2)"
			}
			next++
			name := fmt.Sprintf("local%d", next)
			return "with context {} {\nlet " + name + " = open(2)\nlend(" + name + ")\n" + name + "\n}"
		}
		arm := func() string {
			next++
			name := fmt.Sprintf("local%d", next)
			if rng.IntN(2) == 0 {
				tail := name
				if rng.IntN(2) == 0 {
					tail = "with context {} { " + name + " }"
				}
				return "let " + name + " = open(2)\nlend(" + name + ")\n" + tail
			}
			return value(depth - 1)
		}
		switch rng.IntN(5) {
		case 0:
			return "if cond(n) {\n" + arm() + "\n} else {\n" + arm() + "\n}"
		case 1:
			return "match n {\ncase 0:\n" + arm() + "\ncase _:\n" + arm() + "\n}"
		case 2:
			return "match (n, n) {\ncase (_, _):\n" + arm() + "\n}"
		case 3:
			return "with context {} {\n" + arm() + "\n}"
		default:
			return "tryOpen(n) catch {\ncase Bad:\n" + arm() + "\n}"
		}
	}
	x := value(1 + rng.IntN(3))
	stmt := "lend(" + x + ")"
	switch consumer {
	case 1:
		stmt = "keep(" + x + ")"
	case 2:
		stmt = "let result = " + x + "\nlend(result)"
	case 3:
		stmt = "let _ = cond(n) && held(" + x + ")"
	}
	src := ofPrelude + "func tryOpen(n int) Res fails Bad { if n > 0 { return Bad }; open(n) }\nfunc body(n int) {\nlet outer = open(1)\n" + stmt + "\nlend(outer)\n}\nfunc main() {}\n"
	c, ds := checkFor(src)
	if len(ds) != 0 {
		t.Fatalf("yield seed %d: %v\n%s", seed, ds, src)
	}
	var body *syntax.BlockStmt
	for _, d := range c.pkg.Files()[0].Decls {
		if f, ok := d.(*syntax.FuncDecl); ok && f.Name.Name == "body" {
			body = f.Body
		}
	}
	// Each choice consumes one bit. The generated tree has at most three
	// nested choices, plus the short-circuit operand and catch success.
	for path := range 1 << 5 {
		type token struct{ releases int }
		var tokens []*token
		vars := map[*Var]*token{}
		temps := map[syntax.Expr]*token{}
		active := map[syntax.Expr]bool{}
		var events []string
		bits := path
		choose := func() bool { b := bits&1 != 0; bits >>= 1; return b }
		fail := func(msg string) {
			t.Fatalf("yield seed %d path %d: %s\nevents %v\nReplay: ERGO_SEED=%d go test -run '^TestOwnershipFlowModel$' ./internal/sketch/check\n%s", seed, path, msg, events, seed, src)
		}
		use := func(v *token) {
			if v != nil {
				events = append(events, "use")
				if v.releases != 0 {
					fail("use after release")
				}
			}
		}
		release := func(n syntax.Node) {
			for _, r := range c.pkg.Info.Releases[n] {
				v := vars[r.Var]
				if r.Temp != nil {
					v = temps[r.Temp]
					if r.Flag && !active[r.Temp] {
						continue
					}
				}
				if v == nil {
					fail(fmt.Sprintf("release without acquisition at %T", n))
				}
				events = append(events, fmt.Sprintf("release at %T", n))
				v.releases++
				if v.releases != 1 {
					fail("double release")
				}
			}
		}
		var expr func(syntax.Expr) *token
		var block func(syntax.Node, []syntax.Stmt) *token
		// evalResult associates the selected token with the expression's
		// result place before its arm closes. An enclosing name is lent:
		// it has an owner before evaluation starts. A new/local result has
		// no such owner and belongs to the consumer's temporary instead.
		result := func(x syntax.Expr, key syntax.Node, list []syntax.Stmt) *token {
			before := len(tokens)
			var v *token
			for i, s := range list {
				if i == len(list)-1 {
					var tail syntax.Expr
					if es, ok := s.(*syntax.ExprStmt); ok {
						tail = es.X
					} else if e, ok := s.(syntax.Expr); ok {
						tail = e
					}
					if tail != nil {
						v = expr(tail)
						temps[x] = v
						active[x] = slices.Contains(tokens[before:], v)
						release(s)
						continue
					}
				}
				block(nil, []syntax.Stmt{s})
			}
			release(key)
			return v
		}
		expr = func(x syntax.Expr) *token {
			switch x := x.(type) {
			case *syntax.Ident:
				v, _ := c.pkg.Info.Uses[x].(*Var)
				out := vars[v]
				use(out)
				return out
			case *syntax.ParenExpr:
				return expr(x.X)
			case *syntax.CallExpr:
				k := c.pkg.Info.Calls[x]
				if k != nil && k.Kind == CallWith {
					return result(x, x.Trailing, x.Trailing.List)
				}
				name := calleeText(x)
				if name == "open" || name == "tryOpen" {
					v := &token{}
					tokens = append(tokens, v)
					temps[x] = v
					active[x] = true
					events = append(events, "acquire")
					return v
				}
				for _, a := range x.Args {
					v := expr(a)
					use(v)
					if name == "keep" && v != nil {
						v.releases++
						events = append(events, "take/release")
					}
				}
			case *syntax.IfStmt:
				if choose() {
					return result(x, x.Body, x.Body.List)
				}
				b := x.Else.(*syntax.BlockStmt)
				return result(x, b, b.List)
			case *syntax.SwitchStmt:
				i := 0
				if len(x.Clauses) > 1 && choose() {
					i = 1
				}
				cl := x.Clauses[i]
				return result(x, cl, cl.Body)
			case *syntax.TypeSwitchStmt:
				cl := x.Clauses[0]
				return result(x, cl, cl.Body)
			case *syntax.CatchExpr:
				if choose() {
					a := x.Arms[0]
					return result(x, a, a.Body)
				}
				v := expr(x.X)
				temps[x] = v
				active[x] = true
				return v
			case *syntax.BinaryExpr:
				expr(x.X)
				if choose() {
					expr(x.Y)
				}
			}
			return nil
		}
		block = func(key syntax.Node, list []syntax.Stmt) *token {
			for _, s := range list {
				switch s := s.(type) {
				case *syntax.DeclStmt:
					for _, spec := range s.Decl.Specs {
						v := expr(spec.Values[0])
						release(spec)
						obj, _ := c.pkg.Info.Defs[spec.Names[0]].(*Var)
						vars[obj] = v
					}
				case *syntax.ExprStmt:
					expr(s.X)
					release(s)
				}
			}
			if key != nil {
				release(key)
			}
			return nil
		}
		block(body, body.List)
		for _, v := range tokens {
			if v.releases != 1 {
				fail("acquired token not released exactly once")
			}
		}
	}
}

// TestOwnershipFlowWorkIsLinear bounds the flow pass's work: n copies of a
// body that nests loops, branches, temporaries and blocks cost n times one
// copy, counted in visited nodes and copied state slots (no timing). It
// kills the mutant "a loop's fixpoint not reused for an unchanged entry"
// (the work of nested loops grows as d²).
func TestOwnershipFlowWorkIsLinear(t *testing.T) {
	unit := "\tfor _ in 0..n {\n\t\tfor _ in 0..n {\n\t\t\tfor _ in 0..n {\n\t\t\t\tif cond(n) {\n\t\t\t\t\tlet r = open(1)\n\t\t\t\t\tlend(open(2))\n\t\t\t\t\tkeep(r)\n\t\t\t\t\tcontinue\n\t\t\t\t}\n\t\t\t\tlend(q)\n\t\t\t}\n\t\t}\n\t}\n"
	steps := func(n int) int {
		c, diags := checkFor(ofPrelude + "func body(n int, p Res, q Res) fails Bad takes q {\n" + strings.Repeat(unit, n) + "}\n\nfunc main() {}\n")
		if len(diags) != 0 {
			t.Fatalf("diagnostics: %v", diags)
		}
		return c.ownSteps
	}
	s8, s16, s32 := steps(8), steps(16), steps(32)
	if d1, d2 := s16-s8, s32-s16; d1 <= 0 || d2 != 2*d1 {
		t.Fatalf("flow work not linear: %d, %d, %d at 8, 16, 32", s8, s16, s32)
	}
	t.Logf("flow work %d, %d, %d at 8, 16, 32", s8, s16, s32)
	// Loops nested d deep, the innermost handing off a local of the
	// outermost frame on one path: each loop runs its body to its fixpoint
	// once, and an enclosing loop's later pass reuses it (the loop memo),
	// so the work is linear in d, not d² (a rerun per enclosing pass) nor
	// 2^d (no memo).
	nested := func(d int) int {
		// The innermost hand-off changes every loop's entry state (and is
		// S108 at its second iteration); the diagnostics do not matter here.
		src := "\tlet r = open(1)\n" + strings.Repeat("\tfor _ in 0..n {\n", d) + "\tif cond(n) {\n\tkeep(r)\n\t}\n" + strings.Repeat("\t}\n", d)
		c, _ := checkFor(ofPrelude + "func body(n int, p Res, q Res) fails Bad takes q {\n" + src + "}\n\nfunc main() {}\n")
		return c.ownSteps
	}
	n8, n16 := nested(8), nested(16)
	if n16 > 2*n8 {
		t.Fatalf("nested-loop work grows faster than linear: %d at depth 8, %d at depth 16", n8, n16)
	}
	t.Logf("nested-loop work %d, %d at depth 8, 16", n8, n16)
}

// TestOwnershipMessages fixes the text of each flow diagnostic (rows for
// text only; the property owns the verdicts). Its return row kills the
// mutant "return does not move", which the property cannot reach: its
// generated bodies return no value.
func TestOwnershipMessages(t *testing.T) {
	for _, row := range []struct{ body, want string }{
		{"let a = open(1)\n\tlet b = a\n\tlend(a)\n\tkeep(b)", "a was handed off at +2:10 (let moves it) and cannot be used after that"},
		{"let a = open(1)\n\tif cond(n) {\n\t\ta.Close()\n\t}\n\tlend(a)", "a may have been handed off at +3:3 (Close takes it) on one path"},
		{"keep(p)", "p is lent to body, so it cannot hand p off (keep takes r)"},
		{"let a = open(1)\n\trun() {\n\t\ta.Close()\n\t}", "the block passed to run cannot hand off a (Close takes it): a belongs to the enclosing function"},
		{"let a = open(1)\n\tboth(a, take(a))", "a is lent to both here and handed off at +2:15 (take takes r) in the same call"},
		{"open(1)", "the Res this produces is discarded"},
		{"let _ = open(1)", "the Res this produces is discarded"},
	} {
		// +k is the body's k-th line.
		base := strings.Count(ofPrelude, "\n") + 1
		row.want = regexp.MustCompile(`\+(\d+):`).ReplaceAllStringFunc(row.want, func(s string) string {
			k, _ := strconv.Atoi(s[1 : len(s)-1])
			return fmt.Sprintf("%d:", base+k)
		})
		_, diags := checkFor(ofPrelude + "func body(n int, p Res, q Res) fails Bad takes q {\n\t" + row.body + "\n}\n\nfunc main() {}\n")
		if len(diags) != 1 || !strings.Contains(diags[0].Message, row.want) {
			t.Errorf("%q: got %v, want one S108 containing %q", row.body, diags, row.want)
		}
	}
	// A return hands off (R10): the generated bodies return no value.
	_, diags := checkFor(ofPrelude + "func give(p Res) Res {\n\treturn p\n}\n\nfunc main() {}\n")
	if want := "p is lent to give, so it cannot hand p off (it is returned)"; len(diags) != 1 || !strings.Contains(diags[0].Message, want) {
		t.Errorf("return p: got %v, want one S108 containing %q", diags, want)
	}
}

const ofPrelude = `type Res { id int }

type Bad {}

func Close(r Res) {}

func open(n int) Res { Res{id: n} }

func lend(r Res) {}

func held(r Res) bool { true }

func keep(r Res) takes r {}

func take(r Res) int takes r { 0 }

func both(r Res, n int) {}

func bothInt(n int, m int) {}

func fail(n int) int fails Bad {
	if n > 5 {
		return Bad
	}
	n
}

func cond(n int) bool { n > 2 }

func run(f func()) { f() }

`

const (
	ofDecl = iota
	ofDeclMove
	ofLend
	ofClose
	ofKeep
	ofAssign
	ofAssignMove
	ofTemp
	ofDiscard
	ofDiscardLet
	ofBoth
	ofIf
	ofMatch
	ofLoop
	ofBreak
	ofContinue
	ofReturn
	ofTry
	ofShort
	ofRun
	ofValue // an if or match used as a value (batch-15 A2 M1)
	ofTail  // the last line of a value arm: where the arm's value comes from
)

// Where an ofValue's if or match stands (form) and what its arms yield.
const (
	vLetBool = iota // let _ = IF, arms of bool type
	vShort          // let _ = cond(n) && (IF): an operand that may not run
	vLend           // lend(IF): a lent Res, a temporary of the statement
	vKeep           // keep(IF): a taken Res, each arm's value moves
	vLetRes         // let rN = IF: the value moves into rN
	vDiscard        // let _ = IF of Res: each arm's new value is discarded
	vForms
)

// The tail of a value arm (an ofTail's tk).
const (
	tCond   = iota // cond(n)
	tTake          // take(y) > 0: hands y off
	tName          // y: moved, lent or dropped as the value is
	tOpen          // open(k): a new value, owned where the value goes
	tNested        // a nested if or match (body[0], an ofValue), bool only
)

type ofStmt struct {
	kind      int
	x, y      string // x: the declared or assigned name; y: the operand
	mut       bool
	op        string // ofShort: && or ||
	left      bool   // ofShort: the take is the left operand, which always runs
	temp      bool   // ofShort: the right operand lends a temporary instead
	form      int    // ofValue: vLetBool…
	match     bool   // ofValue: a match instead of an if
	tk        int    // ofTail: tCond…
	body, els []*ofStmt
	// Filled by ofPrint: the statement's line, the else line, and columns.
	line, elseLine int
	armLine        int // ofValue match: the first clause's line
	col, col2      int // y's column (both: the lent y); the taken y in both
}

type ofVar struct {
	name  string
	mut   bool
	depth int
}

type ofGen struct {
	r    *rand.Rand
	n    int
	vars []ofVar
}

func (g *ofGen) program(size int) []*ofStmt {
	g.vars = []ofVar{{name: "p", depth: 1}, {name: "q", depth: 1}}
	body := g.block(size, 1, 0, false)
	// A let mut that is never assigned is S110: write it as a let.
	assigned := map[string]bool{}
	var walk func([]*ofStmt, func(*ofStmt))
	walk = func(list []*ofStmt, f func(*ofStmt)) {
		for _, s := range list {
			f(s)
			walk(s.body, f)
			walk(s.els, f)
		}
	}
	walk(body, func(s *ofStmt) {
		if s.kind == ofAssign || s.kind == ofAssignMove {
			assigned[s.x] = true
		}
	})
	walk(body, func(s *ofStmt) { s.mut = s.mut && assigned[s.x] })
	return body
}

func (g *ofGen) pick() string { return g.vars[g.r.IntN(len(g.vars))].name }

// block generates up to size statements at frame depth, inLoop loops
// deep. The last statement of an if body may leave (return, break or
// continue).
func (g *ofGen) block(size, depth, nest int, inLoop bool) []*ofStmt {
	mark := len(g.vars)
	defer func() { g.vars = g.vars[:mark] }()
	var out []*ofStmt
	n := 1 + g.r.IntN(size)
	for range n {
		out = append(out, g.stmt(depth, nest, inLoop))
	}
	return out
}

func (g *ofGen) fresh() string {
	g.n++
	return fmt.Sprintf("r%d", g.n)
}

// value is an if or match used as a value, at form; a bool one may nest
// another in an arm's tail.
func (g *ofGen) value(form, depth, nest int, inLoop bool) *ofStmt {
	s := &ofStmt{kind: ofValue, form: form, match: g.r.IntN(2) == 0}
	s.body = g.arm(form, depth, nest, inLoop)
	s.els = g.arm(form, depth, nest, inLoop)
	return s
}

// arm is a value arm: up to two statements, then its tail.
func (g *ofGen) arm(form, depth, nest int, inLoop bool) []*ofStmt {
	mark := len(g.vars)
	defer func() { g.vars = g.vars[:mark] }()
	var out []*ofStmt
	for range g.r.IntN(3) {
		out = append(out, g.stmt(depth, nest+1, inLoop))
	}
	t := &ofStmt{kind: ofTail}
	if form == vLetBool || form == vShort {
		t.tk = []int{tCond, tTake, tNested}[g.r.IntN(3)]
		if t.tk == tNested && nest >= 2 {
			t.tk = tTake
		}
	} else {
		t.tk = []int{tName, tOpen}[g.r.IntN(2)]
	}
	switch t.tk {
	case tTake, tName:
		t.y = g.pick()
	case tNested:
		t.body = []*ofStmt{g.value(vLetBool, depth, nest+1, inLoop)}
	}
	return append(out, t)
}

func (g *ofGen) stmt(depth, nest int, inLoop bool) *ofStmt {
	for {
		k := g.r.IntN(ofValue + 1)
		switch k {
		case ofValue:
			if nest >= 3 {
				continue
			}
			form := g.r.IntN(vForms)
			s := g.value(form, depth, nest, inLoop)
			if form == vLetRes {
				s.x = g.fresh()
				g.vars = append(g.vars, ofVar{name: s.x, depth: depth})
			}
			return s
		case ofBreak, ofContinue, ofReturn:
			continue // only as an if body's last statement
		case ofIf, ofMatch, ofLoop, ofRun:
			if nest >= 3 {
				continue
			}
		case ofTry:
			if depth > 1 {
				continue // a trailing block of run has no failure row
			}
		case ofAssign, ofAssignMove:
			var muts []string
			for _, v := range g.vars {
				if v.mut && v.depth == depth {
					muts = append(muts, v.name)
				}
			}
			if len(muts) == 0 {
				continue
			}
			s := &ofStmt{kind: k, x: muts[g.r.IntN(len(muts))]}
			if k == ofAssignMove {
				s.y = g.pick()
				if s.y == s.x {
					continue
				}
			}
			return s
		}
		s := &ofStmt{kind: k}
		switch k {
		case ofDecl, ofDeclMove:
			if k == ofDeclMove {
				s.y = g.pick()
			}
			s.x = g.fresh()
			s.mut = g.r.IntN(3) == 0
			if k == ofDeclMove {
				s.mut = false
			}
			g.vars = append(g.vars, ofVar{name: s.x, mut: s.mut, depth: depth})
		case ofLend, ofClose, ofKeep, ofBoth:
			s.y = g.pick()
			if k == ofBoth {
				s.temp = g.r.IntN(3) == 0
				s.left = !s.temp && g.r.IntN(2) == 0 // a crossing field, not a lender
			}
		case ofShort:
			s.y = g.pick()
			s.op = []string{"&&", "||"}[g.r.IntN(2)]
			switch g.r.IntN(4) {
			case 0:
				s.left = true
			case 1:
				s.temp = true
			}
		case ofIf:
			s.body = g.block(4, depth, nest+1, inLoop)
			if g.r.IntN(2) == 0 {
				s.body = append(s.body, g.leave(depth, inLoop))
			}
			if g.r.IntN(2) == 0 {
				s.els = g.block(4, depth, nest+1, inLoop)
			}
		case ofMatch:
			s.body = g.block(3, depth, nest+1, inLoop)
			s.els = g.block(3, depth, nest+1, inLoop)
		case ofLoop:
			s.body = g.block(4, depth, nest+1, true)
		case ofRun:
			s.body = g.block(3, depth+1, nest+1, false)
		}
		return s
	}
}

// leave is an if body's last statement: a return, or in a loop a break or
// continue. A trailing block's frame has no return here.
func (g *ofGen) leave(depth int, inLoop bool) *ofStmt {
	ks := []int{}
	if depth == 1 {
		ks = append(ks, ofReturn)
	}
	if inLoop {
		ks = append(ks, ofBreak, ofContinue)
	}
	if len(ks) == 0 {
		return &ofStmt{kind: ofLend, y: "p"}
	}
	return &ofStmt{kind: ks[g.r.IntN(len(ks))]}
}

// ofPrint writes body as the function body of `body` after the prelude;
// it returns the source and the body's first line.
func ofPrint(body []*ofStmt) (string, int) {
	var b strings.Builder
	b.WriteString(ofPrelude)
	line := strings.Count(ofPrelude, "\n") + 1
	first := line
	emit := func(depth int, s string) {
		b.WriteString(strings.Repeat("\t", depth))
		b.WriteString(s)
		b.WriteString("\n")
		line++
	}
	col := func(depth int, prefix string) int { return depth + len(prefix) + 1 }
	k := 0
	var value func(s *ofStmt, depth int, prefix, suffix string)
	var walk func(list []*ofStmt, depth int)
	walk = func(list []*ofStmt, depth int) {
		for _, s := range list {
			s.line = line
			switch s.kind {
			case ofDecl:
				k++
				m := ""
				if s.mut {
					m = "mut "
				}
				emit(depth, fmt.Sprintf("let %s%s = open(%d)", m, s.x, k))
			case ofDeclMove:
				p := fmt.Sprintf("let %s = ", s.x)
				s.col = col(depth, p)
				emit(depth, p+s.y)
			case ofLend:
				s.col = col(depth, "lend(")
				emit(depth, "lend("+s.y+")")
			case ofClose:
				s.col = col(depth, "")
				emit(depth, s.y+".Close()")
			case ofKeep:
				s.col = col(depth, "keep(")
				emit(depth, "keep("+s.y+")")
			case ofAssign:
				k++
				emit(depth, fmt.Sprintf("%s = open(%d)", s.x, k))
			case ofAssignMove:
				p := s.x + " = "
				s.col = col(depth, p)
				emit(depth, p+s.y)
			case ofTemp:
				k++
				emit(depth, fmt.Sprintf("lend(open(%d))", k))
			case ofDiscard:
				k++
				s.col = col(depth, "")
				emit(depth, fmt.Sprintf("open(%d)", k))
			case ofDiscardLet:
				k++
				s.col = col(depth, "let _ = ")
				emit(depth, fmt.Sprintf("let _ = open(%d)", k))
			case ofBoth:
				prefix, arg := "both(", s.y
				if s.left {
					prefix, arg = "bothInt(", arg+".id"
				}
				if s.temp {
					prefix += "if cond(n) { "
					arg += " } else { open(0) }"
				}
				s.col = col(depth, prefix)
				p := prefix + arg + ", take("
				s.col2 = col(depth, p)
				emit(depth, p+s.y+"))")
			case ofIf:
				emit(depth, "if cond(n) {")
				walk(s.body, depth+1)
				if s.els != nil {
					s.elseLine = line
					emit(depth, "} else {")
					walk(s.els, depth+1)
				}
				emit(depth, "}")
			case ofMatch:
				emit(depth, "match n {")
				s.line = line
				emit(depth, "case 0:")
				walk(s.body, depth+1)
				s.elseLine = line
				emit(depth, "case _:")
				walk(s.els, depth+1)
				emit(depth, "}")
			case ofLoop:
				emit(depth, "for _ in 0..n {")
				walk(s.body, depth+1)
				emit(depth, "}")
			case ofBreak:
				emit(depth, "break")
			case ofContinue:
				emit(depth, "continue")
			case ofReturn:
				emit(depth, "return")
			case ofTry:
				emit(depth, "let _ = try fail(n)")
			case ofShort:
				if s.temp {
					k++
					emit(depth, fmt.Sprintf("let _ = cond(n) %s held(open(%d))", s.op, k))
				} else if s.left {
					s.col = col(depth, "let _ = take(")
					emit(depth, "let _ = take("+s.y+") > 0 "+s.op+" cond(n)")
				} else {
					p := "let _ = cond(n) " + s.op + " take("
					s.col = col(depth, p)
					emit(depth, p+s.y+") > 0")
				}
			case ofRun:
				emit(depth, "run() {")
				walk(s.body, depth+1)
				emit(depth, "}")
			case ofValue:
				prefix, suffix := "", ""
				switch s.form {
				case vLetBool, vDiscard:
					prefix = "let _ = "
				case vShort:
					prefix, suffix = "let _ = cond(n) && (", ")"
				case vLend:
					prefix, suffix = "lend(", ")"
				case vKeep:
					prefix, suffix = "keep(", ")"
				case vLetRes:
					prefix = "let " + s.x + " = "
				}
				value(s, depth, prefix, suffix)
			case ofTail:
				switch s.tk {
				case tCond:
					emit(depth, "cond(n)")
				case tTake:
					s.col = col(depth, "take(")
					emit(depth, "take("+s.y+") > 0")
				case tName:
					s.col = col(depth, "")
					emit(depth, s.y)
				case tOpen:
					k++
					s.col = col(depth, "")
					emit(depth, fmt.Sprintf("open(%d)", k))
				case tNested:
					value(s.body[0], depth, "", "")
				}
			}
		}
	}
	// value writes an if or match whose value goes where prefix and
	// suffix put it; line is the statement's (and the then block's or
	// the first clause's), elseLine the other arm's.
	value = func(s *ofStmt, depth int, prefix, suffix string) {
		s.line = line
		if !s.match {
			emit(depth, prefix+"if cond(n) {")
			walk(s.body, depth+1)
			s.elseLine = line
			emit(depth, "} else {")
			walk(s.els, depth+1)
			emit(depth, "}"+suffix)
			return
		}
		emit(depth, prefix+"match n {")
		s.armLine = line
		emit(depth, "case 0:")
		walk(s.body, depth+1)
		s.elseLine = line
		emit(depth, "case _:")
		walk(s.els, depth+1)
		emit(depth, "}"+suffix)
	}
	emit(0, "func body(n int, p Res, q Res) fails Bad takes q {")
	walk(body, 1)
	emit(0, "}")
	b.WriteString("\nfunc main() {}\n")
	return b.String(), first
}

// The reference interpreter. A slot is a local, a parameter or a
// temporary, in acquisition order.
const (
	rfOwned = iota + 1
	rfMoved
	rfMaybe
)

type rfSlot struct {
	name  string
	st    int
	lent  bool
	depth int
}

type rfLoop struct {
	first        int
	breaks, cont [][]rfSlot
}

type rfRun struct {
	st     []rfSlot // nil: unreachable
	quiet  int
	depth  int
	first  int // the frame's first slot
	loops  []*rfLoop
	diags  map[string]bool
	rel    map[string]string
	movers map[string]bool // slots moved in the current both call
	// The value arms being run: where their value goes, and for a lent
	// Res the slot of the statement's temporary that owns a new value.
	vmode, vslot, vfirst int
}

// Where a value arm's value goes (rfRun.vmode).
const (
	rvMove = iota
	rvLend
	rvDiscard
)

func ofReference(body []*ofStmt) ([]string, []string) {
	r := &rfRun{diags: map[string]bool{}, rel: map[string]string{}, depth: 1}
	r.st = []rfSlot{{name: "p", st: rfOwned, lent: true, depth: 1}, {name: "q", st: rfOwned, depth: 1}}
	r.block(body, "block", 0, 0)
	var d, rs []string
	for k := range r.diags {
		d = append(d, k)
	}
	for k, v := range r.rel {
		if v != "" {
			rs = append(rs, k+"="+v)
		}
	}
	slices.Sort(rs)
	return d, rs
}

func (r *rfRun) diag(line, col int) {
	if r.quiet == 0 && r.st != nil {
		r.diags[fmt.Sprintf("%d:%d S108", line, col)] = true
	}
}

func (r *rfRun) find(name string) int {
	for i := len(r.st) - 1; i >= 0; i-- {
		if r.st[i].name == name {
			return i
		}
	}
	panic("no slot " + name)
}

// releases is what slots first… still own, last acquired first.
func (r *rfRun) releases(first int) string {
	if r.st == nil {
		return ""
	}
	var out []string
	for i := len(r.st) - 1; i >= first; i-- {
		s := r.st[i]
		if s.lent || s.st == rfMoved {
			continue
		}
		n := s.name
		if s.st == rfMaybe {
			n += "?"
		}
		out = append(out, n)
	}
	return strings.Join(out, ",")
}

func (r *rfRun) record(key string, v string) {
	if r.quiet == 0 {
		r.rel[key] = v
	}
}

func (r *rfRun) use(name string, line, col int) {
	if r.st == nil {
		return
	}
	if s := r.st[r.find(name)].st; s == rfMoved || s == rfMaybe {
		r.diag(line, col)
	}
}

func (r *rfRun) move(name string, line, col int) {
	if r.st == nil {
		return
	}
	r.use(name, line, col)
	i := r.find(name)
	s := &r.st[i]
	if s.st != rfOwned {
		s.st = rfMoved
		return
	}
	if s.depth != r.depth || s.lent {
		r.diag(line, col)
		return
	}
	s.st = rfMoved
	if r.movers != nil {
		r.movers[name] = true
	}
}

func rfCopy(s []rfSlot) []rfSlot {
	if s == nil {
		return nil
	}
	return append([]rfSlot{}, s...)
}

func rfJoin(a, b []rfSlot, n int) []rfSlot {
	if a == nil {
		if b == nil {
			return nil
		}
		return rfCopy(b[:n])
	}
	a = rfCopy(a[:n])
	if b == nil {
		return a
	}
	for i := range a {
		if a[i].st != b[i].st {
			a[i].st = rfMaybe
		}
	}
	return a
}

func rfSame(a, b []rfSlot) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].st != b[i].st {
			return false
		}
	}
	return true
}

// block runs a scope's statements and records its end under key@line.
func (r *rfRun) block(list []*ofStmt, kind string, line, _ int) {
	first := len(r.st)
	for _, s := range list {
		r.stmt(s)
	}
	if r.st != nil {
		if line > 0 {
			r.record(fmt.Sprintf("%s@%d", kind, line), r.releases(first))
		} else {
			r.record("body", r.releases(r.first))
		}
		r.st = r.st[:first]
	}
}

func (r *rfRun) acquire(name string) {
	if r.st != nil {
		r.st = append(r.st, rfSlot{name: name, st: rfOwned, depth: r.depth})
	}
}

func (r *rfRun) stmt(s *ofStmt) {
	switch s.kind {
	case ofDecl:
		r.acquire(s.x)
	case ofDeclMove:
		r.move(s.y, s.line, s.col)
		r.acquire(s.x)
	case ofLend:
		r.use(s.y, s.line, s.col)
	case ofClose, ofKeep:
		r.move(s.y, s.line, s.col)
	case ofAssign, ofAssignMove:
		if s.kind == ofAssignMove {
			r.move(s.y, s.line, s.col)
		}
		if r.st != nil {
			i := r.find(s.x)
			v := ""
			switch r.st[i].st {
			case rfOwned:
				v = s.x
			case rfMaybe:
				v = s.x + "?"
			}
			r.record(fmt.Sprintf("assign@%d", s.line), v)
			r.st[i].st = rfOwned
		}
	case ofTemp:
		r.record(fmt.Sprintf("stmt@%d", s.line), "tmp")
	case ofDiscard, ofDiscardLet:
		r.diag(s.line, s.col)
	case ofBoth:
		r.use(s.y, s.line, s.col)
		r.movers = map[string]bool{}
		r.move(s.y, s.line, s.col2)
		if !s.left && r.movers[s.y] {
			r.diag(s.line, s.col)
		}
		r.movers = nil
		if s.temp {
			r.record(fmt.Sprintf("stmt@%d", s.line), "tmp?")
		}
	case ofIf:
		n := len(r.st)
		saved := rfCopy(r.st)
		r.block(s.body, "block", s.line, 0)
		then := r.st
		r.st = saved
		if s.els != nil {
			r.block(s.els, "block", s.elseLine, 0)
		}
		r.st = rfJoin(then, r.st, n)
	case ofMatch:
		n := len(r.st)
		saved := rfCopy(r.st)
		r.block(s.body, "case", s.line, 0)
		a := r.st
		r.st = rfCopy(saved)
		r.block(s.els, "case", s.elseLine, 0)
		r.st = rfJoin(a, r.st, n)
	case ofLoop:
		r.loop(s)
	case ofBreak, ofContinue:
		lp := r.loops[len(r.loops)-1]
		r.record(fmt.Sprintf("branch@%d", s.line), r.releases(lp.first))
		if r.st != nil {
			if s.kind == ofBreak {
				lp.breaks = append(lp.breaks, rfCopy(r.st[:lp.first]))
			} else {
				lp.cont = append(lp.cont, rfCopy(r.st[:lp.first]))
			}
		}
		r.st = nil
	case ofReturn:
		r.record(fmt.Sprintf("return@%d", s.line), r.releases(r.first))
		r.st = nil
	case ofTry:
		r.record(fmt.Sprintf("try@%d", s.line), r.releases(r.first))
	case ofShort:
		// The right operand runs on some paths only: an if with no else.
		if s.temp {
			// Its temporary is released at the statement's end under a flag.
			r.record(fmt.Sprintf("*syntax.VarSpec@%d", s.line), "tmp?")
			return
		}
		saved := rfCopy(r.st)
		r.move(s.y, s.line, s.col)
		if !s.left {
			r.st = rfJoin(r.st, saved, len(r.st))
		}
	case ofValue:
		switch s.form {
		case vLetBool:
			r.arms(s, rvDiscard, -1)
		case vShort:
			// The operand runs on some paths only: an if with no else.
			saved := rfCopy(r.st)
			r.arms(s, rvLend, -1)
			if saved != nil {
				r.st = rfJoin(r.st, saved, len(saved))
			}
		case vLend:
			// The value is a temporary of the statement, owned on the
			// paths whose arm made it.
			if r.st == nil {
				r.arms(s, rvLend, -1)
				return
			}
			i := len(r.st)
			r.st = append(r.st, rfSlot{name: "tmp", st: rfMoved, depth: r.depth})
			r.arms(s, rvLend, i)
			r.record(fmt.Sprintf("stmt@%d", s.line), r.releases(i))
			if r.st != nil {
				r.st = r.st[:i]
			}
		case vKeep:
			r.arms(s, rvMove, -1)
		case vLetRes:
			r.arms(s, rvMove, -1)
			r.acquire(s.x)
		case vDiscard:
			r.arms(s, rvDiscard, -1)
		}
	case ofTail:
		switch s.tk {
		case tTake:
			r.move(s.y, s.line, s.col)
		case tName:
			if r.vmode == rvLend && r.vslot >= 0 && r.find(s.y) >= r.vfirst {
				r.move(s.y, s.line, s.col)
				if r.st != nil {
					r.st[r.vslot].st = rfOwned
				}
			} else if r.vmode == rvMove {
				r.move(s.y, s.line, s.col)
			} else {
				r.use(s.y, s.line, s.col)
			}
		case tOpen:
			switch {
			case r.vmode == rvDiscard:
				r.diag(s.line, s.col)
			case r.vslot >= 0 && r.st != nil:
				r.st[r.vslot].st = rfOwned
			}
		case tNested:
			r.stmt(s.body[0])
		}
	case ofRun:
		saved := rfCopy(r.st)
		loops, first := r.loops, r.first
		r.loops, r.first = nil, len(r.st)
		r.depth++
		r.block(s.body, "block", s.line, 0)
		r.depth--
		r.loops, r.first = loops, first
		r.st = saved
	}
}

// arms runs the two arms of a value if or match whose value goes to mode
// (slot: the temporary that owns a new lent value, or -1), and joins them.
func (r *rfRun) arms(s *ofStmt, mode, slot int) {
	vmode, vslot, vfirst := r.vmode, r.vslot, r.vfirst
	r.vmode, r.vslot = mode, slot
	r.vfirst = len(r.st)
	kind, first := "block", s.line
	if s.match {
		kind, first = "case", s.armLine
	}
	n := len(r.st)
	saved := rfCopy(r.st)
	r.block(s.body, kind, first, 0)
	a := r.st
	r.st = rfCopy(saved)
	r.block(s.els, kind, s.elseLine, 0)
	r.st = rfJoin(a, r.st, n)
	r.vmode, r.vslot, r.vfirst = vmode, vslot, vfirst
}

// loop runs the body to its plain fixpoint quietly, then once more.
func (r *rfRun) loop(s *ofStmt) {
	first := len(r.st)
	entry := rfCopy(r.st)
	pass := func() (back []rfSlot, lp *rfLoop) {
		lp = &rfLoop{first: first}
		r.loops = append(r.loops, lp)
		r.st = rfCopy(entry)
		r.block(s.body, "block", s.line, 0)
		back = r.st
		for _, c := range lp.cont {
			back = rfJoin(back, c, first)
		}
		r.loops = r.loops[:len(r.loops)-1]
		return back, lp
	}
	r.quiet++
	for entry != nil {
		back, _ := pass()
		next := rfJoin(entry, back, first)
		if rfSame(next, entry) {
			break
		}
		entry = next
	}
	r.quiet--
	_, lp := pass()
	out := rfCopy(entry)
	for _, b := range lp.breaks {
		out = rfJoin(out, b, first)
	}
	r.st = out
}

func ofNumbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d %s\n", i+1, l)
	}
	return b.String()
}

// ofReleases renders the checker's release plan of body's lines.
func ofReleases(c *checker, first int) []string {
	f := c.pkg.Files()[0]
	var out []string
	var bodyBlock *syntax.BlockStmt
	for _, d := range f.Decls {
		if fd, ok := d.(*syntax.FuncDecl); ok && fd.Name.Name == "body" {
			bodyBlock = fd.Body
		}
	}
	for n, rs := range c.pkg.Info.Releases {
		line := f.Line(n.Pos())
		if line < first {
			continue
		}
		kind := ""
		switch n.(type) {
		case *syntax.BlockStmt:
			kind = "block"
		case *syntax.CaseClause:
			kind = "case"
		case *syntax.ReturnStmt:
			kind = "return"
		case *syntax.BranchStmt:
			kind = "branch"
		case *syntax.TryExpr:
			kind = "try"
		case *syntax.Ident:
			kind = "assign"
		case *syntax.ExprStmt:
			kind = "stmt"
		default:
			kind = fmt.Sprintf("%T", n)
		}
		key := fmt.Sprintf("%s@%d", kind, line)
		if n == syntax.Node(bodyBlock) {
			key = "body"
		}
		var names []string
		for _, r := range rs {
			s := "tmp"
			if r.Var != nil {
				s = r.Var.Name
			}
			if r.Flag {
				s += "?"
			}
			names = append(names, s)
		}
		out = append(out, key+"="+strings.Join(names, ","))
	}
	slices.Sort(out)
	return out
}
