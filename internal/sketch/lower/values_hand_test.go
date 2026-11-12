package lower

import (
	"fmt"
	"slices"
	"strings"
)

// Hand-over (V7) in the value rule's seeded property (TestValueRuleAgrees).
// Each shared program declares generated user functions h1… of
// (s []int, t []int, k int) []int, each with a body drawn from handShapes,
// and rebinds let mut slices and Box fields through them: p = h(p, o, k)
// or p = h(o, p, k), once or in a loop, with o a binding or a literal.
// A shape whose result shares only the parameter p is passed in hands p
// over with no copy; a shape that returns the other parameter, the
// package let base, or either by a branch must copy, and the statement
// after each rebinding writes p[0] and prints o and p, and the program
// prints base at its end, so a wrong hand-over prints a changed o or base.
// The model runs the shape on copies (value semantics); the copying
// lowering "copy", which hands nothing over, is the reference.
// The draws use their own generator (valueGen.h), so the seeds keep the
// paths they drew before.

// handShape is a body of a generated function and its model.
type handShape struct {
	body  string
	model func(s, t []int, k int) []int
}

var handBase = []int{5, 6}

var handShapes = []handShape{
	{"s.Append(k)", func(s, t []int, k int) []int { return append(slices.Clone(s), k) }},
	{"s", func(s, t []int, k int) []int { return s }},
	{"t", func(s, t []int, k int) []int { return t }},
	{"base", func(s, t []int, k int) []int { return handBase }},
	{"if k > 4 {\n\t\treturn base\n\t}\n\ts", func(s, t []int, k int) []int {
		if k > 4 {
			return handBase
		}
		return s
	}},
	{"let mut c = s\n\tif len(c) > 0 {\n\t\tc[0] = k\n\t}\n\tc", func(s, t []int, k int) []int {
		c := slices.Clone(s)
		if len(c) > 0 {
			c[0] = k
		}
		return c
	}},
	{"s.Append(len(t))", func(s, t []int, k int) []int { return append(slices.Clone(s), len(t)) }},
	{"slices.Slice(s, 0, len(s))", func(s, t []int, k int) []int { return s }},
	{"slices.Slice(t, 0, len(t))", func(s, t []int, k int) []int { return t }},
	{"if k > 4 {\n\t\treturn s.Append(k)\n\t}\n\tt", func(s, t []int, k int) []int {
		if k > 4 {
			return append(slices.Clone(s), k)
		}
		return t
	}},
	{"let c = s\n\tc.Append(k)", func(s, t []int, k int) []int { return append(slices.Clone(s), k) }},
}

// handFn is a generated function.
type handFn struct {
	name  string
	shape handShape
}

// handFuncs draws the program's functions and writes their source.
func (g *valueGen) handFuncs() string {
	var b strings.Builder
	b.WriteString("let base = []int{5, 6}\n\n")
	for i := range 4 {
		f := handFn{name: fmt.Sprintf("h%d", i+1), shape: handShapes[g.h.IntN(len(handShapes))]}
		g.hands = append(g.hands, f)
		fmt.Fprintf(&b, "func %s(s []int, t []int, k int) []int {\n\t%s\n}\n\n", f.name, f.shape.body)
	}
	return b.String()
}

// hand writes, at most once, a rebinding through a generated function and
// runs it on the model.
func (g *valueGen) hand() {
	if !g.shared || len(g.hands) == 0 || g.h.IntN(2) == 0 {
		return
	}
	var roots []*vbind
	for _, b := range g.binds {
		if b.mut && (b.kind == kSlice || b.kind == kBox) {
			roots = append(roots, b)
		}
	}
	if len(roots) == 0 {
		return
	}
	b := roots[g.h.IntN(len(roots))]
	place, val := b.name, &b.val.s
	if b.kind == kBox {
		place = b.name + ".S"
		g.reached["hand field"] = true
	} else {
		g.reached["hand rebind"] = true
	}
	// The other argument: another slice binding, or a literal.
	var others []*vbind
	for _, o := range g.binds {
		if o != b && o.kind == kSlice {
			others = append(others, o)
		}
	}
	var other *vbind
	ox, ov := "", []int(nil)
	if len(others) > 0 && g.h.IntN(3) > 0 {
		other = others[g.h.IntN(len(others))]
		ox, ov = other.name, slices.Clone(other.val.s)
	} else {
		ov = []int{g.h.IntN(9), g.h.IntN(9)}
		ox = fmt.Sprintf("[]int{%d, %d}", ov[0], ov[1])
	}
	f := g.hands[g.h.IntN(len(g.hands))]
	k := g.h.IntN(9)
	second := g.h.IntN(2) == 0
	call := func(kx string) string {
		if second {
			return fmt.Sprintf("%s = %s(%s, %s, %s)", place, f.name, ox, place, kx)
		}
		return fmt.Sprintf("%s = %s(%s, %s, %s)", place, f.name, place, ox, kx)
	}
	run := func(k int) {
		var r []int
		if second {
			r = f.shape.model(ov, *val, k)
		} else {
			r = f.shape.model(*val, ov, k)
		}
		*val = slices.Clone(r)
	}
	if second {
		g.reached["hand second"] = true
	}
	if g.h.IntN(3) == 0 {
		g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s\n\t}", call(fmt.Sprintf("i + %d", k))))
		for i := range 3 {
			run(i + k)
		}
		g.reached["hand loop"] = true
	} else {
		g.line(call(fmt.Sprint(k)))
		run(k)
	}
	b.written = true
	// A write through p after the call: a result that shares o or base
	// would show it.
	g.line(fmt.Sprintf("if len(%s) > 0 {\n\t\t%s[0] = 77\n\t}", place, place))
	if len(*val) > 0 {
		(*val)[0] = 77
	}
	if other != nil {
		g.print(other.name, other.kind, other.val.digest(other.kind))
	}
	g.print(b.name, b.kind, b.val.digest(b.kind))
}
