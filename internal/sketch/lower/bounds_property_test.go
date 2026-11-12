package lower

import (
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The bounds facts' seeded property (BCE1; bounds.go). A generator writes
// functions over a string s, a slice xs and two ints a and b: an if, or a
// for, whose condition is one to three comparisons (the forms bounds.go
// reads, and forms it must not read: v+k <= len(S), which wraps), and in
// its body index and slice sites, writes of a, lets of masked or summed
// values and nested ifs. A reference model in Go (boundsFn.run) runs each
// function over every state of a grid: lengths of s and xs, and values of
// a and b that include the int range's ends, where a sum wraps.
//
// TestBoundsFactsSound reads, from the lowered Go, which sites the
// lowering made unchecked (v1.ByteAt, StringAt, StringN, AddrAt), and
// fails when a state the model runs reaches an unchecked site out of
// range: a fact the guard did not prove. It also fails when a site of the
// covered forms (a guard 0 <= a && a <= len(s)-C, or uint(a+k) <
// uint(len(s)), and a site inside its margin before any write) stays
// checked. It lowers in process. TestBoundsFactsAgree builds both
// lowerings, the default and factLowering "checked" (the reference), and
// runs each function over states in which the model reaches every site in
// range, then over one state that reaches a site out of range: both
// builds print the model's results, and both stop at that site with Go's
// bounds failure, since a site a wrong guard reaches is never unchecked.
//
// Replay: go test -run '^TestBoundsFacts' ./internal/sketch/lower -args -bounds.seed=N

var boundsSeed = flag.Uint64("bounds.seed", 0, "run TestBoundsFactsSound and TestBoundsFactsAgree on this seed alone")

var boundsSeeds = []uint64{1, 2, 3, 4, 5, 6, 7, 8}

const boundsCases = 12 // functions per seed

// bx is a generated expression: an int (var, const, len, +, -, &) or a
// condition (cmp, ucmp, and).
type bx struct {
	op   string // var const len add sub and cmp ucmp land
	cmp  string // < <= > >=
	name string // var: its name; len: s or xs
	c    int64
	x, y *bx
}

func (e *bx) String() string {
	switch e.op {
	case "var":
		return e.name
	case "const":
		return fmt.Sprint(e.c)
	case "len":
		return "len(" + e.name + ")"
	case "add":
		return "(" + e.x.String() + " + " + e.y.String() + ")"
	case "sub":
		return "(" + e.x.String() + " - " + e.y.String() + ")"
	case "and":
		return "(" + e.x.String() + " & " + e.y.String() + ")"
	case "cmp":
		return e.x.String() + " " + e.cmp + " " + e.y.String()
	case "ucmp":
		return "uint(" + e.x.String() + ") " + e.cmp + " uint(" + e.y.String() + ")"
	case "land":
		return e.x.String() + " && " + e.y.String()
	}
	panic(e.op)
}

// bstate is one state of the model: the locals' values and the lengths.
type bstate struct {
	env       map[string]int64
	ls, lxs   int64
	acc       int64
	stopped   bool // a checked site out of range: the program stops
	stopSite  int
	badSite   int // an unchecked site out of range (-1: none)
	reached   map[int]bool
	unchecked map[int]bool
}

func (e *bx) eval(st *bstate) int64 {
	switch e.op {
	case "var":
		return st.env[e.name]
	case "const":
		return e.c
	case "len":
		if e.name == "s" {
			return st.ls
		}
		return st.lxs
	case "add":
		return e.x.eval(st) + e.y.eval(st)
	case "sub":
		return e.x.eval(st) - e.y.eval(st)
	case "and":
		return e.x.eval(st) & e.y.eval(st)
	}
	panic(e.op)
}

func (e *bx) holds(st *bstate) bool {
	switch e.op {
	case "land":
		return e.x.holds(st) && e.y.holds(st)
	case "cmp", "ucmp":
		x, y := e.x.eval(st), e.y.eval(st)
		var lt, eq bool
		if e.op == "ucmp" {
			lt, eq = uint64(x) < uint64(y), x == y
		} else {
			lt, eq = x < y, x == y
		}
		switch e.cmp {
		case "<":
			return lt
		case "<=":
			return lt || eq
		case ">":
			return !lt && !eq
		case ">=":
			return !lt
		}
	}
	panic(e.op)
}

// bitem is one statement of a body: a site, a write of a, a let, or an if.
type bitem struct {
	kind   string // index xsindex slice write let if
	site   int    // index, xsindex, slice: the site's number
	i      *bx    // index; the let's value; the write's value
	lo, hi *bx    // slice (nil: absent)
	name   string // let: its name
	cond   *bx    // if
	body   []bitem
	must   bool // a covered form: the lowering must make it unchecked
}

// boundsFn is one generated function: its guard and body, as an if or a
// for whose body ends with a = a + step, the for inside an if (outer) or
// not.
type boundsFn struct {
	name   string
	outer  *bx
	loop   bool
	step   int64
	cond   *bx
	body   []bitem
	sites  int
	writes bool
}

func (f *boundsFn) source() string {
	var b strings.Builder
	kw := "let"
	if f.writes || f.loop {
		kw = "let mut"
	}
	fmt.Fprintf(&b, "func %s(s string, xs []int, a0 int, b int) int {\n\t%s a = a0\n\tlet mut acc = 0\n", f.name, kw)
	head, ind := "if", "\t"
	if f.loop {
		head = "for"
	}
	if f.outer != nil {
		fmt.Fprintf(&b, "\tif %s {\n", f.outer)
		ind = "\t\t"
	}
	fmt.Fprintf(&b, "%s%s %s {\n", ind, head, f.cond)
	writeItems(&b, f.body, ind+"\t")
	if f.loop {
		fmt.Fprintf(&b, "%s\ta = a + %d\n", ind, f.step)
	}
	b.WriteString(ind + "}\n")
	if f.outer != nil {
		b.WriteString("\t}\n")
	}
	b.WriteString("\tacc = acc + (a & 1) + (b & 2)\n\tacc\n}\n")
	return b.String()
}

func writeItems(b *strings.Builder, items []bitem, ind string) {
	for _, it := range items {
		switch it.kind {
		case "index":
			fmt.Fprintf(b, "%slet r%d = s[%s]\n%sacc = acc * 31 + int(r%d)\n", ind, it.site, it.i, ind, it.site)
		case "xsindex":
			fmt.Fprintf(b, "%slet r%d = xs[%s]\n%sacc = acc * 31 + r%d\n", ind, it.site, it.i, ind, it.site)
		case "slice":
			lo, hi := "", ""
			if it.lo != nil {
				lo = it.lo.String()
			}
			if it.hi != nil {
				hi = it.hi.String()
			}
			fmt.Fprintf(b, "%slet r%d = s[%s : %s]\n%sacc = acc * 31 + len(r%d)\n", ind, it.site, lo, hi, ind, it.site)
			fmt.Fprintf(b, "%sif len(r%d) > 0 {\n%s\tacc = acc + int(r%d[0])\n%s}\n", ind, it.site, ind, it.site, ind)
		case "write":
			fmt.Fprintf(b, "%sa = %s\n", ind, it.i)
		case "let":
			fmt.Fprintf(b, "%slet %s = %s\n%sacc = acc + %s\n", ind, it.name, it.i, ind, it.name)
		case "if":
			fmt.Fprintf(b, "%sif %s {\n", ind, it.cond)
			writeItems(b, it.body, ind+"\t")
			fmt.Fprintf(b, "%s}\n", ind)
		}
	}
}

// sByte is s's byte at i in every program: 'a' + i%26.
func sByte(i int64) int64 { return 'a' + i%26 }

// xsAt is xs's element at i: 7i+1.
func xsAt(i int64) int64 { return 7*i + 1 }

// run is the model: the function on one state.
func (f *boundsFn) run(st *bstate) {
	st.env["a"] = st.env["a0"]
	st.acc = 0
	st.badSite = -1
	f.body1(st)
	st.acc += st.env["a"]&1 + st.env["b"]&2
}

// body1 is the function's if or for.
func (f *boundsFn) body1(st *bstate) {
	if f.outer != nil && !f.outer.holds(st) {
		return
	}
	if !f.loop {
		if f.cond.holds(st) {
			f.items(st, f.body)
		}
		return
	}
	for steps := 0; f.cond.holds(st); steps++ {
		if steps > 1000 {
			panic("a generated loop that does not end: a generator defect")
		}
		if f.items(st, f.body); st.stopped {
			return
		}
		st.env["a"] += f.step
	}
}

// items runs a body; it returns at a stop.
func (f *boundsFn) items(st *bstate, items []bitem) {
	for _, it := range items {
		if st.stopped {
			return
		}
		switch it.kind {
		case "index", "xsindex":
			i, n := it.i.eval(st), st.ls
			if it.kind == "xsindex" {
				n = st.lxs
			}
			if !f.reach(st, it, 0 <= i && i < n) {
				return
			}
			if it.kind == "index" {
				st.acc = st.acc*31 + sByte(i)
			} else {
				st.acc = st.acc*31 + xsAt(i)
			}
		case "slice":
			lo, hi := int64(0), st.ls
			if it.lo != nil {
				lo = it.lo.eval(st)
			}
			if it.hi != nil {
				hi = it.hi.eval(st)
			}
			if !f.reach(st, it, 0 <= lo && lo <= hi && hi <= st.ls) {
				return
			}
			st.acc = st.acc*31 + (hi - lo)
			if hi > lo {
				st.acc += sByte(lo)
			}
		case "write":
			st.env["a"] = it.i.eval(st)
		case "let":
			st.env[it.name] = it.i.eval(st)
			st.acc += st.env[it.name]
		case "if":
			if it.cond.holds(st) {
				f.items(st, it.body)
			}
		}
	}
}

// reach records a site reached; out of range, a checked site stops the
// program and an unchecked one is the property's failure.
func (f *boundsFn) reach(st *bstate, it bitem, in bool) bool {
	st.reached[it.site] = true
	if in {
		return true
	}
	if st.unchecked[it.site] && st.badSite < 0 {
		st.badSite = it.site
	}
	st.stopped, st.stopSite = true, it.site
	return false
}

// boundsGen writes functions.
type boundsGen struct {
	r     *rand.Rand
	f     *boundsFn
	pool  []string // the int locals a site may read
	lets  int
	depth int
}

func bvar(n string) *bx               { return &bx{op: "var", name: n} }
func bconst(c int64) *bx              { return &bx{op: "const", c: c} }
func blen(n string) *bx               { return &bx{op: "len", name: n} }
func badd(x, y *bx) *bx               { return &bx{op: "add", x: x, y: y} }
func bsub(x, y *bx) *bx               { return &bx{op: "sub", x: x, y: y} }
func bcmp(x *bx, c string, y *bx) *bx { return &bx{op: "cmp", cmp: c, x: x, y: y} }
func bucmp(x *bx, c string, y *bx) *bx {
	return &bx{op: "ucmp", cmp: c, x: x, y: y}
}
func band(x, y *bx) *bx { return &bx{op: "land", x: x, y: y} }

// plusK is v+k, v-k, or v.
func plusK(v *bx, k int64) *bx {
	switch {
	case k > 0:
		return badd(v, bconst(k))
	case k < 0:
		return bsub(v, bconst(-k))
	}
	return v
}

func (g *boundsGen) k(lo, hi int) int64 { return int64(lo + g.r.IntN(hi-lo+1)) }

// bigK is a constant near boundLimit, now and then.
func (g *boundsGen) bigK() int64 {
	return []int64{1 << 30, 1<<30 + 1, 1<<30 - 1, 1 << 31}[g.r.IntN(4)]
}

func (g *boundsGen) pick() *bx { return bvar(g.pool[g.r.IntN(len(g.pool))]) }

// sub picks s or xs.
func (g *boundsGen) seq() string {
	if g.r.IntN(3) == 0 {
		return "xs"
	}
	return "s"
}

// index is an int expression a site reads.
func (g *boundsGen) index() *bx {
	switch g.r.IntN(10) {
	case 0, 1, 2:
		return g.pick()
	case 3, 4, 5:
		return plusK(g.pick(), g.k(-2, 12))
	case 6:
		return bsub(blen("s"), bconst(g.k(0, 4)))
	case 7:
		return &bx{op: "and", x: g.pick(), y: bconst([]int64{3, 7, 15}[g.r.IntN(3)])}
	case 8:
		return badd(bvar("a"), bvar("b"))
	}
	return bconst(g.k(0, 12))
}

// atom is one comparison over v.
func (g *boundsGen) atom(v *bx) *bx {
	S := blen(g.seq())
	switch g.r.IntN(13) {
	case 0:
		return bcmp(bconst(0), "<=", v)
	case 1:
		return bcmp(v, ">=", bconst(g.k(-1, 2)))
	case 2:
		return bcmp(v, "<=", bsub(S, bconst(g.k(0, 16))))
	case 3:
		return bcmp(v, "<", bsub(S, bconst(g.k(0, 8))))
	case 4:
		return bcmp(bsub(S, bconst(g.k(0, 8))), ">=", v)
	case 5:
		return bcmp(S, ">", v)
	case 6, 7:
		// v+k <= len(S): wraps near the largest int, so it proves nothing.
		return bcmp(plusK(v, g.k(1, 16)), []string{"<=", "<"}[g.r.IntN(2)], S)
	case 8:
		return bucmp(plusK(v, g.k(-2, 8)), "<", S)
	case 9:
		return bucmp(v, []string{"<", "<="}[g.r.IntN(2)], bconst(g.k(0, 20)))
	case 10:
		c := g.k(0, 20)
		if g.r.IntN(4) == 0 {
			c = g.bigK()
		}
		return bcmp(v, []string{"<", "<="}[g.r.IntN(2)], bconst(c))
	case 11:
		return bcmp(plusK(v, g.k(-3, 8)), "<", bconst(g.k(0, 20)))
	}
	return bcmp(bconst(g.k(-2, 3)), "<", v)
}

// guard is one to three atoms over a and b.
func (g *boundsGen) guard() *bx {
	vars := []*bx{bvar("a"), bvar("b")}
	c := g.atom(vars[g.r.IntN(2)])
	for range g.r.IntN(3) {
		c = band(c, g.atom(vars[g.r.IntN(2)]))
	}
	return c
}

// site is an index or a slice of s or xs.
func (g *boundsGen) site() bitem {
	g.f.sites++
	n := g.f.sites
	switch g.r.IntN(4) {
	case 0:
		return bitem{kind: "xsindex", site: n, i: g.index()}
	case 1:
		return bitem{kind: "index", site: n, i: g.index()}
	}
	it := bitem{kind: "slice", site: n}
	switch g.r.IntN(5) {
	case 0:
		it.hi = g.index()
	case 1:
		it.lo = g.index()
	case 2, 3:
		// s[e+k1 : e+k2]: one sum, constants in order (a cut of a constant
		// length when k1 < k2).
		e, k1 := g.pick(), g.k(-1, 8)
		it.lo, it.hi = plusK(e, k1), plusK(e, k1+g.k(0, 8))
	default:
		it.lo, it.hi = g.index(), g.index()
		if it.lo.op == "const" && it.hi.op == "const" && it.lo.c > it.hi.c {
			// The checker refuses constant bounds out of order.
			it.lo, it.hi = it.hi, it.lo
		}
	}
	return it
}

// items is a body of 1 to 5 statements.
func (g *boundsGen) items() []bitem {
	var out []bitem
	saved := len(g.pool)
	for range 1 + g.r.IntN(5) {
		switch x := g.r.IntN(10); {
		case x < 5:
			out = append(out, g.site())
		case x < 7 && !g.f.loop: // a write in a loop could keep it from ending
			g.f.writes = true
			v := plusK(bvar("a"), g.k(-2, 5))
			if g.r.IntN(3) == 0 {
				v = bvar("b")
			}
			out = append(out, bitem{kind: "write", i: v})
		case x < 9:
			g.lets++
			name := fmt.Sprintf("m%d", g.lets)
			out = append(out, bitem{kind: "let", name: name, i: g.index()})
			g.pool = append(g.pool, name)
		default:
			if g.depth >= 2 {
				break
			}
			g.depth++
			if g.r.IntN(2) == 0 {
				out = append(out, bitem{kind: "if", cond: g.guard(), body: g.items()})
			} else {
				// An if whose facts end with it: a site after it in the
				// margin it proves must stay checked.
				c := g.k(1, 8)
				b := bvar("b")
				cond := band(bcmp(bconst(0), "<=", b), bcmp(b, "<=", bsub(blen("s"), bconst(c))))
				out = append(out, bitem{kind: "if", cond: cond, body: g.items()})
				g.f.sites++
				out = append(out, bitem{kind: "index", site: g.f.sites, i: plusK(b, g.k(0, int(c)-1))})
			}
			g.depth--
		}
	}
	g.pool = g.pool[:saved]
	return out
}

// covered is a guard of a covered form and a site it proves, first in the
// body: the lowering must make the site unchecked.
func (g *boundsGen) covered() (*bx, bitem) {
	a := bvar("a")
	g.f.sites++
	it := bitem{site: g.f.sites, must: true}
	if g.r.IntN(3) == 0 {
		// k >= -1: uint(a-2) < uint(len(s)) gives a <= len(s)+1, a bound
		// bounds.go does not keep.
		k := g.k(-1, 8)
		it.kind, it.i = "index", plusK(a, k)
		return bucmp(plusK(a, k), "<", blen("s")), it
	}
	c := g.k(1, 16)
	cond := band(bcmp(bconst(0), "<=", a), bcmp(a, "<=", bsub(blen("s"), bconst(c))))
	switch g.r.IntN(4) {
	case 0:
		// A decoy: a+c <= len(s) wraps for a near the largest int, so it
		// proves no site; the site must stay checked.
		it.must = false
		cond = band(bcmp(bconst(0), "<=", a), bcmp(plusK(a, c), []string{"<=", "<"}[g.r.IntN(2)], blen("s")))
		it.kind, it.i = "index", plusK(a, g.k(0, int(c)-1))
		return cond, it
	case 1:
		// A decoy one past the margin: s[a+c] under a <= len(s)-c.
		it.must = false
		it.kind, it.i = "index", plusK(a, c)
		return cond, it
	}
	switch g.r.IntN(3) {
	case 0:
		it.kind, it.i = "index", plusK(a, g.k(0, int(c)-1))
	case 1:
		cond = band(bcmp(bconst(0), "<=", a), bcmp(a, "<=", bsub(blen("xs"), bconst(c))))
		it.kind, it.i = "xsindex", plusK(a, g.k(0, int(c)-1))
	default:
		k1 := g.k(0, int(c))
		it.kind, it.lo, it.hi = "slice", plusK(a, k1), plusK(a, g.k(int(k1), int(c)))
		switch g.r.IntN(4) {
		case 0:
			it.hi = nil // s[a+k1:], to the end
		case 1:
			it.lo = nil // s[:a+k2]
		}
		if g.r.IntN(3) == 0 && it.lo != nil && it.hi != nil && it.lo.String() != it.hi.String() {
			// A decoy: both bounds in range, out of order.
			it.lo, it.hi, it.must = it.hi, it.lo, false
		}
	}
	return cond, it
}

func (g *boundsGen) fn(name string) *boundsFn {
	g.f = &boundsFn{name: name}
	g.pool = []string{"a", "b"}
	f := g.f
	if g.r.IntN(4) == 0 {
		// A loop: its first conjunct bounds a in a uint compare, so a
		// grows out of it and the loop ends.
		f.loop, f.step = true, g.k(1, 3)
		f.cond = bucmp(bvar("a"), "<", []*bx{blen("s"), bconst(g.k(0, 20))}[g.r.IntN(2)])
		if g.r.IntN(2) == 0 {
			f.cond = band(f.cond, g.atom(bvar([]string{"a", "b"}[g.r.IntN(2)])))
		}
		f.body = g.items()
		if g.r.IntN(2) == 0 {
			// Inside an if whose facts over a the loop's writes end: a
			// site in the margin the if proves holds on the first
			// iteration only.
			c := g.k(1, 8)
			f.outer = band(bcmp(bconst(0), "<=", bvar("a")), bcmp(bvar("a"), "<=", bsub(blen("s"), bconst(c))))
			g.f.sites++
			f.body = append([]bitem{{kind: "index", site: g.f.sites, i: plusK(bvar("a"), g.k(0, int(c)-1))}}, f.body...)
		}
		return f
	}
	if g.r.IntN(2) == 0 {
		cond, it := g.covered()
		if g.r.IntN(2) == 0 {
			cond = band(cond, g.atom(bvar("b")))
		}
		f.cond = cond
		f.body = []bitem{it}
		if g.r.IntN(2) == 0 {
			// A write of a ends the guard's facts: a site after it in the
			// old margin must stay checked.
			f.writes = true
			g.f.sites++
			f.body = append(f.body, bitem{kind: "write", i: plusK(bvar("a"), g.k(1, 8))},
				bitem{kind: "index", site: g.f.sites, i: plusK(bvar("a"), g.k(0, 1))})
		}
		f.body = append(f.body, g.items()...)
		return f
	}
	f.cond = g.guard()
	f.body = g.items()
	return f
}

// boundsCase is one seed's functions in one program.
type boundsCase struct {
	seed uint64
	fns  []*boundsFn
}

func boundsProgram(seed uint64) *boundsCase {
	g := &boundsGen{r: rand.New(rand.NewPCG(seed, 0x6263653))}
	c := &boundsCase{seed: seed}
	for i := range boundsCases {
		c.fns = append(c.fns, g.fn(fmt.Sprintf("f%d", i)))
	}
	return c
}

// source is the program: the functions, and a main that calls each on
// the given states (nil: none).
func (c *boundsCase) source(calls []string) string {
	var b strings.Builder
	for _, f := range c.fns {
		b.WriteString(f.source())
		b.WriteString("\n")
	}
	b.WriteString("func main() needs Console {\n")
	for _, call := range calls {
		fmt.Fprintf(&b, "\tConsole.Println(\"\\(%s)\")\n", call)
	}
	b.WriteString("}\n")
	return b.String()
}

// boundsInts are the values of a and b: small ones around the lengths,
// and the int range's ends, where a sum wraps.
var boundsInts = []int64{-3, -1, 0, 1, 2, 3, 4, 5, 7, 8, 9, 12, 15, 16, 17, 19, 20, 21, 1 << 30,
	math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 - 7, math.MaxInt64 - 15, math.MinInt64, math.MinInt64 + 1}

var boundsLens = []int64{0, 1, 2, 3, 5, 8, 13, 17, 20}

// eachState calls fn on every state of the grid.
func eachState(fn func(ls, lxs, a, b int64)) {
	for _, ls := range boundsLens {
		for _, lxs := range []int64{0, 3, 8, 20} {
			for _, a := range boundsInts {
				for _, b := range boundsInts {
					fn(ls, lxs, a, b)
				}
			}
		}
	}
}

// uncheckedSites reads, from the lowered Go, the sites written through an
// unchecked helper: the let of rN whose value is a v1 call.
func uncheckedSites(goSrc string) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	fnRe := regexp.MustCompile(`^func (f\d+)\(`)
	siteRe := regexp.MustCompile(`\br(\d+)\b[^=]*:?= .*\.(ByteAt|StringAt|StringN|AddrAt)\(`)
	cur := ""
	for _, line := range strings.Split(goSrc, "\n") {
		if m := fnRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			out[cur] = map[int]bool{}
			continue
		}
		if m := siteRe.FindStringSubmatch(line); m != nil && cur != "" {
			var n int
			fmt.Sscan(m[1], &n)
			out[cur][n] = true
		}
	}
	return out
}

func boundsSeedList() []uint64 {
	if *boundsSeed != 0 {
		return []uint64{*boundsSeed}
	}
	return boundsSeeds
}

func boundsReplay(seed uint64) string {
	return fmt.Sprintf("go test -run '^TestBoundsFacts' ./internal/sketch/lower -args -bounds.seed=%d", seed)
}

// TestBoundsFactsSound is the in-process half of the property (above): no
// state reaches an unchecked site out of range, and every covered site is
// unchecked. It catches the mutants bounds-wrap-taken and
// bounds-index-margin-widened of the ledger.
func TestBoundsFactsSound(t *testing.T) {
	if factLowering == "checked" {
		t.Skip("the reference lowering writes no unchecked site")
	}
	helpers := map[string]bool{}
	var programs []*lowered
	sites, fast := 0, 0
	defer func() { t.Logf("%d sites, %d unchecked", sites, fast) }()
	for _, seed := range boundsSeedList() {
		c := boundsProgram(seed)
		src := c.source(nil)
		p := mustLower(t, fmt.Sprintf("bounds%d", seed), src)
		programs = append(programs, p)
		goSrc := p.mainGo()
		for _, h := range []string{"ByteAt", "StringAt", "StringN", "AddrAt"} {
			if strings.Contains(goSrc, "."+h+"(") {
				helpers[h] = true
			}
		}
		unchecked := uncheckedSites(goSrc)
		for _, f := range c.fns {
			un := unchecked[f.name]
			sites += f.sites
			fast += len(un)
			for _, it := range f.body {
				if it.must && !un[it.site] {
					t.Errorf("seed %d, %s: site r%d is of a covered form but stays checked\nReplay: %s\n%s\nGo:\n%s", seed, f.name, it.site, boundsReplay(seed), f.source(), goFunc(t, goSrc, f.name))
				}
			}
			failed := false
			eachState(func(ls, lxs, a, b int64) {
				if failed {
					return
				}
				st := &bstate{env: map[string]int64{"a0": a, "b": b}, ls: ls, lxs: lxs, reached: map[int]bool{}, unchecked: un}
				f.run(st)
				if st.badSite >= 0 {
					failed = true
					t.Errorf("seed %d, %s: the state len(s)=%d len(xs)=%d a0=%d b=%d passes the guard and reaches the unchecked site r%d out of range\nReplay: %s\n%s\nGo:\n%s", seed, f.name, ls, lxs, a, b, st.badSite, boundsReplay(seed), f.source(), goFunc(t, goSrc, f.name))
				}
			})
		}
	}
	if *boundsSeed == 0 {
		for _, h := range []string{"ByteAt", "StringAt", "StringN", "AddrAt"} {
			if !helpers[h] {
				t.Errorf("the fixed seeds never write v1.%s", h)
			}
		}
	}
	typecheckGo(t, programs)
}

// TestBoundsFactsAgree is the run half of the property (above). It builds
// Go because the unchecked helpers' results (the bytes and lengths they
// read) and Go's bounds failure under a wrong guard are only seen when the
// program runs. It catches the mutant unchecked-string-length of
// runtime/v1's ledger.
func TestBoundsFactsAgree(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed      uint64
		fn        *boundsFn
		name      string
		src, want string
		defect    bool
	}
	var runs []run
	var programs []*lowered
	seeds := boundsSeedList()
	if *boundsSeed == 0 {
		seeds = seeds[:4] // a program per function and lowering: half the seeds
	}
	for _, seed := range seeds {
		c := boundsProgram(seed)
		probe := mustLower(t, "probe", c.source(nil))
		unchecked := uncheckedSites(probe.mainGo())
		r := rand.New(rand.NewPCG(seed, 0x616772))
		for _, f := range c.fns {
			var calls []string
			var want strings.Builder
			var bad string
			eachState(func(ls, lxs, a, b int64) {
				st := &bstate{env: map[string]int64{"a0": a, "b": b}, ls: ls, lxs: lxs, reached: map[int]bool{}, unchecked: unchecked[f.name]}
				f.run(st)
				call := fmt.Sprintf("%s(%s, %s, %s, %s)", f.name, boundsString(ls), boundsSlice(lxs), boundsInt(a), boundsInt(b))
				switch {
				case st.stopped:
					if bad == "" || r.IntN(8) == 0 {
						bad = call
					}
				case len(st.reached) > 0 && len(calls) < 24 && r.IntN(6) == 0:
					calls = append(calls, call)
					fmt.Fprintf(&want, "%d\n", st.acc)
				}
			})
			if bad != "" {
				calls = append(calls, bad)
			}
			one := &boundsCase{seed: seed, fns: []*boundsFn{f}}
			src := one.source(calls)
			for _, mode := range []string{"", "checked"} {
				name := fmt.Sprintf("bounds%d%s%s", seed, f.name, mode)
				saved := factLowering
				factLowering = mode
				programs = append(programs, mustLower(t, name, src))
				factLowering = saved
				runs = append(runs, run{seed, f, name, src, want.String(), bad != ""})
			}
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the bounds programs do not build")
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		ok := out == r.want
		if r.defect {
			ok = ok && code != 0 && strings.Contains(stderr, "out of range")
		} else {
			ok = ok && code == 0
		}
		if !ok {
			t.Errorf("seed %d, %s: the program and the model differ\nReplay: %s\n%s\n--- program (exit %d)\n%s%s\n--- model (stops at a bounds failure: %v)\n%s", r.seed, r.name, boundsReplay(r.seed), r.src, code, out, firstLines(stderr, 5), r.defect, r.want)
		}
	}
}

// boundsString is the Ergo text of the string s of length n.
func boundsString(n int64) string {
	var b strings.Builder
	for i := range n {
		b.WriteByte(byte(sByte(i)))
	}
	return fmt.Sprintf("%q", b.String())
}

// boundsSlice is the Ergo text of the slice xs of length n.
func boundsSlice(n int64) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprint(xsAt(i))
	}
	return "[]int{" + strings.Join(parts, ", ") + "}"
}

// boundsInt is the Ergo text of an int, the smallest one included.
func boundsInt(n int64) string {
	if n == math.MinInt64 {
		return "(-9223372036854775807 - 1)"
	}
	return fmt.Sprint(n)
}
