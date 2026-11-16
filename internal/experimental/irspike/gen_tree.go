package irspike

import (
	"fmt"
	"strings"
)

// treeFuncs are the functions trees writes (IRG3b): matches the emitter's
// decision tree lowers (matchtree.go): record, constant, bind and nested
// patterns, several subjects, or-patterns and wildcards whose arms
// several leaves reach (a shared arm), and a break inside a match; catches
// with patterns, a loop-carried union local (the scalar form), a returned
// call's converted value, a row conversion, named scalars' text, and an if
// or a match as an operator's operand.
var treeFuncs = []string{"pairs", "P.points", "trees", "flags", "stops", "risky", "caught", "part", "walk", "leafOf", "lift", "label", "mix"} // points takes a P first: P's method

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
	// A catch with record patterns, an or-pattern, `case err` and a
	// wildcard: the emitter's tree on the error (patternCatch); total and
	// partial.
	g.b.WriteString("type E1 { Code int }\n\ntype E2 { Why string }\n\ntype Bad = E1 | E2\n\n")
	fmt.Fprintf(&g.b, "func risky(n int) int fails E1 | E2 {\n\tif n %% 3 == 0 {\n\t\treturn E1{Code: n}\n\t}\n\tif n %% 3 == 1 {\n\t\treturn E2{Why: \"w\\(n %% %d)\"}\n\t}\n\tn * 2\n}\n\n", 2+g.n(3))
	fmt.Fprintf(&g.b, "func caught(n int) int {\n\trisky(n) catch {\n%s\t}\n}\n\n", block("\t", g.pick([]string{
		"case E1{Code: 0}:\n\t-100",
		"case E1{Code: 3}, E2{Why: \"w1\"}:\n\t33",
		"case E2{Why: \"w0\"}:\n\t-7",
		"case E1{Code: 6} as e:\n\te.Code * 10",
		[]string{"!case err:\n\tmatch err {\n\tcase E1 as e:\n\t\te.Code + 500\n\tcase E2:\n\t\t-1\n\t}", "!case _:\n\t-1", "!case err:\n\tlet bad Bad = err\n\tmatch bad {\n\tcase E1 as e:\n\t\te.Code + 700\n\tcase E2:\n\t\t-2\n\t}"}[g.n(3)],
	})))
	fmt.Fprintf(&g.b, "func part(n int) int fails E1 | E2 {\n\tlet v = try risky(n) catch {\n%s\t}\n\tv + 1\n}\n\n", block("\t", g.pick([]string{
		"!case E1{Code: " + fmt.Sprint(3*g.n(3)) + "}:\n\t1000",
		"!case E2{Why: \"w1\"}, E2{Why: \"w2\"}:\n\t2000",
	})))
	// A loop-carried union local, which the lowering gives the scalar
	// form (scalar.go): its let and assignments scatter, a match reads
	// its tag, a call's operand gathers.
	fmt.Fprintf(&g.b, "func walk(t Tree, n int) int {\n\tlet mut cur = t\n\tlet mut acc = 0\n\tlet mut i = 0\n\tfor i < n {\n\t\tmatch cur {\n\t\tcase Leaf as l:\n\t\t\tacc += l.N\n\t\t\tcur = Node{L: l, R: Leaf{N: i}, K: i + %d}\n\t\tcase Node as nd:\n\t\t\tacc += nd.K\n\t\t\tcur = nd.%s\n\t\t}\n\t\ti += 1\n\t}\n\tlet s = sum(cur)\n\tacc * 100 + s\n}\n\n", g.n(3), []string{"L", "R"}[g.n(2)])
	// A fallible call's variant returned into the frame's union: the
	// failure leaves first, then the value converts.
	fmt.Fprintf(&g.b, "func leafOf(n int) Leaf fails E1 | E2 {\n\tif n %% %d == 0 {\n\t\treturn E1{Code: n}\n\t}\n\tLeaf{N: n}\n}\n\nfunc lift(n int) Tree fails E1 | E2 {\n\treturn leafOf(n)\n}\n\n", 2+g.n(3))
	// Named scalars' text; an if and a match as operands of an operator,
	// after an operand an arm assigns (the operand is read first).
	g.b.WriteString("type Word string\n\ntype On bool\n\n")
	fmt.Fprintf(&g.b, "func label(m Meters, w Word, o On) string {\n\t\"m=\\(m) w=\\(w) o=\\(o) \\(m * %d)\"\n}\n\n", 1+g.n(3))
	fmt.Fprintf(&g.b, "func mix(xs []P) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + (match x {\n\t\tcase P{X: %d}:\n\t\t\tn = 100\n\t\t\t1\n\t\tcase P as q:\n\t\t\tq.Y\n\t\t})\n\t\tn = n * (if x.Y > %d {\n\t\t\tn = n + 1\n\t\t\t2\n\t\t} else {\n\t\t\t3\n\t\t})\n\t}\n\tn\n}\n\n", g.n(4), g.n(4))
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
	for n := 0; n < 10; n++ {
		fmt.Fprintf(&g.b, "\tlet p%d = part(%d) catch {\n\tcase E1 as e:\n\t\te.Code - 50\n\tcase E2 as e:\n\t\tlen(e.Why) - 60\n\t}\n", n, n)
		fmt.Fprintf(&g.b, "\tConsole.Println(\"catch %d \\(caught(%d)) \\(p%d)\")\n", n, n, n)
	}
	for d := 0; d < 4; d++ {
		fmt.Fprintf(&g.b, "\tConsole.Println(\"walk %d \\(walk(build(%d), %d))\")\n", d, d, g.n(7))
	}
	for n := 0; n < 6; n++ {
		fmt.Fprintf(&g.b, "\tlet t%d = lift(%d) catch {\n\tcase E1 as e:\n\t\tLeaf{N: e.Code + 40}\n\tcase E2:\n\t\tLeaf{N: -4}\n\t}\n", n, n)
		fmt.Fprintf(&g.b, "\tConsole.Println(\"lift %d \\(sum(t%d)) \\(label(Meters(%d), Word(\"w%d\"), On(%v)))\")\n", n, n, n*7, g.n(3), g.n(2) == 0)
	}
	ps = ps[:0]
	for i := 0; i < 5; i++ {
		ps = append(ps, fmt.Sprintf("P{X: %d, Y: %d}", g.n(4), g.n(6)))
	}
	fmt.Fprintf(&g.b, "\tConsole.Println(\"mix \\(mix([]P{%s}))\")\n", strings.Join(ps, ", "))
}
