package irspike

import (
	"fmt"
	"math/rand/v2"
	"slices"
)

// GenerateOrder writes the program of seed for F7's G2 families (lane GP):
// GenerateFail's failure programs, with statements whose order the IR
// must keep as the emitter does: a continue in a polling loop, which
// polls before the next iteration (the emitter jumps to the back-edge
// poll); impure calls (tr prints its argument) inside an expression,
// after an operand that may be a defect, in conditions and in for-in
// domains.
func GenerateOrder(seed uint64) string {
	g := &fgen{r: rand.New(rand.NewPCG(seed, seed^0x6f7264657267656e)), order: true}
	return g.program()
}

// orderKinds is the number of statement kinds orderStmt adds.
const orderKinds = 7

// orderDecls writes the declarations orderStmt's statements call: tr
// prints its argument (the order of calls shows in the output) and span
// is an impure call whose value a for-in ranges over.
func (g *fgen) orderDecls() {
	g.b.WriteString("type Pt { A int, B int }\n\n")
	g.b.WriteString("func tr(n int) int needs Console {\n\tConsole.Println(\"tr \\(n)\")\n\tn\n}\n\n")
	g.b.WriteString("func span(n int) []int needs Console {\n\tConsole.Println(\"span \\(n)\")\n\t[]int{n, n + 1}\n}\n\n")
}

// traced is a call of tr: an operation call inside an expression.
func (g *fgen) traced() string { return "tr(" + g.expr(1) + ")" }

// orderExpr is an int expression whose operands run calls, in a form the
// emitter orders: a binary, a unary, an index, a slice, record or text
// literal, a short-circuit operand; an earlier operand may be a defect
// (indexed) that must come before a later call's effect.
func (g *fgen) orderExpr() string {
	switch g.n(9) {
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

// callsStmt writes statement kind k (2 to 6) of orderStmt: operation
// calls inside an expression, in an if's or else-if's condition, in a
// loop's condition, and in a for-in's domain (a slice, a Range).
func (g *fgen) callsStmt(in string, budget, k int) {
	f := g.cur
	switch k {
	case 2:
		g.let(in, g.orderExpr())
	case 3:
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
