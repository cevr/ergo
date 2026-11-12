package irspike

import (
	"fmt"
	"strings"
)

// treeFuncs are the functions trees writes (IRG3b): matches the emitter's
// decision tree lowers (matchtree.go): record, constant, bind and nested
// patterns, several subjects, or-patterns and wildcards whose arms
// several leaves reach (a shared arm), and a break inside a match.
var treeFuncs = []string{"pairs", "P.points", "trees", "flags", "stops"} // points takes a P first: P's method

// pick is a random subset of arms in their order, with every arm whose
// text starts with "!" kept (its "!" dropped): dropping an arm never makes
// a later one unreachable, so the specific arms come first and the general
// ones after.
func (g *ugen) pick(arms []string) []string {
	var out []string
	for _, a := range arms {
		switch {
		case strings.HasPrefix(a, "!"):
			out = append(out, a[1:])
		case g.n(4) != 0:
			out = append(out, a)
		}
	}
	return out
}

// block is arms written one per line at indent in.
func block(in string, arms []string) string {
	var b strings.Builder
	for _, a := range arms {
		b.WriteString(in + strings.ReplaceAll(a, "\n", "\n"+in) + "\n")
	}
	return b.String()
}

// trees writes treeFuncs. P is a record; Small and Tree are GenerateUnion's
// unions (Small flat or an interface by the seed, Tree an interface).
func (g *ugen) trees() {
	g.b.WriteString("type P { X int, Y int }\n\n")
	c := func(n int) string { return fmt.Sprint(g.n(n)) }
	// Two subjects: a union and an int; or-patterns that bind.
	fmt.Fprintf(&g.b, "func pairs(s Small, n int) string {\n\tmatch (s, n) {\n%s\t}\n}\n\n", block("\t", g.pick([]string{
		"case (B{N: 0}, _):\n\t\"b0\"",
		"case (B{N: m}, " + c(3) + "), (B{N: m}, " + fmt.Sprint(3+g.n(3)) + "):\n\t\"bm \\(m)\"",
		"case (C{S: \"c" + c(3) + "\"}, k):\n\t\"cs \\(k)\"",
		"case (B as b, 7):\n\t\"b7 \\(b.N)\"",
		"case (A, k), (C{S: \"z\"}, k):\n\t\"ak \\(k)\"",
		"!case _:\n\t\"rest\"",
	})))
	// One record subject: constant fields, binds, an or-pattern with no
	// binds, `as`, in value position.
	fmt.Fprintf(&g.b, "func points(p P) int {\n\tlet r = match p {\n%s\t}\n\tr + 1\n}\n\n", block("\t", g.pick([]string{
		"case P{X: 0, Y: y}:\n\ty * 3",
		"case P{X: x, Y: " + fmt.Sprint(2+g.n(3)) + "}:\n\tx * 2",
		"case P{X: 1}, P{Y: 1}:\n\t7",
		"case P{X: " + fmt.Sprint(2+g.n(5)) + ", Y: _}:\n\t-5",
		"!case P as q:\n\tq.X - q.Y",
	})))
	// Nested union patterns on the recursive union, a statement match
	// that assigns.
	fmt.Fprintf(&g.b, "func trees(t Tree) int {\n\tlet mut n = 0\n\tmatch t {\n%s\t}\n\tn\n}\n\n", block("\t", g.pick([]string{
		"case Node{L: Leaf{N: 0}, R: r}:\n\tn = 100 + sum(r)",
		"case Node{L: Node as l, K: " + c(3) + "}:\n\tn = 200 + l.K",
		"case Node{R: Leaf{N: " + c(4) + "}}, Node{L: Leaf{N: 1}}:\n\tn = 300",
		"!case Node{K: k}:\n\tn = k",
		"!case Leaf{N: v}:\n\tn = v - 1",
	})))
	// Constants of bool and string subjects, the function's result.
	fmt.Fprintf(&g.b, "func flags(b bool, s string) string {\n\tmatch (b, s) {\n%s\t}\n}\n\n", block("\t", g.pick([]string{
		"case (true, \"a\"):\n\t\"ta\"",
		"case (false, \"b\"), (true, \"c\"):\n\t\"fb\"",
		"case (false, w):\n\t\"f\" + w",
		"!case (_, w):\n\t\"t\" + w",
	})))
	// A break inside a match, in a shared arm and in an arm of its own:
	// it leaves the loop, not Go's switch.
	fmt.Fprintf(&g.b, "func stops(xs []P) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tmatch x {\n%s\t\t}\n\t\tn += 1\n\t}\n\tn\n}\n\n", block("\t\t", g.pick([]string{
		"case P{X: " + c(4) + "}, P{Y: " + c(4) + "}:\n\tbreak",
		"case P{X: 9, Y: v}:\n\tn += v\n\tbreak",
		"!case P{X: u}:\n\tn += u",
	})))
}

// treeMain writes main's lines for treeFuncs.
func (g *ugen) treeMain() {
	for i := 0; i < 6; i++ {
		x, y := g.n(6)-1, g.n(6)-1
		cs := []string{"c0", "c1", "c2", "z"}[g.n(4)]
		cv := "C{S: \"" + cs + "\"}"
		if strings.Contains(g.b.String(), "type C { S string, T string }") {
			cv = "C{S: \"" + cs + "\", T: \"t\"}"
		}
		fmt.Fprintf(&g.b, "\tConsole.Println(\"pairs %d \\(pairs(mk(%d), %d)) \\(pairs(B{N: %d}, %d)) \\(pairs(%s, %d))\")\n", i, x, y, g.n(3), g.n(8), cv, y)
		fmt.Fprintf(&g.b, "\tConsole.Println(\"points %d \\(points(P{X: %d, Y: %d})) \\(flags(%v, \"%s\"))\")\n", i, x, y, g.n(2) == 0, []string{"a", "b", "c", "d"}[g.n(4)])
	}
	for d := 0; d < 4; d++ {
		fmt.Fprintf(&g.b, "\tConsole.Println(\"trees %d \\(trees(build(%d))) \\(trees(Node{L: Leaf{N: %d}, R: Leaf{N: %d}, K: %d}))\")\n", d, d, g.n(2), g.n(5), g.n(3))
	}
	var ps []string
	for i := 0; i < 6; i++ {
		ps = append(ps, fmt.Sprintf("P{X: %d, Y: %d}", g.n(10), g.n(5)))
	}
	fmt.Fprintf(&g.b, "\tConsole.Println(\"stops \\(stops([]P{%s}))\")\n", strings.Join(ps, ", "))
}
