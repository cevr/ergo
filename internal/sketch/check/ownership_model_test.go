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
// and continue, return, the failure edge of a try, and a trailing block
// (a frame of its own) that uses or hands off the enclosing locals; with a
// lent and a taken parameter. A reference abstract interpreter in the
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
// its release reported S110.
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

func keep(r Res) takes r {}

func take(r Res) int takes r { 0 }

func both(r Res, n int) {}

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
	ofRun
)

type ofStmt struct {
	kind      int
	x, y      string // x: the declared or assigned name; y: the operand
	mut       bool
	body, els []*ofStmt
	// Filled by ofPrint: the statement's line, the else line, and columns.
	line, elseLine int
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

func (g *ofGen) stmt(depth, nest int, inLoop bool) *ofStmt {
	for {
		k := g.r.IntN(ofRun + 1)
		switch k {
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
				s.col = col(depth, "both(")
				p := "both(" + s.y + ", take("
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
			case ofRun:
				emit(depth, "run() {")
				walk(s.body, depth+1)
				emit(depth, "}")
			}
		}
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
}

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
		if r.movers[s.y] {
			r.diag(s.line, s.col)
		}
		r.movers = nil
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
