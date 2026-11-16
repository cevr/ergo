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
// poll).
func GenerateOrder(seed uint64) string {
	g := &fgen{r: rand.New(rand.NewPCG(seed, seed^0x6f7264657267656e)), order: true}
	return g.program()
}

// orderKinds is the number of statement kinds orderStmt adds.
const orderKinds = 2

// orderDecls writes the declarations orderStmt's statements call.
func (g *fgen) orderDecls() {}

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
	default:
		return false
	}
	return true
}
