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

// boxShape is a body of a generated function of (b Box, k int) Box, which
// a program calls with a dot (p = p.bN(k)), and its model. Its receiver is
// the first operand the rebinding hands over (types.md, Bindings 2).
type boxShape struct {
	body  string
	model func(b mval, k int) mval
}

var handBoxBase = mval{m: map[int]int{1: 2}, s: []int{5, 6}}

var boxShapes = []boxShape{
	{"b", func(b mval, k int) mval { return b }},
	{"bbase", func(b mval, k int) mval { return handBoxBase }},
	{"if k > 4 {\n\t\treturn bbase\n\t}\n\tb", func(b mval, k int) mval {
		if k > 4 {
			return handBoxBase
		}
		return b
	}},
	{"Box{..b, S: b.S.Append(k)}", func(b mval, k int) mval {
		return mval{m: b.m, s: append(slices.Clone(b.s), k)}
	}},
	{"Box{M: bbase.M, S: b.S}", func(b mval, k int) mval { return mval{m: handBoxBase.m, s: b.s} }},
}

// handFn is a generated function.
type handFn struct {
	name  string
	shape handShape
}

// boxFn is a generated function of a Box.
type boxFn struct {
	name  string
	shape boxShape
}

// handFuncs draws the program's functions and writes their source.
func (g *valueGen) handFuncs() string {
	var b strings.Builder
	b.WriteString("let base = []int{5, 6}\n\nlet bbase = Box{M: Map[int, int]{1: 2}, S: []int{5, 6}}\n\n")
	for i := range 4 {
		f := handFn{name: fmt.Sprintf("h%d", i+1), shape: handShapes[g.h.IntN(len(handShapes))]}
		g.hands = append(g.hands, f)
		fmt.Fprintf(&b, "func %s(s []int, t []int, k int) []int {\n\t%s\n}\n\n", f.name, f.shape.body)
	}
	for i := range 2 {
		f := boxFn{name: fmt.Sprintf("b%d", i+1), shape: boxShapes[g.h.IntN(len(boxShapes))]}
		g.boxes = append(g.boxes, f)
		fmt.Fprintf(&b, "func %s(b Box, k int) Box {\n\t%s\n}\n\n", f.name, f.shape.body)
	}
	return b.String()
}

// handDot writes a dot rebinding p = p.bN(k) of a let mut Box p, once or
// in a loop, then writes p's slice and Map, so a result that shares bbase
// shows it when the program prints bbase at its end.
func (g *valueGen) handDot(b *vbind) {
	f := g.boxes[g.h.IntN(len(g.boxes))]
	k := g.h.IntN(9)
	if g.h.IntN(3) == 0 {
		g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s = %s.%s(i + %d)\n\t}", b.name, b.name, f.name, k))
		for i := range 3 {
			b.val = f.shape.model(b.val, i+k).copy()
		}
		g.reached["hand dot loop"] = true
	} else {
		g.line(fmt.Sprintf("%s = %s.%s(%d)", b.name, b.name, f.name, k))
		b.val = f.shape.model(b.val, k).copy()
		g.reached["hand dot"] = true
	}
	b.written = true
	g.line(fmt.Sprintf("if len(%s.S) > 0 {\n\t\t%s.S[0] = 77\n\t}\n\t%s.M[1] = 77", b.name, b.name, b.name))
	if len(b.val.s) > 0 {
		b.val.s[0] = 77
	}
	if b.val.m == nil {
		b.val.m = map[int]int{}
	}
	b.val.m[1] = 77
	g.print(b.name, b.kind, b.val.digest(b.kind))
}

// sortStmt writes, at most once, a call of slices.Sort, whose runtime/v1
// body has an owned form that sorts in place (v1.Body.Owned): on a slice
// binding the program reads again (it prints every binding at its end),
// whose call must copy, or on a fresh slice, which it may take over
// (types.md, Bindings 2, consumed arguments). A consumed live operand
// would print the binding sorted.
func (g *valueGen) sortStmt() {
	if !g.shared || g.h.IntN(3) > 0 {
		return
	}
	var live []*vbind
	for _, b := range g.binds {
		if b.kind == kSlice {
			live = append(live, b)
		}
	}
	if len(live) > 0 && g.h.IntN(2) == 0 {
		b := live[g.h.IntN(len(live))]
		g.line(fmt.Sprintf("Console.Println(\"\\(ds(slices.Sort(%s)))\")", b.name))
		fmt.Fprintf(&g.want, "%d\n", dsModel(slices.Sorted(slices.Values(b.val.s))))
		g.print(b.name, b.kind, b.val.digest(b.kind))
		g.reached["sort live"] = true
		return
	}
	xs := []int{g.h.IntN(9), g.h.IntN(9), g.h.IntN(9)}
	g.line(fmt.Sprintf("Console.Println(\"\\(ds(slices.Sort([]int{%d, %d, %d})))\")", xs[0], xs[1], xs[2]))
	fmt.Fprintf(&g.want, "%d\n", dsModel(slices.Sorted(slices.Values(xs))))
	g.reached["sort dead"] = true
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
	if b.kind == kBox && len(g.boxes) > 0 && g.h.IntN(2) == 0 {
		g.handDot(b)
		return
	}
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
