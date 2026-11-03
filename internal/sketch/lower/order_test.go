package lower

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// TestEvaluationOrder: generated compositions of traced calls, arguments,
// dot-call receivers, record and slice literal fields, selectors, len,
// arithmetic, comparisons, short-circuit operators, try with a failure at a
// chosen earlier step, with blocks whose tail is the value, provider
// operations, if statements and conditional loops. Every effectful operand
// prints its tag through Console when it is evaluated, so the program's
// output is its evaluation trace. An independent Go interpreter of the same
// tree computes the expected trace and value: operands left to right, each
// once, untaken branches and the right side of a decided && or || never.
// Thousands of composed operands across the cases; finite evidence, not a
// proof over all programs.
func TestEvaluationOrder(t *testing.T) {
	const programs, cases = 4, 60
	var built []*lowered
	var want []string
	for p := 0; p < programs; p++ {
		g := &orderGen{rng: rand.New(rand.NewSource(int64(1000 + p)))}
		src, expected := g.program(cases)
		built = append(built, mustLower(t, fmt.Sprintf("order%d", p), src))
		want = append(want, expected)
	}
	exes := buildAll(t, built, true)
	if exes == nil {
		return
	}
	for i, o := range built {
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != want[i] {
			t.Errorf("%s: exit %d, stderr %q\n%s", o.name, code, stderr, diffLines(stdout, want[i]))
		}
	}
}

// diffLines shows the first line where got and want differ.
func diffLines(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			lo := max(0, i-3)
			return fmt.Sprintf("line %d: got %q, want %q\ncontext (want): %q", i+1, gl, wl, w[lo:min(len(w), i+3)])
		}
	}
	return "equal"
}

// orderGen generates one program and its expected output.
type orderGen struct {
	rng  *rand.Rand
	tags int
	fail int // the failing call's number in the current case, or 0
	seen int // fallible calls generated in the current case
}

// node is an expression of the generated language: its Ergo text and its
// meaning, run by the interpreter.
type node struct {
	src  string
	eval func(*interp) int // an int expression; a bool is 0 or 1
}

// interp runs a case: the trace it prints and its failure.
type interp struct {
	trace  []string
	failed string
}

// errFailed unwinds the interpreter at a failing try.
type errFailed struct{}

func (g *orderGen) tag() string {
	g.tags++
	return "e" + strconv.Itoa(g.tags)
}

func lit(n int) node {
	return node{src: strconv.Itoa(n), eval: func(*interp) int { return n }}
}

// intExpr is an int expression of the given depth.
func (g *orderGen) intExpr(depth int) node {
	if depth <= 0 {
		if g.rng.Intn(3) == 0 {
			return lit(g.rng.Intn(10))
		}
		tag, v := g.tag(), g.rng.Intn(10)
		return node{src: fmt.Sprintf("t(%q, %d)", tag, v), eval: func(in *interp) int { in.trace = append(in.trace, tag); return v }}
	}
	switch g.rng.Intn(10) {
	case 0: // a traced identity
		tag, x := g.tag(), g.intExpr(depth-1)
		return node{src: fmt.Sprintf("t(%q, %s)", tag, x.src), eval: func(in *interp) int {
			v := x.eval(in)
			in.trace = append(in.trace, tag)
			return v
		}}
	case 1: // arithmetic
		a, b := g.intExpr(depth-1), g.intExpr(depth-1)
		op := []string{"+", "-", "*"}[g.rng.Intn(3)]
		return node{src: "(" + a.src + " " + op + " " + b.src + ")", eval: func(in *interp) int {
			x := a.eval(in)
			y := b.eval(in)
			switch op {
			case "+":
				return x + y
			case "-":
				return x - y
			}
			return x * y
		}}
	case 2: // a two-argument call
		a, b := g.intExpr(depth-1), g.intExpr(depth-1)
		return node{src: "sub(" + a.src + ", " + b.src + ")", eval: func(in *interp) int { x := a.eval(in); return x - b.eval(in) }}
	case 3: // record literal fields, then a selector
		a, b := g.intExpr(depth-1), g.intExpr(depth-1)
		pickA := g.rng.Intn(2) == 0
		field := "B"
		if pickA {
			field = "A"
		}
		return node{src: "Box{A: " + a.src + ", B: " + b.src + "}." + field, eval: func(in *interp) int {
			x := a.eval(in)
			y := b.eval(in)
			if pickA {
				return x
			}
			return y
		}}
	case 4: // a dot call: the receiver, then the argument
		a, b, c := g.intExpr(depth-1), g.intExpr(depth-1), g.intExpr(depth-1)
		return node{src: "Box{A: " + a.src + ", B: " + b.src + "}.Second(" + c.src + ")", eval: func(in *interp) int {
			a.eval(in)
			y := b.eval(in)
			return y + c.eval(in)
		}}
	case 5: // slice literal elements and an index
		a, b, c := g.intExpr(depth-1), g.intExpr(depth-1), g.intExpr(depth-1)
		k := g.rng.Intn(3)
		return node{src: fmt.Sprintf("[]int{%s, %s, %s}[%d]", a.src, b.src, c.src, k), eval: func(in *interp) int {
			vs := []int{a.eval(in), b.eval(in), c.eval(in)}
			return vs[k]
		}}
	case 6: // len of a slice literal
		a, b := g.intExpr(depth-1), g.intExpr(depth-1)
		return node{src: "len([]int{" + a.src + ", " + b.src + "})", eval: func(in *interp) int { a.eval(in); b.eval(in); return 2 }}
	case 7, 8: // a with block: its tail is the value, a provider operation inside
		x := g.intExpr(depth - 1)
		return node{src: "with Fixed { (" + x.src + " + Num.Get()) }", eval: func(in *interp) int { return x.eval(in) + 7 }}
	}
	// a comparison as an int through a traced bool call
	b := g.boolExpr(depth - 1)
	tag := g.tag()
	return node{src: fmt.Sprintf("pick(tb(%q, %s))", tag, b.src), eval: func(in *interp) int {
		v := b.eval(in)
		in.trace = append(in.trace, tag)
		return v
	}}
}

// boolExpr is a bool expression; its eval returns 0 or 1.
func (g *orderGen) boolExpr(depth int) node {
	if depth <= 0 {
		tag, v := g.tag(), g.rng.Intn(2)
		return node{src: fmt.Sprintf("tb(%q, %v)", tag, v == 1), eval: func(in *interp) int { in.trace = append(in.trace, tag); return v }}
	}
	switch g.rng.Intn(4) {
	case 0, 1: // short circuit
		a, b := g.boolExpr(depth-1), g.boolExpr(depth-1)
		and := g.rng.Intn(2) == 0
		op := "||"
		if and {
			op = "&&"
		}
		return node{src: "(" + a.src + " " + op + " " + b.src + ")", eval: func(in *interp) int {
			x := a.eval(in)
			if and && x == 0 || !and && x == 1 {
				return x
			}
			return b.eval(in)
		}}
	case 2: // a comparison of ints
		a, b := g.intExpr(depth-1), g.intExpr(depth-1)
		op := []string{"<", "==", ">="}[g.rng.Intn(3)]
		return node{src: "(" + a.src + " " + op + " " + b.src + ")", eval: func(in *interp) int {
			x := a.eval(in)
			y := b.eval(in)
			r := op == "<" && x < y || op == "==" && x == y || op == ">=" && x >= y
			if r {
				return 1
			}
			return 0
		}}
	}
	a := g.boolExpr(depth - 1)
	return node{src: "!" + a.src, eval: func(in *interp) int { return 1 - a.eval(in) }}
}

// try is `let gN = try f(tag, x, fails)`: a fallible call, tried at the
// start of a let (try stands at the start of an expression, grammar D8).
func (g *orderGen) try(src *strings.Builder, n int) func(*interp) int {
	g.seen++
	tag, x := g.tag(), g.intExpr(1+g.rng.Intn(2))
	fails := g.seen == g.fail
	fmt.Fprintf(src, "\tlet g%d = try f(%q, %s, %v)\n", n, tag, x.src, fails)
	return func(in *interp) int {
		v := x.eval(in)
		in.trace = append(in.trace, tag)
		if fails {
			in.failed = tag
			panic(errFailed{})
		}
		return v
	}
}

// caseBody is one case's statements: lets, tried calls that may fail at
// any of three points, an if statement whose untaken branch must not run,
// a conditional loop and a returned expression.
func (g *orderGen) caseBody() (string, func(*interp) int) {
	var src strings.Builder
	a := g.intExpr(2 + g.rng.Intn(2))
	fmt.Fprintf(&src, "\tlet first = %s\n", a.src)
	g1 := g.try(&src, 1)
	src.WriteString("\tlet mut acc = first + g1\n")
	cond := g.boolExpr(1 + g.rng.Intn(2))
	then, els := g.intExpr(1), g.intExpr(1)
	fmt.Fprintf(&src, "\tif %s {\n\t\tacc += %s\n\t} else {\n\t\tacc -= %s\n\t}\n", cond.src, then.src, els.src)
	g2 := g.try(&src, 2)
	bound := g.rng.Intn(3)
	body := g.intExpr(1)
	src.WriteString("\tlet mut i = 0\n")
	fmt.Fprintf(&src, "\tfor tb(\"loop\", i < %d) {\n\t\tacc = acc + %s\n\t\ti += 1\n\t}\n", bound, body.src)
	g3 := g.try(&src, 3)
	// A later operand that assigns an earlier one's variable: the read of
	// acc on the left happens before the with block assigns it.
	src.WriteString("\tlet snap = acc + with Fixed {\n\t\tacc = acc + 1000\n\t\tacc\n\t}\n")
	src.WriteString("\tacc = acc - snap\n")
	places := g.placeStmts(&src)
	ret := g.intExpr(1 + g.rng.Intn(3))
	fmt.Fprintf(&src, "\treturn acc * 2 + %s + g2 - g3 + %s\n", ret.src, placeSum)
	return src.String(), func(in *interp) int {
		first := a.eval(in)
		acc := first + g1(in)
		if cond.eval(in) == 1 {
			acc += then.eval(in)
		} else {
			acc -= els.eval(in)
		}
		v2 := g2(in)
		for i := 0; ; i++ {
			in.trace = append(in.trace, "loop")
			if !(i < bound) {
				break
			}
			acc = acc + body.eval(in)
		}
		v3 := g3(in)
		snap := acc + (acc + 1000)
		acc = acc + 1000 - snap
		st := places(in)
		old := acc
		r := old*2 + ret.eval(in) + v2 - v3
		return r + st.sum()
	}
}

// placeState is the interpreter's model of a case's places: Go variables,
// a pointer and slices, so aliasing is real.
type placeState struct {
	b1, b2 box
	p      *box
	xs, ys []int
	bs, cs []box
	ws, vs []wrap
}

type box struct{ A, B int }

type wrap struct{ inner box }

func (s *placeState) sum() int {
	return s.b1.A + 3*s.b1.B + 5*s.b2.A + 7*s.b2.B + 11*s.xs[0] + 13*s.xs[1] + 17*s.ys[0] + 19*s.ys[1] +
		23*s.bs[0].A + 29*s.bs[1].A + 31*s.cs[0].A + 37*s.cs[1].A + 41*s.p.A +
		43*s.ws[0].inner.A + 47*s.ws[1].inner.B + 53*s.vs[0].inner.A + 59*s.vs[1].inner.B
}

// placeSum is placeState.sum in the generated language.
const placeSum = "b1.A + 3 * b1.B + 5 * b2.A + 7 * b2.B + 11 * xs[0] + 13 * xs[1] + 17 * ys[0] + 19 * ys[1] + 23 * bs[0].A + 29 * bs[1].A + 31 * cs[0].A + 37 * cs[1].A + 41 * p.A + 43 * ws[0].inner.A + 47 * ws[1].inner.B + 53 * vs[0].inner.A + 59 * vs[1].inner.B"

// placeStmts writes the places of a case and assignments and *T dot calls
// on them (spec D4: a place is fixed before the value, and a receiver is the
// place, not a copy). The value or argument is a traced call or a with
// block that first moves the place's base: it points p at b2, makes xs
// ys, makes bs cs, or replaces b1. The oracle fixes each place in a
// temporary before it evaluates the value, so a lowering that reads the
// base after the value, or calls a *T function on a copy, differs.
func (g *orderGen) placeStmts(out *strings.Builder) func(*interp) *placeState {
	// The statements go to src first: a base is declared `let mut` only
	// when a statement reassigns it, writes through it or borrows it (b1,
	// which p borrows), as check-v1 D-D1 and Q3 require.
	src := &strings.Builder{}
	mut := map[string]bool{"b1": true}
	// value is an int operand that may move one base first.
	type move int
	const (
		none move = iota
		moveP
		moveXs
		moveBs
		moveB1
		moveWs
	)
	value := func(m move) (string, func(*interp, *placeState) int) {
		tag, v := g.tag(), 1+g.rng.Intn(9)
		call := fmt.Sprintf("t(%q, %d)", tag, v)
		var stmt string
		var apply func(*placeState)
		switch m {
		case moveP:
			stmt, apply = "p = &b2", func(s *placeState) { s.p = &s.b2 }
			mut["p"], mut["b2"] = true, true
		case moveXs:
			stmt, apply = "xs = ys", func(s *placeState) { s.xs = s.ys }
			mut["xs"] = true
		case moveBs:
			stmt, apply = "bs = cs", func(s *placeState) { s.bs = s.cs }
			mut["bs"] = true
		case moveB1:
			stmt, apply = "b1 = Box{A: 40, B: 50}", func(s *placeState) { s.b1 = box{40, 50} }
		case moveWs:
			stmt, apply = "ws = vs", func(s *placeState) { s.ws = s.vs }
			mut["ws"] = true
		}
		eval := func(in *interp, s *placeState) int {
			if apply != nil {
				apply(s)
			}
			in.trace = append(in.trace, tag)
			return v
		}
		if stmt == "" {
			return call, eval
		}
		return "with Fixed {\n\t\t" + stmt + "\n\t\t" + call + "\n\t}", eval
	}
	pickMove := func(m move) move {
		if g.rng.Intn(3) == 0 {
			return none
		}
		return m
	}
	index := func() (string, func(*interp) int) {
		k := g.rng.Intn(2)
		if g.rng.Intn(2) == 0 {
			return strconv.Itoa(k), func(*interp) int { return k }
		}
		tag := g.tag()
		return fmt.Sprintf("t(%q, %d)", tag, k), func(in *interp) int { in.trace = append(in.trace, tag); return k }
	}
	var steps []func(*interp, *placeState)
	for n := 2 + g.rng.Intn(3); n > 0; n-- {
		switch g.rng.Intn(11) {
		case 0: // a field through a pointer
			src1, v := value(pickMove(moveP))
			fmt.Fprintf(src, "\tp.A = %s\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { base := s.p; base.A = v(in, s) })
		case 1: // an op-assignment through a pointer: the old value is read first
			src1, v := value(pickMove(moveP))
			fmt.Fprintf(src, "\tp.B += %s\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { base := s.p; old := base.B; base.B = old + v(in, s) })
		case 2: // a slice element
			isrc, i := index()
			src1, v := value(pickMove(moveXs))
			fmt.Fprintf(src, "\txs[%s] = %s\n", isrc, src1)
			mut["xs"] = true
			steps = append(steps, func(in *interp, s *placeState) { base := s.xs; k := i(in); base[k] = v(in, s) })
		case 3: // a *T dot call on a variable: the variable, not a copy
			src1, v := value(pickMove(moveB1))
			fmt.Fprintf(src, "\tb1.Bump(%s)\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { recv := &s.b1; n := v(in, s); recv.A += n })
		case 4: // a *T dot call through a pointer
			src1, v := value(pickMove(moveP))
			fmt.Fprintf(src, "\tp.Bump(%s)\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { recv := s.p; n := v(in, s); recv.A += n })
		case 5: // a *T dot call on a slice element
			isrc, i := index()
			src1, v := value(pickMove(moveBs))
			fmt.Fprintf(src, "\tbs[%s].Bump(%s)\n", isrc, src1)
			mut["bs"] = true
			steps = append(steps, func(in *interp, s *placeState) { base := s.bs; recv := &base[i(in)]; n := v(in, s); recv.A += n })
		case 7: // a *T dot call on a dereferenced pointer: the pointee, not a copy
			src1, v := value(pickMove(moveP))
			fmt.Fprintf(src, "\t(*p).Bump(%s)\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { recv := s.p; n := v(in, s); recv.A += n })
		case 8: // a field of a dereferenced pointer
			src1, v := value(pickMove(moveP))
			fmt.Fprintf(src, "\t(*p).B = %s\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { base := s.p; base.B = v(in, s) })
		case 9: // a *T dot call on a field of a slice element
			isrc, i := index()
			src1, v := value(pickMove(moveWs))
			fmt.Fprintf(src, "\tws[%s].inner.Bump(%s)\n", isrc, src1)
			mut["ws"] = true
			steps = append(steps, func(in *interp, s *placeState) { base := s.ws; recv := &base[i(in)].inner; n := v(in, s); recv.A += n })
		case 10: // a field of a field of a slice element
			isrc, i := index()
			src1, v := value(pickMove(moveWs))
			fmt.Fprintf(src, "\tws[%s].inner.B = %s\n", isrc, src1)
			mut["ws"] = true
			steps = append(steps, func(in *interp, s *placeState) { base := s.ws; k := i(in); base[k].inner.B = v(in, s) })
		default: // a field of a field of a variable
			src1, v := value(pickMove(moveB1))
			fmt.Fprintf(src, "\tb1.B = %s\n", src1)
			steps = append(steps, func(in *interp, s *placeState) { n := v(in, s); s.b1.B = n })
		}
	}
	let := func(name, value string) {
		kw := "let"
		if mut[name] {
			kw = "let mut"
		}
		fmt.Fprintf(out, "\t%s %s = %s\n", kw, name, value)
	}
	let("b1", "Box{A: 1, B: 2}")
	let("b2", "Box{A: 3, B: 4}")
	let("p", "&b1")
	let("xs", "[]int{5, 6}")
	let("ys", "[]int{7, 8}")
	let("bs", "[]Box{Box{A: 9, B: 0}, Box{A: 10, B: 0}}")
	let("cs", "[]Box{Box{A: 11, B: 0}, Box{A: 12, B: 0}}")
	let("ws", "[]Wrap{Wrap{inner: Box{A: 13, B: 14}}, Wrap{inner: Box{A: 15, B: 16}}}")
	let("vs", "[]Wrap{Wrap{inner: Box{A: 17, B: 18}}, Wrap{inner: Box{A: 19, B: 20}}}")
	out.WriteString(src.String())
	return func(in *interp) *placeState {
		s := &placeState{b1: box{1, 2}, b2: box{3, 4}, xs: []int{5, 6}, ys: []int{7, 8}, bs: []box{{9, 0}, {10, 0}}, cs: []box{{11, 0}, {12, 0}}, ws: []wrap{{box{13, 14}}, {box{15, 16}}}, vs: []wrap{{box{17, 18}}, {box{19, 20}}}}
		s.p = &s.b1
		for _, step := range steps {
			step(in, s)
		}
		return s
	}
}

const orderPrelude = `type Bad { At string }

type Box { A int, B int }

type Wrap { inner Box }

type Num { Get() int }

type Seven {}

func Get(_ Seven) int { return 7 }

let Fixed = context {
	Num: Seven,
}

func t(tag string, v int) int needs Console {
	Console.Println(tag)
	return v
}

func tb(tag string, v bool) bool needs Console {
	Console.Println(tag)
	return v
}

func f(tag string, v int, fail bool) int fails Bad needs Console {
	Console.Println(tag)
	if fail {
		return Bad{At: tag}
	}
	return v
}

func pick(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sub(a int, b int) int { return a - b }

func Second(b Box, n int) int { return b.B + n }

func Bump(b *Box, n int) { b.A = b.A + n }
`

// program is a program of n cases and its expected output.
func (g *orderGen) program(n int) (string, string) {
	var src, main, want strings.Builder
	src.WriteString(orderPrelude)
	main.WriteString("\nfunc main() needs Console {\n")
	for k := 1; k <= n; k++ {
		g.seen = 0
		// About half the cases fail at one of their fallible calls; the
		// failing call's number is chosen before the case is generated.
		g.fail = 0
		if g.rng.Intn(2) == 0 {
			g.fail = 1 + g.rng.Intn(3)
		}
		body, eval := g.caseBody()
		fmt.Fprintf(&src, "\nfunc case%d() int fails Bad needs Console {\n%s}\n", k, body)
		in := &interp{}
		result := func() (r int) {
			defer func() {
				if p := recover(); p != nil {
					if _, ok := p.(errFailed); !ok {
						panic(p)
					}
					r = -1000
				}
			}()
			return eval(in)
		}()
		fmt.Fprintf(&want, "case %d\n", k)
		for _, line := range in.trace {
			want.WriteString(line + "\n")
		}
		if in.failed != "" {
			fmt.Fprintf(&want, "failed at %s\n", in.failed)
		}
		want.WriteString("ok\n")
		fmt.Fprintf(&main, "\tConsole.Println(%q)\n", fmt.Sprintf("case %d", k))
		fmt.Fprintf(&main, "\tlet r%d = case%d() catch {\n\tcase Bad as e:\n\t\tConsole.Println(\"failed at \" + e.At)\n\t\t-1000\n\t}\n", k, k)
		fmt.Fprintf(&main, "\tif r%d == %d {\n\t\tConsole.Println(\"ok\")\n\t} else {\n\t\tConsole.Println(\"wrong value\")\n\t}\n", k, result)
	}
	main.WriteString("}\n")
	src.WriteString(main.String())
	return src.String(), want.String()
}
