package irspike

import "fmt"

// conversion is an int converted from another scalar (IRG4b r2): an
// int64 or a uint8, or a float64, which the runtime's checked helper
// converts. Some floats are past int's range (1e300), and both paths
// must fail the same way; the huge control (gen.go) is one that main
// always reaches.
func (g *gen) conversion(d int) string {
	switch g.n(4) {
	case 0:
		return "int(" + g.expr(tI64, d-1) + ")"
	case 1:
		return "int(" + g.expr(tU8, d-1) + ")"
	}
	// A constant float that is not whole does not check: the converted
	// value has a float variable in it, or an int variable widened.
	v := g.variable(tF64)
	if v == "" {
		if w := g.variable(tInt); w != "" && g.n(2) == 0 {
			v = "float64(" + w + ")"
		} else if w != "" {
			v = "(float64(" + w + ") + 0.5)"
		} else {
			return "int(" + g.expr(tI64, d-1) + ")"
		}
	}
	if g.n(10) == 0 {
		return "int(" + v + " * 1e300)"
	}
	return "int(" + v + " * " + g.expr(tF64, d-1) + ")"
}

// values writes the value-rule form (IRG4b): two Appends to one slice
// that has room past its length, a Put and a Delete on one Map, then a
// for-in over a Map and over a string. Ergo's slices and Maps are values
// (V3): each Append and each write leaves its source as it was, so the
// second Append does not see the first one's element, and the Map keeps
// its entries. The locals it binds are scalars at the top of a function
// whose result is a string, which ends with the text of every scalar in
// scope (trace): a body that writes into a shared array or Map prints
// another value.
func (g *gen) values() {
	a, b, c := g.fresh(tInts, false), g.fresh(tInts, false), g.fresh(tInts, false)
	m, put, del := g.fresh(tInt, false), g.fresh(tInt, false), g.fresh(tInt, false)
	sum, runes := g.fresh(tInt, true), g.fresh(tInt, true)
	v1, v2 := g.fresh(tInt, false), g.fresh(tInt, false)
	// Go's append doubles a full slice's capacity: a literal of n
	// elements and two Appends leave room for at least one more.
	fmt.Fprintf(&g.b, "\tlet %s []int = %s.Append(%d).Append(%d)\n", a.name, g.leaf(tInts), g.n(9), g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s []int = %s.Append(%d)\n", b.name, a.name, 10+g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s []int = %s.Append(%d)\n", c.name, a.name, 20+g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s int = %s[len(%s) - 1] * 100 + %s[len(%s) - 1] + len(%s) * 1000\n", v1.name, b.name, b.name, c.name, c.name, a.name)
	fmt.Fprintf(&g.b, "\tlet %s Map[string, int] = Map[string, int]{\"a\": %s, \"b\": %d}\n", m.name, g.expr(tInt, 1), g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s Map[string, int] = %s.Put(\"c\", %d)\n", put.name, m.name, g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s Map[string, int] = %s.Delete(\"a\")\n", del.name, m.name)
	fmt.Fprintf(&g.b, "\tlet mut %s = 0\n\tfor k, x in %s {\n\t\t%s = %s + x * len(k) + len(%s) * 10\n\t}\n", sum.name, put.name, sum.name, sum.name, del.name)
	fmt.Fprintf(&g.b, "\tlet mut %s = 0\n\tfor i, r in %s {\n\t\tlet _ = r\n\t\t%s = %s + i\n\t}\n", runes.name, g.leaf(tStr), runes.name, runes.name)
	fmt.Fprintf(&g.b, "\tlet %s int = %s * 1000 + len(%s) * 100 + %s\n", v2.name, sum.name, m.name, runes.name)
	g.appendPlaces()
	for _, v := range []gvar{a, b, c} {
		g.used[v.name] = true
	}
	for _, v := range []gvar{m, put, del} {
		g.used[v.name] = true
	}
	g.used[sum.name], g.used[runes.name] = true, true
	g.scope = append(g.scope, v1, v2)
}

// appendPlaces writes Appends to a place (FLIP F3, Go's builtin append):
// a let mut from a literal, Appends assigned back to it (in place), then
// an Append of it to another name while it stays live, then one more
// Append back to it. Go's append leaves room past the length, so the
// third write would land in the array the second one's result holds;
// the value rule clips that source (valueSource), so it does not, and
// an append that skips the clip prints another value (mutant
// f3-append-unclipped).
func (g *gen) appendPlaces() {
	p, r, v := g.fresh(tInts, true), g.fresh(tInts, false), g.fresh(tInt, false)
	fmt.Fprintf(&g.b, "\tlet mut %s []int = %s\n", p.name, g.leaf(tInts))
	for i := 0; i < 1+g.n(3); i++ {
		fmt.Fprintf(&g.b, "\t%s = %s.Append(%d)\n", p.name, p.name, 30+g.n(9))
	}
	fmt.Fprintf(&g.b, "\tlet %s []int = %s.Append(%d)\n", r.name, p.name, 40+g.n(9))
	fmt.Fprintf(&g.b, "\t%s = %s.Append(%d)\n", p.name, p.name, 50+g.n(9))
	fmt.Fprintf(&g.b, "\tlet %s int = %s[len(%s) - 1] * 100 + %s[len(%s) - 1] + len(%s) * 1000 + len(%s) * 10000\n", v.name, p.name, p.name, r.name, r.name, r.name, p.name)
	g.used[p.name], g.used[r.name] = true, true
	g.scope = append(g.scope, v)
}
