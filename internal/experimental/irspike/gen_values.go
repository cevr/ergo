package irspike

import "fmt"

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
	for _, v := range []gvar{a, b, c} {
		g.used[v.name] = true
	}
	for _, v := range []gvar{m, put, del} {
		g.used[v.name] = true
	}
	g.used[sum.name], g.used[runes.name] = true, true
	g.scope = append(g.scope, v1, v2)
}
