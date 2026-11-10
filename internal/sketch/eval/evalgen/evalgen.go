// Package evalgen generates well-typed Ergo programs over values and
// control (L7 EV2) together with their expected result from an
// independent reference model. Two properties read it: the evaluator's
// (internal/sketch/eval, TestEvalAgreesWithModel), which holds the
// check-time evaluator to the model in process, and the lowering's
// (internal/sketch/lower, TestEvalAgreesWithGo), which holds the native
// Go program, under the default and the reference lowerings, to the same
// model and the evaluator.
//
// A program is one function r() whose body runs a seeded sequence of
// statements over locals of every admitted value form: integers of three
// widths, strings (bytes, runes, invalid UTF-8 after a slice), slices,
// a slice of slices, Maps, arrays, records holding slices, and a union;
// through loops (for, for-in over slices, arrays, strings and ranges,
// break and continue), match on the union and on Get's Option, generic
// functions, a function literal called where it is written that writes
// the locals it captures, package lets in initialization order, and
// conversions. Aliasing is the point: `b = a` then a write through one of
// them must not be seen through the other (owner q26). r returns every
// local in one record, and the test asserts it equals the model's value.
//
// The model is a plain Go interpreter of the generated statements with
// copy-on-assignment: each assignment deep-copies, so it shares nothing
// and has no ownership rule to get wrong. It never reads the checker, the
// evaluator or the lowering. A defect the Go target raises (an index out
// of range, an integer divided by zero) is a defect of the model too; a
// program the model says defects asserts against a sentinel.
package evalgen

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
)

// Program is one generated program.
type Program struct {
	Seed uint64
	// Source is the Ergo source: the declarations, r() and the one test
	// V that asserts r() == Want (or the sentinel, when it defects).
	Source string
	// Defect reports that the model's r() hits a defect.
	Defect bool
	// Want is the model's value of r() as an Ergo literal.
	Want string
	// Kinds counts the statement and expression forms the program uses,
	// so a property can require that its seeds reach every form.
	Kinds map[string]int
}

// Kinds lists every form a run of seeds must reach.
var Kinds = []string{
	"int-assign", "int-op-assign", "narrow-wrap", "conversion",
	"slice-append-handover", "slice-append-other", "slice-alias", "slice-index-write", "slice-slice",
	"grid-nested-write", "grid-row-alias", "grid-append",
	"map-write", "map-delete", "map-put", "map-alias", "map-get-match",
	"array-write", "array-alias", "array-default",
	"string-concat", "string-slice", "string-text",
	"record-field-write", "record-slice-write", "record-alias",
	"union-assign", "union-match",
	"for-cond", "for-slice", "for-array", "for-string", "for-range", "break", "continue",
	"if", "literal-capture", "literal-value",
	"generic-call", "package-let", "recursion", "float-text",
	"defect-index", "defect-divide", "slice-bound",
}

// defect is the model's panic for a defect of the Go target.
type defect struct{ what string }

// Generate makes the program of seed.
func Generate(seed uint64) Program {
	g := &gen{r: rand.New(rand.NewPCG(seed, 0x65763267)), kinds: map[string]int{}}
	g.m = newModel(g.r.IntN(5) + 1)
	g.prologue()
	n := 10 + g.r.IntN(14)
	for i := 0; i < n; i++ {
		g.stmt(2)
	}
	g.epilogue()
	p := Program{Seed: seed, Kinds: g.kinds}
	var want string
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(defect); !ok {
					panic(r)
				}
				p.Defect = true
			}
		}()
		for _, s := range g.body {
			s.run(g.m)
		}
		want = g.m.out()
	}()
	if p.Defect {
		want = sentinel
	}
	p.Want = want
	p.Source = g.source(want)
	return p
}

const sentinel = `Out{n: -1, w: 0, u: 0, s: "sentinel", xs: []int{}, ys: []int{}, g: [][]int{}, m: Map[string, int]{}, m2: Map[string, int]{}, p: Pair{a: 0, xs: []int{}}, q: Pair{a: 0, xs: []int{}}, sh: Dot, f: ""}`

// decls are the program's fixed declarations: the result record, the
// union, the helpers r() calls and two package lets, the second after
// the first in initialization order though written before it.
func decls(base int) string {
	return `type Pair { a int, xs []int }

type Shape = Dot{} | Line{ n int } | Grid{ cells []int }

type Out {
	n int,
	w int8,
	u uint16,
	s string,
	xs []int,
	ys []int,
	g [][]int,
	m Map[string, int],
	m2 Map[string, int],
	p Pair,
	q Pair,
	sh Shape,
	f string,
}

let table = []int{base, base * 3, base - 7}

let base = seed() + ` + strconv.Itoa(base) + `

func seed() int { 10 }

func idx(n int, i int) int { ((i % n) + n) % n }

func at(xs []int, i int) int {
	if len(xs) == 0 {
		return -1
	}
	xs[idx(len(xs), i)]
}

func pick[T](xs []T, i int, d T) T {
	if len(xs) == 0 {
		return d
	}
	xs[idx(len(xs), i)]
}

func unbox[T](o Option[T], d T) T {
	match o {
	case Some[T]{Value: v}:
		v
	case None:
		d
	}
}

func sumArr(a [4]int) int {
	let mut s = 0
	for i, x in a {
		s += (i + 1) * x
	}
	s
}

func runes(t string) int {
	let mut n = 0
	for i, c in t {
		n += i + int(c) % 7
	}
	n
}

func area(s Shape) int {
	match s {
	case Dot:
		0
	case Line{n: k}:
		k
	case Grid{cells: cs}:
		len(cs) + at(cs, 1)
	}
}

func fib(n int) int {
	if n < 2 {
		return n
	}
	fib(n - 1) + fib(n - 2)
}

`
}

// source is the program: the declarations, r() and the test.
func (g *gen) source(want string) string {
	var b strings.Builder
	b.WriteString(decls(int(g.m.base - 10)))
	b.WriteString("func r() Out {\n")
	for _, l := range g.locals {
		b.WriteString("\t" + l + "\n")
	}
	for _, s := range g.body {
		s.write(&b, 1)
	}
	b.WriteString("\tOut{n: i0 + 31 * i1 + 7 * i2 + sumArr(a0) + 3 * sumArr(a1), w: w0, u: u0, s: t0, xs: s0, ys: s1, g: g0, m: m0, m2: m1, p: p0, q: p1, sh: sh0, f: \"\\(float64(i0) / float64(i1)) \\(float32(i2) / float32(i0))\"}\n}\n\n")
	b.WriteString("test V {\n\tassert r() == " + want + "\n}\n")
	return b.String()
}

// ---------------------------------------------------------------- the model

// pair and shape are the model's record and union values.
type pair struct {
	a  int64
	xs []int64
}

type shape struct {
	kind  string // "Dot", "Line", "Grid"
	n     int64
	cells []int64
}

// model is the reference state: each local by name.
type model struct {
	base   int64
	ints   map[string]int64
	w      int8
	u      uint16
	str    string
	slices map[string][]int64
	grid   [][]int64
	maps   map[string]map[string]int64
	arrs   map[string][4]int64
	pairs  map[string]pair
	sh     shape
}

func newModel(baseOffset int) *model {
	return &model{
		base:   int64(10 + baseOffset),
		ints:   map[string]int64{},
		slices: map[string][]int64{},
		maps:   map[string]map[string]int64{},
		arrs:   map[string][4]int64{},
		pairs:  map[string]pair{},
	}
}

func cloneInts(xs []int64) []int64 { return append([]int64{}, xs...) }

func cloneGrid(g [][]int64) [][]int64 {
	out := make([][]int64, len(g))
	for i, row := range g {
		out[i] = cloneInts(row)
	}
	return out
}

func cloneMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func clonePair(p pair) pair { return pair{a: p.a, xs: cloneInts(p.xs)} }

func cloneShape(s shape) shape { return shape{kind: s.kind, n: s.n, cells: cloneInts(s.cells)} }

func (m *model) table() []int64 { return []int64{m.base, m.base * 3, m.base - 7} }

func idx(n, i int64) int64 { return ((i % n) + n) % n }

func at(xs []int64, i int64) int64 {
	if len(xs) == 0 {
		return -1
	}
	return xs[idx(int64(len(xs)), i)]
}

func sumArr(a [4]int64) int64 {
	var s int64
	for i, x := range a {
		s += int64(i+1) * x
	}
	return s
}

func runes(t string) int64 {
	var n int64
	for i, c := range t {
		n += int64(i) + int64(c)%7
	}
	return n
}

func area(s shape) int64 {
	switch s.kind {
	case "Line":
		return s.n
	case "Grid":
		return int64(len(s.cells)) + at(s.cells, 1)
	}
	return 0
}

func fib(n int64) int64 {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

// out is r()'s value as an Ergo literal.
func (m *model) out() string {
	n := m.ints["i0"] + 31*m.ints["i1"] + 7*m.ints["i2"] + sumArr(m.arrs["a0"]) + 3*sumArr(m.arrs["a1"])
	f := strconv.FormatFloat(float64(m.ints["i0"])/float64(m.ints["i1"]), 'g', -1, 64) + " " +
		strconv.FormatFloat(float64(float32(m.ints["i2"])/float32(m.ints["i0"])), 'g', -1, 32)
	return fmt.Sprintf("Out{n: %d, w: %d, u: %d, s: %s, xs: %s, ys: %s, g: %s, m: %s, m2: %s, p: %s, q: %s, sh: %s, f: %s}",
		n, m.w, m.u, strconv.Quote(m.str), intsLit(m.slices["s0"]), intsLit(m.slices["s1"]), gridLit(m.grid),
		mapLit(m.maps["m0"]), mapLit(m.maps["m1"]), pairLit(m.pairs["p0"]), pairLit(m.pairs["p1"]), shapeLit(m.sh), strconv.Quote(f))
}

func intsLit(xs []int64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.FormatInt(x, 10)
	}
	return "[]int{" + strings.Join(parts, ", ") + "}"
}

func gridLit(g [][]int64) string {
	parts := make([]string, len(g))
	for i, row := range g {
		parts[i] = intsLit(row)
	}
	return "[][]int{" + strings.Join(parts, ", ") + "}"
}

func mapLit(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = strconv.Quote(k) + ": " + strconv.FormatInt(m[k], 10)
	}
	return "Map[string, int]{" + strings.Join(parts, ", ") + "}"
}

func pairLit(p pair) string { return fmt.Sprintf("Pair{a: %d, xs: %s}", p.a, intsLit(p.xs)) }

func shapeLit(s shape) string {
	switch s.kind {
	case "Line":
		return fmt.Sprintf("Line{n: %d}", s.n)
	case "Grid":
		return "Grid{cells: " + intsLit(s.cells) + "}"
	}
	return "Dot"
}

// ---------------------------------------------------------------- the generator

// gen builds a program's statements: each one both the Ergo text and the
// model's run of it.
type gen struct {
	r      *rand.Rand
	m      *model
	locals []string
	body   []stmt
	kinds  map[string]int
	fresh  int
	loops  int // the enclosing loops of the statement being made
}

// stmt is one generated statement.
type stmt struct {
	write func(b *strings.Builder, depth int)
	run   func(m *model)
}

func line(text string) func(b *strings.Builder, depth int) {
	return func(b *strings.Builder, depth int) {
		b.WriteString(strings.Repeat("\t", depth) + text + "\n")
	}
}

func block(head string, inner []stmt, tail string) func(b *strings.Builder, depth int) {
	return func(b *strings.Builder, depth int) {
		b.WriteString(strings.Repeat("\t", depth) + head + "\n")
		for _, s := range inner {
			s.write(b, depth+1)
		}
		b.WriteString(strings.Repeat("\t", depth) + tail + "\n")
	}
}

func runAll(list []stmt, m *model) {
	for _, s := range list {
		s.run(m)
	}
}

func (g *gen) kind(k string) { g.kinds[k]++ }

func (g *gen) name(prefix string) string {
	g.fresh++
	return prefix + strconv.Itoa(g.fresh)
}

var keys = []string{"a", "b", "c", "é"}

// prologue declares every local; the model starts from the same values.
func (g *gen) prologue() {
	i0, i1 := int64(g.r.IntN(9)+1), int64(g.r.IntN(7)-3)
	g.locals = append(g.locals,
		fmt.Sprintf("let mut i0 = %d", i0),
		fmt.Sprintf("let mut i1 = %d", i1),
		"let mut i2 = base",
		"let mut w0 int8 = 120",
		"let mut u0 uint16 = 65530",
		`let mut t0 = "héllo"`,
		"let mut s0 = []int{3, 1, 4}",
		"let mut s1 = s0",
		"let mut g0 = [][]int{[]int{1, 2}, []int{3}}",
		`let mut m0 = Map[string, int]{"a": 1, "b": 2}`,
		"let mut m1 = m0",
		"let mut a0 [4]int",
		"let mut a1 = [4]int{1, 2, 3, 4}",
		"let mut p0 = Pair{a: 1, xs: []int{5}}",
		"let mut p1 = p0",
		"let mut sh0 Shape = Line{n: 2}",
	)
	g.kind("array-default")
	m := g.m
	m.ints["i0"], m.ints["i1"], m.ints["i2"] = i0, i1, m.base
	m.w, m.u, m.str = 120, 65530, "héllo"
	m.slices["s0"], m.slices["s1"] = []int64{3, 1, 4}, []int64{3, 1, 4}
	m.grid = [][]int64{{1, 2}, {3}}
	m.maps["m0"], m.maps["m1"] = map[string]int64{"a": 1, "b": 2}, map[string]int64{"a": 1, "b": 2}
	m.arrs["a0"], m.arrs["a1"] = [4]int64{}, [4]int64{1, 2, 3, 4}
	m.pairs["p0"], m.pairs["p1"] = pair{a: 1, xs: []int64{5}}, pair{a: 1, xs: []int64{5}}
	m.sh = shape{kind: "Line", n: 2}
	g.kind("package-let")
}

// epilogue writes every local once more, so none is a let mut that is
// never reassigned (S110), whatever the seed's statements wrote.
func (g *gen) epilogue() {
	add := func(text string, run func(m *model)) { g.body = append(g.body, stmt{line(text), run}) }
	for _, v := range intVars {
		add(v+" += 0", func(*model) {})
	}
	add("w0 += 0", func(*model) {})
	add("u0 += 0", func(*model) {})
	add(`t0 = t0 + ""`, func(*model) {})
	for _, s := range sliceVars {
		add(s+" = "+s+"[0:len("+s+")]", func(*model) {})
	}
	add("g0 = g0[0:len(g0)]", func(*model) {})
	for _, mv := range mapVars {
		add(mv+" = "+mv+`.Delete("zz")`, func(*model) {})
	}
	for _, a := range arrVars {
		add(a+"[0] += 0", func(*model) {})
	}
	for _, p := range pairVars {
		add(p+".a += 0", func(*model) {})
	}
	add("if i0 == -999999 {\n\tsh0 = Dot\n}", func(m *model) {
		if m.ints["i0"] == -999999 {
			m.sh = shape{kind: "Dot"}
		}
	})
}

// iexpr is an integer expression: its text and its model value. It reads
// the locals and calls pure helpers; it writes nothing.
type iexpr struct {
	text  string
	val   func(m *model) int64
	konst bool // a constant: the checker folds it, so it must not overflow or divide by zero
}

// live is e, or a local when e is a constant: an operand the checker
// must not fold.
func (g *gen) live(e iexpr) iexpr {
	if !e.konst {
		return e
	}
	v := g.pick(intVars)
	return iexpr{text: v, val: func(m *model) int64 { return m.ints[v] }}
}

func lit(v int64) iexpr {
	text := strconv.FormatInt(v, 10)
	if v < 0 {
		text = "(" + text + ")"
	}
	return iexpr{text: text, val: func(*model) int64 { return v }, konst: true}
}

func ie(text string, val func(m *model) int64) iexpr { return iexpr{text: text, val: val} }

var intVars = []string{"i0", "i1", "i2"}
var sliceVars = []string{"s0", "s1"}
var mapVars = []string{"m0", "m1"}
var arrVars = []string{"a0", "a1"}
var pairVars = []string{"p0", "p1"}

func (g *gen) pick(xs []string) string { return xs[g.r.IntN(len(xs))] }

// intExpr is an integer expression of depth at most d.
func (g *gen) intExpr(d int) iexpr {
	if d <= 0 || g.r.IntN(4) == 0 {
		if g.r.IntN(3) == 0 {
			return lit(int64(g.r.IntN(11) - 3))
		}
		v := g.pick(intVars)
		return ie(v, func(m *model) int64 { return m.ints[v] })
	}
	switch g.r.IntN(17) {
	case 0, 1, 2:
		ops := []string{"+", "-", "*"}
		op := ops[g.r.IntN(len(ops))]
		x, y := g.intExpr(d-1), g.intExpr(d-1)
		if x.konst {
			y = g.live(y)
		}
		return ie("(" + x.text + " " + op + " " + y.text + ")", func(m *model) int64 {
			a := x.val(m)
			b := y.val(m)
			switch op {
			case "+":
				return a + b
			case "-":
				return a - b
			}
			return a * b
		})
	case 3:
		// Division by a value that may be zero: a defect when it is.
		x, y := g.intExpr(d-1), g.live(g.intExpr(d-1))
		op := []string{"/", "%"}[g.r.IntN(2)]
		g.kind("defect-divide")
		return ie("(" + x.text + " " + op + " " + y.text + ")", func(m *model) int64 {
			a := x.val(m)
			b := y.val(m)
			if b == 0 {
				panic(defect{"integer divide by zero"})
			}
			if op == "/" {
				return a / b
			}
			return a % b
		})
	case 4:
		s := g.pick(sliceVars)
		return ie("len(" + s + ")", func(m *model) int64 { return int64(len(m.slices[s])) })
	case 5:
		s, i := g.pick(sliceVars), g.intExpr(d-1)
		return ie("at(" + s + ", " + i.text + ")", func(m *model) int64 { return at(m.slices[s], i.val(m)) })
	case 6:
		// A raw index: a defect past the end.
		s, k := g.pick(sliceVars), g.r.IntN(5)
		g.kind("defect-index")
		return ie(fmt.Sprintf("%s[%d]", s, k), func(m *model) int64 {
			xs := m.slices[s]
			if k >= len(xs) {
				panic(defect{"index out of range"})
			}
			return xs[k]
		})
	case 7:
		mv, k, dflt := g.pick(mapVars), keys[g.r.IntN(len(keys))], g.intExpr(d-1)
		g.kind("map-get-match")
		g.kind("generic-call")
		return ie(fmt.Sprintf("unbox(%s.Get(%s), %s)", mv, strconv.Quote(k), dflt.text), func(m *model) int64 {
			// Arguments are evaluated before the call, the default too.
			dv := dflt.val(m)
			if v, ok := m.maps[mv][k]; ok {
				return v
			}
			return dv
		})
	case 8:
		s, i, dflt := g.pick(sliceVars), g.intExpr(d-1), g.intExpr(d-1)
		g.kind("generic-call")
		return ie("pick(" + s + ", " + i.text + ", " + dflt.text + ")", func(m *model) int64 {
			xs := m.slices[s]
			iv := i.val(m)
			dv := dflt.val(m)
			if len(xs) == 0 {
				return dv
			}
			return xs[idx(int64(len(xs)), iv)]
		})
	case 9:
		a := g.pick(arrVars)
		return ie("sumArr(" + a + ")", func(m *model) int64 { return sumArr(m.arrs[a]) })
	case 10:
		return ie("runes(t0)", func(m *model) int64 { return runes(m.str) })
	case 11:
		g.kind("union-match")
		return ie("area(sh0)", func(m *model) int64 { return area(m.sh) })
	case 12:
		g.kind("conversion")
		if g.r.IntN(2) == 0 {
			return ie("int(w0)", func(m *model) int64 { return int64(m.w) })
		}
		return ie("int(u0)", func(m *model) int64 { return int64(m.u) })
	case 13:
		g.kind("package-let")
		k := g.r.IntN(3)
		return ie(fmt.Sprintf("table[%d]", k), func(m *model) int64 { return m.table()[k] })
	case 14:
		g.kind("recursion")
		i := g.intExpr(d - 1)
		return ie("fib(idx(12, " + i.text + "))", func(m *model) int64 { return fib(idx(12, i.val(m))) })
	case 15:
		mv := g.pick(mapVars)
		return ie("len(" + mv + ")", func(m *model) int64 { return int64(len(m.maps[mv])) })
	}
	p := g.pick(pairVars)
	return ie("(" + p + ".a + len(" + p + ".xs))", func(m *model) int64 { return m.pairs[p].a + int64(len(m.pairs[p].xs)) })
}

// stmt adds one statement of depth at most d to the body.
func (g *gen) stmt(d int) {
	g.body = append(g.body, g.makeStmt(d))
}

func (g *gen) makeStmt(d int) stmt {
	for {
		c := g.r.IntN(34)
		if d <= 0 && c >= 27 {
			continue
		}
		if s, ok := g.simple(c); ok {
			return s
		}
		if s, ok := g.compound(c, d); ok {
			return s
		}
	}
}

// simple is a statement with no body.
func (g *gen) simple(c int) (stmt, bool) {
	switch c {
	case 0:
		v, e := g.pick(intVars), g.intExpr(3)
		g.kind("int-assign")
		return stmt{line(v + " = " + e.text), func(m *model) { m.ints[v] = e.val(m) }}, true
	case 1:
		v, e := g.pick(intVars), g.intExpr(2)
		op := []string{"+=", "-=", "*="}[g.r.IntN(3)]
		g.kind("int-op-assign")
		return stmt{line(v + " " + op + " " + e.text), func(m *model) {
			x := e.val(m)
			switch op {
			case "+=":
				m.ints[v] += x
			case "-=":
				m.ints[v] -= x
			default:
				m.ints[v] *= x
			}
		}}, true
	case 2:
		e := g.live(g.intExpr(2))
		g.kind("narrow-wrap")
		g.kind("conversion")
		if g.r.IntN(2) == 0 {
			return stmt{line("w0 = w0 + int8(" + e.text + ")"), func(m *model) { m.w += int8(e.val(m)) }}, true
		}
		return stmt{line("u0 = u0 * uint16(" + e.text + ") + 7"), func(m *model) { m.u = m.u*uint16(e.val(m)) + 7 }}, true
	case 3:
		s, e := g.pick(sliceVars), g.intExpr(2)
		g.kind("slice-append-handover")
		return stmt{line(s + " = " + s + ".Append(" + e.text + ")"), func(m *model) {
			v := e.val(m)
			m.slices[s] = append(cloneInts(m.slices[s]), v)
		}}, true
	case 4:
		s, o, e := g.pick(sliceVars), g.pick(sliceVars), g.intExpr(2)
		g.kind("slice-append-other")
		return stmt{line(s + " = " + o + ".Append(" + e.text + ")"), func(m *model) {
			v := e.val(m)
			m.slices[s] = append(cloneInts(m.slices[o]), v)
		}}, true
	case 5:
		s, o := g.pick(sliceVars), g.pick(sliceVars)
		g.kind("slice-alias")
		return stmt{line(s + " = " + o), func(m *model) { m.slices[s] = cloneInts(m.slices[o]) }}, true
	case 6:
		s, i, e := g.pick(sliceVars), g.intExpr(2), g.intExpr(2)
		g.kind("slice-index-write")
		g.kind("if")
		return stmt{block("if len("+s+") > 0 {", []stmt{{line(s + "[idx(len(" + s + "), " + i.text + ")] = " + e.text), nil}}, "}"), func(m *model) {
			if len(m.slices[s]) > 0 {
				k := idx(int64(len(m.slices[s])), i.val(m))
				v := e.val(m)
				m.slices[s][k] = v
			}
		}}, true
	case 7:
		s, o := g.pick(sliceVars), g.pick(sliceVars)
		lo, hi := g.r.IntN(3), g.r.IntN(4)
		g.kind("slice-slice")
		if hi < lo {
			lo, hi = hi, lo
		}
		// Guarded to the length: a bound past it reads the Go target's
		// capacity, which no model predicts.
		g.kind("slice-bound")
		return stmt{block(fmt.Sprintf("if len(%s) >= %d {", o, hi), []stmt{{line(fmt.Sprintf("%s = %s[%d:%d]", s, o, lo, hi)), nil}}, "}"), func(m *model) {
			if len(m.slices[o]) >= hi {
				m.slices[s] = cloneInts(m.slices[o][lo:hi])
			}
		}}, true
	case 8:
		i, j, e := g.intExpr(1), g.intExpr(1), g.intExpr(2)
		g.kind("grid-nested-write")
		text := "if len(g0) > 0 && len(g0[idx(len(g0), " + i.text + ")]) > 0 {"
		inner := "g0[idx(len(g0), " + i.text + ")][idx(len(g0[idx(len(g0), " + i.text + ")]), " + j.text + ")] = " + e.text
		return stmt{block(text, []stmt{{line(inner), nil}}, "}"), func(m *model) {
			if len(m.grid) == 0 {
				return
			}
			r := idx(int64(len(m.grid)), i.val(m))
			if len(m.grid[r]) == 0 {
				return
			}
			r2 := idx(int64(len(m.grid)), i.val(m))
			c := idx(int64(len(m.grid[r2])), j.val(m))
			v := e.val(m)
			m.grid[r][c] = v
		}}, true
	case 9:
		s, i := g.pick(sliceVars), g.intExpr(1)
		g.kind("grid-row-alias")
		return stmt{block("if len(g0) > 0 {", []stmt{{line(s + " = g0[idx(len(g0), " + i.text + ")]"), nil}}, "}"), func(m *model) {
			if len(m.grid) > 0 {
				m.slices[s] = cloneInts(m.grid[idx(int64(len(m.grid)), i.val(m))])
			}
		}}, true
	case 10:
		s := g.pick(sliceVars)
		g.kind("grid-append")
		return stmt{line("g0 = g0.Append(" + s + ")"), func(m *model) { m.grid = append(cloneGrid(m.grid), cloneInts(m.slices[s])) }}, true
	case 11:
		mv, k, e := g.pick(mapVars), keys[g.r.IntN(len(keys))], g.intExpr(2)
		g.kind("map-write")
		return stmt{line(mv + "[" + strconv.Quote(k) + "] = " + e.text), func(m *model) { m.maps[mv][k] = e.val(m) }}, true
	case 12:
		mv, o, k := g.pick(mapVars), g.pick(mapVars), keys[g.r.IntN(len(keys))]
		g.kind("map-delete")
		return stmt{line(mv + " = " + o + ".Delete(" + strconv.Quote(k) + ")"), func(m *model) {
			n := cloneMap(m.maps[o])
			delete(n, k)
			m.maps[mv] = n
		}}, true
	case 13:
		mv, o, k, e := g.pick(mapVars), g.pick(mapVars), keys[g.r.IntN(len(keys))], g.intExpr(2)
		g.kind("map-put")
		return stmt{line(mv + " = " + o + ".Put(" + strconv.Quote(k) + ", " + e.text + ")"), func(m *model) {
			n := cloneMap(m.maps[o])
			n[k] = e.val(m)
			m.maps[mv] = n
		}}, true
	case 14:
		mv, o := g.pick(mapVars), g.pick(mapVars)
		g.kind("map-alias")
		return stmt{line(mv + " = " + o), func(m *model) { m.maps[mv] = cloneMap(m.maps[o]) }}, true
	case 15:
		a, i, e := g.pick(arrVars), g.intExpr(2), g.intExpr(2)
		g.kind("array-write")
		return stmt{line(a + "[idx(4, " + i.text + ")] = " + e.text), func(m *model) {
			k := idx(4, i.val(m))
			v := e.val(m)
			arr := m.arrs[a]
			arr[k] = v
			m.arrs[a] = arr
		}}, true
	case 16:
		a, o := g.pick(arrVars), g.pick(arrVars)
		g.kind("array-alias")
		return stmt{line(a + " = " + o), func(m *model) { m.arrs[a] = m.arrs[o] }}, true
	case 17:
		e := g.intExpr(1)
		g.kind("string-concat")
		g.kind("string-text")
		return stmt{line(`t0 = t0 + "\(` + e.text + `)"`), func(m *model) { m.str += strconv.FormatInt(e.val(m), 10) }}, true
	case 18:
		lo, hi := g.r.IntN(4), g.r.IntN(9)
		if hi < lo {
			lo, hi = hi, lo
		}
		g.kind("string-slice")
		return stmt{block(fmt.Sprintf("if len(t0) >= %d {", hi), []stmt{{line(fmt.Sprintf("t0 = t0[%d:%d] + \"é\"", lo, hi)), nil}}, "}"), func(m *model) {
			if len(m.str) >= hi {
				m.str = m.str[lo:hi] + "é"
			}
		}}, true
	case 19:
		p, e := g.pick(pairVars), g.intExpr(2)
		g.kind("record-field-write")
		return stmt{line(p + ".a = " + e.text), func(m *model) {
			v := e.val(m)
			x := m.pairs[p]
			x.a = v
			m.pairs[p] = x
		}}, true
	case 20:
		p, e := g.pick(pairVars), g.intExpr(2)
		g.kind("record-slice-write")
		if g.r.IntN(2) == 0 {
			return stmt{line(p + ".xs = " + p + ".xs.Append(" + e.text + ")"), func(m *model) {
				v := e.val(m)
				x := clonePair(m.pairs[p])
				x.xs = append(x.xs, v)
				m.pairs[p] = x
			}}, true
		}
		i := g.intExpr(1)
		return stmt{block("if len("+p+".xs) > 0 {", []stmt{{line(p + ".xs[idx(len(" + p + ".xs), " + i.text + ")] = " + e.text), nil}}, "}"), func(m *model) {
			x := m.pairs[p]
			if len(x.xs) > 0 {
				k := idx(int64(len(x.xs)), i.val(m))
				v := e.val(m)
				x.xs[k] = v
			}
		}}, true
	case 21:
		p, o := g.pick(pairVars), g.pick(pairVars)
		g.kind("record-alias")
		if g.r.IntN(2) == 0 {
			s := g.pick(sliceVars)
			return stmt{line(p + " = Pair{a: " + o + ".a, xs: " + s + "}"), func(m *model) {
				m.pairs[p] = pair{a: m.pairs[o].a, xs: cloneInts(m.slices[s])}
			}}, true
		}
		return stmt{line(p + " = " + o), func(m *model) { m.pairs[p] = clonePair(m.pairs[o]) }}, true
	case 22:
		g.kind("union-assign")
		switch g.r.IntN(3) {
		case 0:
			return stmt{line("sh0 = Dot"), func(m *model) { m.sh = shape{kind: "Dot"} }}, true
		case 1:
			e := g.intExpr(2)
			return stmt{line("sh0 = Line{n: " + e.text + "}"), func(m *model) { m.sh = shape{kind: "Line", n: e.val(m)} }}, true
		}
		s := g.pick(sliceVars)
		return stmt{line("sh0 = Grid{cells: " + s + "}"), func(m *model) { m.sh = shape{kind: "Grid", cells: cloneInts(m.slices[s])} }}, true
	case 23:
		v, w, e := g.pick(intVars), g.pick(intVars), g.intExpr(1)
		g.kind("literal-value")
		g.kind("literal-capture")
		// The literal writes w, which it captures; e reads no local the
		// literal writes before it is evaluated as the argument.
		return stmt{line(fmt.Sprintf("%s = func(x int) int {\n\t%s = %s + x\n\t%s * 2\n}(%s)", v, w, w, w, e.text)), func(m *model) {
			x := e.val(m)
			m.ints[w] += x
			m.ints[v] = m.ints[w] * 2
		}}, true
	case 24:
		s, e := g.pick(sliceVars), g.intExpr(1)
		g.kind("literal-capture")
		return stmt{block("func() {", []stmt{{line(s + " = " + s + ".Append(" + e.text + ")"), nil}, {line("i0 += 1"), nil}}, "}()"), func(m *model) {
			v := e.val(m)
			m.slices[s] = append(cloneInts(m.slices[s]), v)
			m.ints["i0"]++
		}}, true
	case 25:
		g.kind("float-text")
		g.kind("string-text")
		return stmt{line(`t0 = "\(float64(i1) / 4.0)"`), func(m *model) { m.str = strconv.FormatFloat(float64(m.ints["i1"])/4.0, 'g', -1, 64) }}, true
	case 26:
		mv := g.pick(mapVars)
		g.kind("map-get-match")
		k := keys[g.r.IntN(len(keys))]
		v := g.pick(intVars)
		text := fmt.Sprintf("match %s.Get(%s) {\ncase Some{Value: got}:\n\t%s = got\ncase None:\n\t%s = -%s\n}", mv, strconv.Quote(k), v, v, v)
		return stmt{line(text), func(m *model) {
			if got, ok := m.maps[mv][k]; ok {
				m.ints[v] = got
			} else {
				m.ints[v] = -m.ints[v]
			}
		}}, true
	}
	return stmt{}, false
}

// compound is a statement with a body of statements of depth d-1.
func (g *gen) compound(c, d int) (stmt, bool) {
	inner := func() []stmt {
		n := 1 + g.r.IntN(3)
		out := make([]stmt, n)
		for i := range out {
			out[i] = g.makeStmt(d - 1)
		}
		return out
	}
	switch c {
	case 27:
		cond := g.intExpr(2)
		g.kind("if")
		then, els := inner(), inner()
		return stmt{func(b *strings.Builder, depth int) {
			block("if "+cond.text+" > 2 {", then, "} else {")(b, depth)
			for _, s := range els {
				s.write(b, depth+1)
			}
			b.WriteString(strings.Repeat("\t", depth) + "}\n")
		}, func(m *model) {
			if cond.val(m) > 2 {
				runAll(then, m)
			} else {
				runAll(els, m)
			}
		}}, true
	case 28:
		// A for over a slice: the domain is its value at the start, and
		// the body may write the slice.
		s, x := g.pick(sliceVars), g.name("lx")
		g.kind("for-slice")
		g.loops++
		body := inner()
		g.loops--
		esc := g.escape(x)
		return stmt{func(b *strings.Builder, depth int) {
			block("for "+x+" in "+s+" {", append([]stmt{esc.stmt}, body...), "}")(b, depth)
		}, func(m *model) {
			dom := cloneInts(m.slices[s])
			for _, v := range dom {
				m.ints[x] = v
				brk, cont := esc.run(m)
				if brk {
					return
				}
				if !cont {
					runAllLoop(body, m)
				}
			}
		}}, true
	case 29:
		a, i, x := g.pick(arrVars), g.name("li"), g.name("lx")
		g.kind("for-array")
		g.loops++
		body := inner()
		g.loops--
		return stmt{func(b *strings.Builder, depth int) {
			block("for "+i+", "+x+" in "+a+" {", append([]stmt{{line("i2 += " + i + " * " + x), nil}}, body...), "}")(b, depth)
		}, func(m *model) {
			dom := m.arrs[a]
			for k, v := range dom {
				m.ints["i2"] += int64(k) * v
				runAllLoop(body, m)
			}
		}}, true
	case 30:
		c := g.name("lc")
		g.kind("for-string")
		return stmt{block("for "+c+" in t0 {", []stmt{{line("i1 = i1 * 3 + int(" + c + ") % 11"), nil}}, "}"), func(m *model) {
			for _, r := range m.str {
				m.ints["i1"] = m.ints["i1"]*3 + int64(r)%11
			}
		}}, true
	case 31:
		i, n := g.name("li"), g.intExpr(1)
		g.kind("for-range")
		g.loops++
		body := inner()
		g.loops--
		return stmt{func(b *strings.Builder, depth int) {
			block("for "+i+" in 0..idx(5, "+n.text+") {", append([]stmt{{line("i0 += " + i), nil}}, body...), "}")(b, depth)
		}, func(m *model) {
			end := idx(5, n.val(m))
			for k := int64(0); k < end; k++ {
				m.ints["i0"] += k
				runAllLoop(body, m)
			}
		}}, true
	case 32:
		k := g.name("lk")
		g.kind("for-cond")
		g.loops++
		body := inner()
		g.loops--
		return stmt{func(b *strings.Builder, depth int) {
			b.WriteString(strings.Repeat("\t", depth) + "let mut " + k + " = 0\n")
			block("for "+k+" < 3 {", append([]stmt{{line(k + " += 1"), nil}}, body...), "}")(b, depth)
		}, func(m *model) {
			for n := 0; n < 3; n++ {
				runAllLoop(body, m)
			}
		}}, true
	case 33:
		g.kind("union-match")
		v, e := g.pick(intVars), g.intExpr(1)
		text := fmt.Sprintf("match sh0 {\ncase Dot:\n\t%s += 1\ncase Line{n: k}:\n\t%s = %s + k\ncase Grid{cells: cs}:\n\tsh0 = Line{n: len(cs) + %s}\n}", v, v, v, e.text)
		return stmt{line(text), func(m *model) {
			switch m.sh.kind {
			case "Dot":
				m.ints[v]++
			case "Line":
				m.ints[v] += m.sh.n
			default:
				m.sh = shape{kind: "Line", n: int64(len(m.sh.cells)) + e.val(m)}
			}
		}}, true
	}
	return stmt{}, false
}

// escaper is a loop's first statement: break or continue on a condition
// of the element x, or a use of it. run reports a break or a continue.
type escaper struct {
	stmt stmt
	run  func(m *model) (brk, cont bool)
}

func (g *gen) escape(x string) escaper {
	switch g.r.IntN(3) {
	case 0:
		g.kind("break")
		return escaper{stmt{block("if "+x+" > 4 {", []stmt{{line("break"), nil}}, "}"), nil}, func(m *model) (bool, bool) { return m.ints[x] > 4, false }}
	case 1:
		g.kind("continue")
		return escaper{stmt{block("if "+x+" % 2 == 0 {", []stmt{{line("continue"), nil}}, "}"), nil}, func(m *model) (bool, bool) { return false, m.ints[x]%2 == 0 }}
	}
	return escaper{stmt{line("i1 += " + x), nil}, func(m *model) (bool, bool) { m.ints["i1"] += m.ints[x]; return false, false }}
}

// runAllLoop runs a loop body: its statements hold no break or continue
// of the loop (only an escaper does).
func runAllLoop(body []stmt, m *model) { runAll(body, m) }
