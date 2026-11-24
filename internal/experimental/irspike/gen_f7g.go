package irspike

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
)

// GenerateOrder writes the program of seed for F7's G2 families (lane GP):
// GenerateFail's failure programs, with statements whose order the IR
// must keep as the emitter does: a continue in a polling loop, which
// polls before the next iteration (the emitter jumps to the back-edge
// poll); impure calls (tr prints its argument) inside an expression,
// after an operand that may be a defect, in conditions and in for-in
// domains.
//
// Every form is reached by construction: seed s writes a function
// `forced` whose body is form OrderForm(s) of orderForms, so the seeds
// 0..len(orderForms())-1 write each form at least once, whatever the
// random statements draw.
func GenerateOrder(seed uint64) string {
	g := &fgen{r: rand.New(rand.NewPCG(seed, seed^0x6f7264657267656e)), order: true, force: OrderForm(seed)}
	return g.program()
}

// orderForm is one form of GenerateOrder: the statement kind of orderStmt
// that writes it and the sub-choices (sub) that pick it within the kind.
type orderForm struct {
	name string
	kind int
	subs map[string]int
}

// orderForms is the schedule of forms the forced functions write.
var orderForms = sync.OnceValue(func() []orderForm {
	return []orderForm{
		{"continue", 0, nil},
		{"call inside an expression", 2, map[string]int{"calls2": 3}},
		{"short-circuit operand with a call", 2, map[string]int{"calls2": 3, "expr": 6}},
		{"step that gives no output", 2, map[string]int{"calls2": 3, "expr": 9}},
		{"error row as a type argument", 2, map[string]int{"calls2": 3, "expr": 10}},
		{"call with a call site", 2, map[string]int{"calls2": 3, "expr": 11}},
		{"len and index of a named string", 2, map[string]int{"calls2": 3, "expr": 12}},
		{"slice of a slice", 2, map[string]int{"calls2": 3, "expr": 13}},
		{"for-in over a call", 5, nil},
		{"loop condition with a call", 4, nil},
		{"try of a non-call", 7, map[string]int{"try7": 1}},
		{"try inside an expression", 8, nil},
		{"catch arm that continues", 9, map[string]int{"try9": 1, "try9loop": 0}},
		{"catch arm whose value is an if", 9, map[string]int{"try9": 1, "try9loop": 1}},
		{"positional record literal", 2, map[string]int{"calls2": 0}},
		{"fallible call with an empty row", 2, map[string]int{"calls2": 1}},
		{"call of a unit fallible function", 7, map[string]int{"try7": 0}},
		{"catch of a match", 9, map[string]int{"try9": 0}},
		{"op-assignment of a value with statements", 3, map[string]int{"calls3": 0}},
	}
})

// OrderForm is the index in orderForms of the form seed's forced
// function writes.
func OrderForm(seed uint64) int { return int(seed % uint64(len(orderForms()))) }

// OrderFormName is the name of the form seed's forced function writes.
func OrderFormName(seed uint64) string { return orderForms()[OrderForm(seed)].name }

// sub draws a sub-choice of n named id: the forced function's choice
// while it is written, else a random one.
func (g *fgen) sub(id string, n int) int {
	if c, ok := g.forced[id]; ok {
		return c
	}
	return g.n(n)
}

// forcedFunction writes `forced`, whose body is the seed's form, and
// which main calls like the others: it fails E1 and prints, so each form
// has what it needs.
func (g *fgen) forcedFunction() {
	form := orderForms()[g.force]
	f := ffunc{name: "forced", params: 2, row: []string{"E1"}, needs: []string{"Console"}}
	g.cur = f
	g.locals = []string{"p0", "p1"}
	g.b.WriteString("func forced(p0 int, p1 int) int fails E1 needs Console {\n\tConsole.Println(\"forced\")\n")
	g.forced = form.subs
	if g.forced == nil {
		g.forced = map[string]int{}
	}
	if !g.orderStmt("\t", 1, form.kind) {
		panic("irspike: GenerateOrder's forced form " + form.name + " was not written")
	}
	g.forced = nil
	g.tail()
	g.b.WriteString("}\n\n")
	g.funcs = append(g.funcs, f)
}

// orderKinds is the number of statement kinds orderStmt adds.
const orderKinds = 10

// orderDecls writes the declarations orderStmt's statements call: tr
// prints its argument (the order of calls shows in the output) and span
// is an impure call whose value a for-in ranges over.
func (g *fgen) orderDecls() {
	g.b.WriteString("type Pt { A int, B int }\n\n")
	g.b.WriteString("func chk(n int) int fails E1 {\n\tif n % 2 == 0 {\n\t\treturn E1{Code: n}\n\t}\n\tn\n}\n\n")
	g.b.WriteString("func tr(n int) int needs Console {\n\tConsole.Println(\"tr \\(n)\")\n\tn\n}\n\n")
	g.b.WriteString("func span(n int) []int needs Console {\n\tConsole.Println(\"span \\(n)\")\n\t[]int{n, n + 1}\n}\n\n")
	// app called with a function that cannot fail: a fallible function
	// whose row is empty at the site (E is no failure), whose Go error the
	// caller drops.
	g.b.WriteString("func app[E](n int, f func(int) int fails E) int fails E {\n\treturn (try f(n)) + 1\n}\n\n")
	// gateUnit has no result, unlike the shared generator's value-returning
	// gate. Two branch tails are error values that fail this frame.
	g.b.WriteString("func gateUnit(n int) fails E1 needs Console {\n\tConsole.Println(\"gate \\(n)\")\n\tif n % 3 == 0 {\n\t\tE1{Code: n}\n\t} else if n % 3 == 1 {\n\t\tConsole.Println(\"gate one\")\n\t} else {\n\t\tif n > 4 {\n\t\t\tConsole.Println(\"gate two\")\n\t\t\tE1{Code: n + 100}\n\t\t} else {\n\t\t\tConsole.Println(\"gate small\")\n\t\t}\n\t}\n}\n\n")
	// settle is a machine step that gives no output: its result type
	// holds the prelude's Never, and settled matches on it.
	g.b.WriteString("func settle(n int) Decision[Pt, Never] {\n\tif n > 2 {\n\t\tEnter[Pt]{Next: Pt{A: n, B: 1}}\n\t} else if n < -2 {\n\t\tStay[Pt]{Next: Pt{A: 1, B: n}}\n\t} else if n == 0 {\n\t\tIgnore\n\t} else {\n\t\tReject\n\t}\n}\n\n")
	// calm's body names Crash[E], whose variant Failed[E] has a field of
	// the error row E: the row's Go error, no Go type parameter.
	g.b.WriteString("type Calm {}\n\ntype Failed[F] { Failure F }\n\ntype Crash[F] = Calm | Failed[F]\n\n")
	g.b.WriteString("func calm[E](n int, f func(int) int fails E) int fails E {\n\tlet c Crash[E] = Calm\n\tmatch c {\n\tcase Calm:\n\t\t(try f(n)) + 4\n\tcase Failed[E]:\n\t\tn\n\t}\n}\n\n")
	// tagged takes the length and a byte of a named type over string
	// (std fs.Text's form).
	g.b.WriteString("type Tag string\n\nfunc tagged(n int) int {\n\tlet t = Tag(\"ab\\(n)c\")\n\tlen(t)*100 + int(t[len(t)-2])\n}\n\n")
	// cut takes slices of a slice: both bounds, each one left out, three
	// indices (whose cap the append sees), a slice changed after it is
	// cut, and a low bound that can pass the end (a defect).
	g.b.WriteString("func cut(n int) int {\n\tlet xs = []int{n, n + 1, n + 2, n + 3, n + 4}\n\tlet a = xs[1:4]\n\tlet b = xs[:n%4+1]\n\tlet c = xs[n%3:]\n\tlet d = a[1:2:3]\n\tlet g = d.Append(7)\n\tlet mut e = xs[1:3]\n\te[0] = 99\n\tlen(a)*100000 + a[2]*10000 + len(b)*1000 + c[0]*100 + len(g)*10 + xs[1] + e[0] + len(xs[n%7:])\n}\n\n")
	// here's last parameter is the call's site, which a call leaves out.
	g.b.WriteString("func here(n int, at CallSite) int {\n\tn + at.Line*1000 + at.Column*7 + len(at.File) % 5\n}\n\n")
	g.b.WriteString("func settled(n int) int {\n\tmatch settle(n) {\n\tcase Enter[Pt] as d:\n\t\td.Next.A * 3\n\tcase Stay[Pt] as d:\n\t\td.Next.B - 7\n\tcase Ignore:\n\t\t5\n\tcase Reject:\n\t\t11\n\t}\n}\n\n")
}

// traced is a call of tr: an operation call inside an expression.
func (g *fgen) traced() string { return "tr(" + g.expr(1) + ")" }

// orderExpr is an int expression whose operands run calls, in a form the
// emitter orders: a binary, a unary, an index, a slice, record or text
// literal, a short-circuit operand; an earlier operand may be a defect
// (indexed) that must come before a later call's effect.
func (g *fgen) orderExpr() string {
	switch g.sub("expr", 14) {
	case 13:
		return "cut(" + g.traced() + ")"
	case 12:
		return "tagged(" + g.traced() + ")"
	case 11:
		return "here(" + g.traced() + ")"
	case 9:
		return "settled(" + g.traced() + ")"
	case 10:
		return fmt.Sprintf("calm(%s, func(n int) int { n * %d })", g.traced(), 2+g.n(3))
	case 0:
		return g.indexed(2) + " + " + g.traced()
	case 1:
		return g.traced() + " * 2 - " + g.traced()
	case 2:
		return "-" + g.traced()
	case 3:
		return fmt.Sprintf("([]int{%s, %s, %s})[(%s %% 3 + 3) %% 3]", g.traced(), g.local(), g.traced(), g.local())
	case 4:
		return fmt.Sprintf("len(\"a\\(%s)b\\(%s)c\\(%s)\")", g.traced(), g.local(), g.traced())
	case 5:
		return fmt.Sprintf("(Pt{A: %s, B: %s}).B", g.indexed(3), g.traced())
	case 6:
		return fmt.Sprintf("if %s > 0 && %s > 1 { 1 } else { 0 }", g.local(), g.traced())
	case 7:
		return fmt.Sprintf("if %s > 0 || %s > 1 { 2 } else { 3 }", g.local(), g.traced())
	}
	return fmt.Sprintf("(%s / (%s - %s)) + %s", g.local(), g.local(), g.local(), g.traced())
}

// orderStmt writes statement kind k of GenerateOrder, or reports that
// the current function cannot take it.
func (g *fgen) orderStmt(in string, budget, k int) bool {
	f := g.cur
	if k >= 7 {
		return g.tryStmt(in, budget, k)
	}
	if budget <= 0 || !f.impure() {
		return false
	}
	trace := ""
	if slices.Contains(f.needs, "Console") {
		trace = fmt.Sprintf("%s\t\tConsole.Println(\"%s.skip%d\")\n", in, f.name, g.n(100))
	}
	switch k {
	case 0:
		// A for-in whose continue skips the rest of the iteration.
		v := fmt.Sprintf("x%d", len(g.locals))
		fmt.Fprintf(&g.b, "%sfor %s in []int{%d, %d, %d} {\n", in, v, g.n(9), g.n(9), g.n(9))
		fmt.Fprintf(&g.b, "%s\tif %s %% 2 == %d {\n%s%s\t\tcontinue\n%s\t}\n", in, v, g.n(2), trace, in, in)
		saved := len(g.locals)
		g.locals = append(g.locals, v)
		g.stmt(in+"\t", budget-1)
		g.sinkFrom(in+"\t", saved)
		g.locals = g.locals[:saved]
		fmt.Fprintf(&g.b, "%s}\n", in)
	case 1:
		// A counting loop that counts first, so its continue ends.
		i := g.fresh()
		fmt.Fprintf(&g.b, "%slet mut %s = 0\n%sfor %s < %d {\n%s\t%s = %s + 1\n", in, i, in, i, 2+g.n(3), in, i, i)
		fmt.Fprintf(&g.b, "%s\tif %s == %d {\n%s%s\t\tcontinue\n%s\t}\n", in, i, 1+g.n(2), trace, in, in)
		saved := len(g.locals)
		g.stmt(in+"\t", budget-1)
		g.sinkFrom(in+"\t", saved)
		g.locals = g.locals[:saved]
		fmt.Fprintf(&g.b, "%s}\n", in)
	case 2, 3, 4, 5, 6:
		if trace == "" {
			return false // tr needs Console
		}
		g.callsStmt(in, budget, k)
	default:
		return false
	}
	return true
}

// shown is let, then a print of the new local where the function has
// Console: GenerateFail's locals reach the output only through a rare
// sink, so a value the IR computes wrong would otherwise go unseen.
func (g *fgen) shown(in, rhs string) {
	g.let(in, rhs)
	if slices.Contains(g.cur.needs, "Console") {
		v := g.locals[len(g.locals)-1]
		fmt.Fprintf(&g.b, "%sConsole.Println(\"%s.%s \\(%s)\")\n", in, g.cur.name, v, v)
	}
}

// tryStmt writes statement kind k (7 to 9) of orderStmt, the try forms:
// one try over an expression of two fallible calls (try of a non-call),
// a try inside an expression, after an operand that may be a defect, and
// catch arms that yield an if's value or continue a loop.
func (g *fgen) tryStmt(in string, budget, k int) bool {
	f := g.cur
	// chk fails E1 on an even argument (orderDecls): about half the calls
	// fail, so the failure paths run.
	chk := func() string { return "chk(" + g.expr(1) + ")" }
	switch {
	case k == 7 && f.fails("E1"):
		if slices.Contains(f.needs, "Console") && g.sub("try7", 3) == 0 {
			fmt.Fprintf(&g.b, "%stry gateUnit(%s)\n", in, g.expr(1))
			return true
		}
		g.shown(in, fmt.Sprintf("try %s * 2 + %s", chk(), chk()))
	case k == 8 && f.fails("E1"):
		switch g.n(3) {
		case 0:
			g.shown(in, fmt.Sprintf("%s + (try %s)", g.indexed(2), chk()))
		case 1:
			g.shown(in, fmt.Sprintf("-(try %s)", chk()))
		default:
			g.shown(in, fmt.Sprintf("([]int{try %s, %s})[0]", chk(), g.local()))
		}
	case k == 9:
		if g.sub("try9", 3) == 0 {
			// A catch of a match whose arms' tails are fallible calls.
			arm := func() string { return fmt.Sprintf("%s\t\t%s\n", in, chk()) }
			g.shown(in, fmt.Sprintf("match %s %% 3 {\n%s\tcase 0:\n%s%s\tcase 1:\n%s%s\tcase _:\n%s\t\t%s\n%s} catch {\n%s\tcase E1 as x:\n%s\t\tx.Code + 100\n%s}",
				g.expr(1), in, arm(), in, arm(), in, in, g.local(), in, in, in, in))
			return true
		}
		if budget > 0 && g.sub("try9loop", 2) == 0 {
			// A catch arm that continues the loop: the catch has no
			// value on that path.
			v := fmt.Sprintf("x%d", len(g.locals))
			fmt.Fprintf(&g.b, "%sfor %s in []int{%d, %d, %d} {\n", in, v, g.n(9), g.n(9), g.n(9))
			saved := len(g.locals)
			g.locals = append(g.locals, v)
			g.shown(in+"\t", fmt.Sprintf("chk(%s) catch {\n%s\t\tcase E1:\n%s\t\t\tcontinue\n%s\t}", v, in, in, in))
			g.sinkFrom(in+"\t", saved)
			g.locals = g.locals[:saved]
			fmt.Fprintf(&g.b, "%s}\n", in)
			return true
		}
		// An arm whose value is an if expression.
		arm := fmt.Sprintf("%s\tcase E1 as x:\n%s\t\tif x.Code > %d {\n%s\t\t\t%d\n%s\t\t} else {\n%s\t\t\tx.Code + 100\n%s\t\t}\n", in, in, g.n(6)-3, in, g.n(50), in, in, in)
		g.shown(in, fmt.Sprintf("%s catch {\n%s%s}", chk(), arm, in))
	default:
		return false
	}
	return true
}

// callsStmt writes statement kind k (2 to 6) of orderStmt: operation
// calls inside an expression, in an if's or else-if's condition, in a
// loop's condition, and in a for-in's domain (a slice, a Range).
func (g *fgen) callsStmt(in string, budget, k int) {
	f := g.cur
	switch k {
	case 2:
		switch g.sub("calls2", 4) {
		case 0:
			// Positional: each element sets the field of its place.
			g.shown(in, fmt.Sprintf("(Pt{%s, %s}).B - (Pt{%s, %s}).A", g.indexed(3), g.traced(), g.local(), g.expr(1)))
			return
		case 1:
			// A fallible call whose row is empty here, before a call.
			g.shown(in, fmt.Sprintf("app(%s, func(n int) int { n * %d }) - %s", g.expr(1), 2+g.n(3), g.traced()))
			return
		}
		g.shown(in, g.orderExpr())
	case 3:
		if g.sub("calls3", 3) == 0 {
			// An op-assignment whose value runs statements, one of which
			// assigns the target: the target is read first (old).
			init := g.local()
			a := g.fresh()
			fmt.Fprintf(&g.b, "%slet mut %s = %s\n%s%s += if tr(%s) > %d {\n%s\t%s = %s * 10\n%s\t1\n%s} else {\n%s\t2\n%s}\n%sConsole.Println(\"%s.%s \\(%s)\")\n",
				in, a, init, in, a, g.expr(1), g.n(4), in, a, a, in, in, in, in, in, f.name, a, a)
			return
		}
		fmt.Fprintf(&g.b, "%sif %s > %d {\n%s\tConsole.Println(\"%s.then\")\n%s} else if %s < 0 {\n%s\tConsole.Println(\"%s.elif\")\n%s}\n",
			in, g.traced(), g.n(5), in, f.name, in, g.traced(), in, f.name, in)
	case 4:
		// i < i%3+1 holds for i in 0..2 only: the loop ends.
		i := g.fresh()
		fmt.Fprintf(&g.b, "%slet mut %s = 0\n%sfor %s < tr(%s) %% 3 + 1 {\n%s\t%s = %s + 1\n", in, i, in, i, i, in, i, i)
		saved := len(g.locals)
		g.stmt(in+"\t", budget-1)
		g.sinkFrom(in+"\t", saved)
		g.locals = g.locals[:saved]
		fmt.Fprintf(&g.b, "%s}\n", in)
	case 5, 6:
		v := fmt.Sprintf("x%d", len(g.locals))
		domain := "span(" + g.traced() + ")"
		if k == 6 {
			domain = "0.." + g.traced() + " % 3"
		}
		fmt.Fprintf(&g.b, "%sfor %s in %s {\n", in, v, domain)
		saved := len(g.locals)
		g.locals = append(g.locals, v)
		g.stmt(in+"\t", budget-1)
		g.sinkFrom(in+"\t", saved)
		g.locals = g.locals[:saved]
		fmt.Fprintf(&g.b, "%s}\n", in)
	}
}
