package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The seeded property of the value forms (forms.go). Each seed writes one
// program over a let mut Box of records of five fields (more than Go's
// four-word SSA limit, so every copy is a memory copy). Its statements
// read a record through inlined helpers whose catch arms all leave
// (must, orFirst), name it with a let (aliasLet), return a let or a
// temporary as an inlined body's tail (viaLet, bumped: renameTail), and
// then write the record's source: the let keeps the value it had (value
// semantics). A let mut tail (mixed), a catch whose arm yields (soft), a
// literal that captures a let (later; hooked stores one through a borrow,
// and a let mut of the call's value is then written), tails returned into
// an interface result from another Go type (shapeVar, shapeDef), tails that share a name with a key
// or a field (keyed, counted), package lets of literals read where Go may
// fold them (limit, label: Go consts) or not (zero as a divisor, big + 1:
// variables), in-place Map writes stored back to their own place or into
// another (drop, ix), and catches of a one-variant row and of a
// two-variant row (pick2) are reached too. The program ends normally,
// with a typed failure of main (one of them through a partial catch of
// one arm over two variants), with a defect raised by an Unreachable in
// an inlined helper, or with a division by zero. A let mut of a let
// (mixed) is two variables. A fixed catch (quiet) of a callee with a
// capability keeps its type switch and default under both lowerings: an
// interruption may come back as that callee's error.
//
// The program runs under the new lowering and under the reference
// (formLowering "reference"); stdout must equal the model's, a Go fold of
// the same statements, and the exit code and standard error must be the
// same under both. A failing seed prints the Ergo source, both outputs
// and its replay command. The fixed seeds reach every path in formPaths,
// and the new lowering uses each form, the reference none.

var formSeed = flag.Uint64("forms.seed", 0, "run TestFormsAgree on this seed alone")

// formPaths are the generator's branches; the fixed seeds reach each.
var formPaths = []string{
	"must", "must-write", "orFirst-ok", "orFirst-missing", "viaLet", "bumped", "mixed", "soft-ok",
	"soft-missing", "later", "consts", "loop", "map", "end-normal", "end-fail", "end-unreachable", "end-zero", "end-gone", "pick2", "dropped",
	"scan-scanClasses-edge", "scan-scanClasses-bare", "scan-scanMask-edge", "scan-scanMask-bare", "scan-scanWide-edge", "scan-scanWide-bare",
	"scan-scanRoom-edge", "scan-scanRoom-bare", "scan-scanZero-edge", "scan-scanZero-bare",
	"local-lClasses-edge", "local-lClasses-bare", "local-lMask-edge", "local-lMask-bare", "local-lWide-edge", "local-lWide-bare",
	"local-lRoom-edge", "local-lRoom-bare", "local-lZero-edge", "local-lZero-bare",
}

// scanLets are the lets of S2b's scanner places (constFold's place
// rules). scanned reads each in a place where no Go fold can fail; a
// program adds, for some, an edge use where a fold could fail (scanEdges),
// and that let must then stay v1.Let.
var scanLets = []string{"scanClasses", "scanMask", "scanWide", "scanRoom", "scanZero"}

// scanEdges are the edge uses: a constant index out of range, a constant
// shifted by a count past its width, a conversion that overflows, a
// product of constants that overflows, a division by zero. The first and
// the last are defects, so they sit in a branch that never runs; Go would
// still refuse their fold at build time.
var scanEdges = map[string]struct {
	never bool
	src   string
	out   string
}{
	"scanClasses": {true, "let eClasses = scanClasses[9]\n\t\tConsole.Println(\"\\(eClasses)\")", ""},
	"scanMask":    {false, "Console.Println(\"\\(scanMask << scanShift) \\(uint64(b.items[0].c) >> scanShift)\")", "0 0"},
	"scanWide":    {false, "let eWide = uint8(scanWide)\n\tConsole.Println(\"\\(eWide)\")", "44"},
	"scanRoom":    {false, "let eRoom = scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom * scanRoom\n\tConsole.Println(\"\\(eRoom)\")", "0"},
	"scanZero":    {true, "let eZero = b.items[0].a % scanZero\n\t\tConsole.Println(\"\\(eZero)\")", ""},
}

const formPrelude = `type Rec { a int, b string, c int, d int, e int }

type Box[T] { items []T, n int }

type Missing { At int }

let limit = 7
let label = "lim"
let zero = 0
let big = 9223372036854775807

let scanClasses = "\x00\x09\x0a\x04\x00\x10"
let scanMask uint64 = 0xff00
let scanLow uint64 = 0x000f
let scanLimit = uint32(0xffffffff)
let scanWide = 300
let scanRoom = 64
let scanZero = 0
let scanShift = 70

// scanned reads the scan lets where no Go fold over them can fail: a
// constant string indexed by a variable, a constant shifted by a
// variable, a bitwise operator over constants, conversions that fit, a
// divisor other than zero, a sum with a variable, an interpolated value.
func scanned(i int, k uint64, n int) string {
	let c = scanClasses[i]
	let bit = (scanMask >> k) & uint64(1)
	let both = (scanMask | scanLow) == uint64(0xff0f)
	let fits = n >= int(scanLimit)
	let narrow = int64(n) < int64(scanWide)
	let slot = n % scanRoom
	let z = n + scanZero
	"\(c) \(bit) \(both) \(fits) \(narrow) \(slot) \(z) \(scanRoom)"
}

func at[T](b mut Box[T], i int) T fails Missing {
	if i < 0 || i >= len(b.items) {
		return Missing{At: i}
	}
	b.items[i]
}

func must[T](b mut Box[T], i int) T {
	at(b, i) catch {
	case Missing:
		Unreachable("no item \(i)")
	}
}

func orFirst[T](b mut Box[T], i int) T {
	at(b, i) catch {
	case Missing:
		return b.items[0]
	}
}

func viaLet[T](b mut Box[T], i int) T {
	let r = must(b, i)
	r
}

func bumped[T](b mut Box[T], i int, f func(r mut T)) T {
	let r = must(b, i)
	f(&b.items[i])
	r
}

func mixed(b mut Box[Rec], i int, k int) Rec {
	let mut r = must(b, i)
	r.c = r.c + k
	r
}

func soft[T](b mut Box[T], i int, d T) T {
	at(b, i) catch {
	case Missing:
		d
	}
}

func later(b mut Box[Rec], i int) func() int {
	let r = must(b, i)
	func() int { r.a + r.c }
}

func keyed[T](b mut Box[T], i int) int {
	let a = i * 3 + b.n
	let r = Rec{a: a, b: "k", c: 1, d: 0, e: 0}
	b.n = r.a - a
	a
}

func counted[T](b mut Box[T], i int) int {
	let n = i * 5 + b.n
	b.n = b.n * 1
	n
}

type Gone {}

func pick2(b mut Box[Rec], i int) int fails Missing | Gone {
	if i == 5 {
		return Gone
	}
	let r = try at(b, i)
	r.a
}

func loud(b mut Box[Rec], i int) int fails Missing needs Console {
	Console.Println("loud")
	let r = try at(b, i)
	r.a
}

func quiet(b mut Box[Rec]) int needs Console {
	loud(b, 0) catch {
	case Missing:
		-1
	}
}

type Index { by Map[int, int], alt Map[int, int], hits int }

func drop(x mut Index, k int) {
	x.by = x.by.Delete(k)
	x.hits = x.hits + 1
}

func show(r Rec) string { "\(r.a) \(r.b) \(r.c) \(r.d) \(r.e)" }

func stash(b mut Box[Rec], i int, k int) Rec {
	let r = must(b, i)
	for j in 0..k {
		b.items[i].e = b.items[i].e + j
	}
	if r.a > 10 {
		return Rec{..r, a: 0}
	}
	b.items[i]
}

func noisy(b mut Box[Rec], i int) int needs Console {
	Console.Println("noisy \(i)")
	for j in 0..2 {
		b.items[i].d = b.items[i].d + j + 1
	}
	i * 2
}

func tally(xs []int, k int) int {
	let mut ys = xs
	ys[0] = ys[0] + k
	for j in 0..k {
		ys[0] = ys[0] + j
	}
	ys[0]
}

type Hook { f func() int }

func hooked(b mut Box[Rec], h mut Hook, i int) Rec {
	let r = must(b, i)
	h.f = func() int { r.a }
	r
}

type Sq { a int, w int }
type Ci { a int }
type Shape = Sq | Ci

func sq(n int) Sq { Sq{a: n, w: n} }

func shapeVar[T](b mut Box[T], n int) Shape {
	let mut s = Sq{a: n, w: 1}
	s.w = s.w + n
	s
}

func shapeDef[T](b mut Box[T], n int) Shape {
	let s = sq(n)
	if s.a > 100 {
		return Ci{a: s.w}
	}
	s
}

func width(x Shape) string {
	match x {
	case Sq as q:
		"\(q.w)"
	case Ci as c:
		"c\(c.a)"
	}
}
`

// formGen writes one program and its model.
type formGen struct {
	r       *rand.Rand
	b       strings.Builder
	want    strings.Builder
	items   []formRec
	ix      map[int]int
	hits    int
	reached map[string]bool
	names   int
	hook    int // what h.f returns
}

type formRec struct {
	a    int
	b    string
	c, d int
	e    int
}

func (r formRec) show() string { return fmt.Sprintf("%d %s %d %d %d", r.a, r.b, r.c, r.d, r.e) }

func (g *formGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *formGen) out(format string, args ...any) { fmt.Fprintf(&g.want, format+"\n", args...) }

func (g *formGen) name(base string) string {
	g.names++
	return fmt.Sprintf("%s%d", base, g.names)
}

// valid is an index of an item; index one in range or out of it.
func (g *formGen) valid() int { return g.r.IntN(len(g.items)) }
func (g *formGen) index() int { return []int{-1, 0, 1, 2, 7}[g.r.IntN(5)] }

func (g *formGen) ok(i int) bool { return i >= 0 && i < len(g.items) }

func (g *formGen) stmt() {
	switch g.r.IntN(13) {
	case 0:
		// A let of a caught read, then a write of its source.
		i, v := g.valid(), g.name("r")
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("let %s = must(&b, %d)", v, i))
		} else {
			g.line(fmt.Sprintf("let %s = b.items[%d]", v, i)) // a place, not a temporary
		}
		old := g.items[i]
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("b.items[%d] = Rec{..b.items[%d], a: b.items[%d].a + 5, b: \"w\"}", i, i, i))
			g.items[i].a += 5
			g.items[i].b = "w"
			g.reached["must-write"] = true
		}
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
		g.out("%s", old.show())
		g.reached["must"] = true
	case 1:
		i, v := g.index(), g.name("r")
		g.line(fmt.Sprintf("let %s = orFirst(&b, %d)", v, i))
		g.line("b.items[0].c = b.items[0].c + 1")
		if g.ok(i) {
			g.out("%s", g.items[i].show())
			g.reached["orFirst-ok"] = true
		} else {
			g.out("%s", g.items[0].show())
			g.reached["orFirst-missing"] = true
		}
		g.items[0].c++
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
	case 2:
		i := g.valid()
		g.line(fmt.Sprintf("Console.Println(show(viaLet(&b, %d)))", i))
		g.out("%s", g.items[i].show())
		g.reached["viaLet"] = true
	case 3:
		// The callback writes the record the let read: the let keeps the
		// value it had.
		i, v, k := g.valid(), g.name("r"), g.r.IntN(9)+1
		g.line(fmt.Sprintf("let %s = bumped(&b, %d, func(r mut Rec) { r.d = r.d + %d })", v, i, k))
		g.line(fmt.Sprintf("Console.Println(show(%s) + \" \" + show(b.items[%d]))", v, i))
		old := g.items[i]
		g.items[i].d += k
		g.out("%s %s", old.show(), g.items[i].show())
		g.reached["bumped"] = true
	case 4:
		i, k := g.valid(), g.r.IntN(9)
		g.line(fmt.Sprintf("Console.Println(show(mixed(&b, %d, %d)) + \" \" + show(b.items[%d]))", i, k, i))
		m := g.items[i]
		m.c += k
		g.out("%s %s", m.show(), g.items[i].show())
		// A let mut of a let: two variables, not one.
		ma, mb := g.name("ma"), g.name("mb")
		g.line(fmt.Sprintf("let %s = must(&b, %d)", ma, i))
		g.line(fmt.Sprintf("let mut %s = %s", mb, ma))
		g.line(fmt.Sprintf("%s.c = %s.c + 1", mb, mb))
		g.line(fmt.Sprintf("Console.Println(show(%s) + \" \" + show(%s))", ma, mb))
		n := g.items[i]
		n.c++
		g.out("%s %s", g.items[i].show(), n.show())
		g.reached["mixed"] = true
	case 5:
		i, v := g.index(), g.name("r")
		g.line(fmt.Sprintf("let %s = soft(&b, %d, Rec{a: -1, b: \"d\", c: 0, d: 0, e: 0})", v, i))
		g.line(fmt.Sprintf("Console.Println(show(%s))", v))
		if g.ok(i) {
			g.out("%s", g.items[i].show())
			g.reached["soft-ok"] = true
		} else {
			g.out("%s", formRec{a: -1, b: "d"}.show())
			g.reached["soft-missing"] = true
		}
	case 6:
		// A literal keeps the let it captures past a write of its source.
		i, f := g.valid(), g.name("f")
		g.line(fmt.Sprintf("let %s = later(&b, %d)", f, i))
		g.line(fmt.Sprintf("b.items[%d].a = 0", i))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s())\")", f))
		g.out("%d", g.items[i].a+g.items[i].c)
		g.items[i].a = 0
		g.reached["later"] = true
		// A literal stored through a borrow keeps the let it captures
		// when a let mut of the call's value is written: the body's tail
		// does not become the result variable (renameTail).
		hv := g.name("hv")
		g.line(fmt.Sprintf("let mut %s = hooked(&b, &h, %d)", hv, i))
		g.line(fmt.Sprintf("%s.a = %s.a + 100", hv, hv))
		g.line(fmt.Sprintf("Console.Println(\"\\(h.f()) \\(%s.a)\")", hv))
		g.out("%d %d", g.items[i].a, g.items[i].a+100)
		g.hook = g.items[i].a
	case 7:
		// limit and label are read where no Go fold can fail (Go
		// consts); zero is a divisor, so it stays a variable, and a
		// division by it is the run's defect, not a build error.
		i := g.valid()
		g.line(fmt.Sprintf("Console.Println(label + \"\\(b.items[%d].a + limit) \\(b.items[%d].a > limit)\")", i, i))
		g.out("lim%d %v", g.items[i].a+7, g.items[i].a > 7)
		g.reached["consts"] = true
		j := g.index()
		if j == 7 {
			j = 2
		}
		p := g.name("p")
		g.line(fmt.Sprintf("let %s = pick2(&b, %d) catch {\n\tcase Missing:\n\t\t-1\n\tcase Gone:\n\t\t-2\n\t}", p, j))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", p))
		if g.ok(j) {
			g.out("%d", g.items[j].a)
		} else {
			g.out("-1")
		}
		g.reached["pick2"] = true
		// big + 1 is two constants: it stays a variable, and the sum
		// wraps at run time.
		g.line("Console.Println(\"\\(big + 1 < 0)\")")
		g.out("true")
		// A let read through a field name it shares with a key.
		g.line(fmt.Sprintf("Console.Println(\"\\(keyed(&b, %d)) \\(counted(&b, %d))\")", i, i))
		g.out("%d %d", i*3, i*5)
	case 8:
		// A let in a loop body: a fresh temporary per iteration.
		g.line("for j in 0..len(b.items) {\n\t\tlet r = must(&b, j)\n\t\tb.items[j].e = r.e + r.a\n\t}")
		for j := range g.items {
			g.items[j].e += g.items[j].a
		}
		g.reached["loop"] = true
	case 10:
		// In-place Map writes stored back to their own place, and one
		// into another place, which takes the written map: a snapshot
		// taken before keeps its keys (value semantics).
		k, v := g.r.IntN(6), g.r.IntN(50)
		s := g.name("s")
		g.line(fmt.Sprintf("let %s = ix.by", s))
		g.line(fmt.Sprintf("ix.by = ix.by.Put(%d, %d)", k, v))
		g.line(fmt.Sprintf("drop(&ix, %d)", (k+1)%6))
		g.line(fmt.Sprintf("let mut m%s = ix.by", s))
		g.line(fmt.Sprintf("m%s = m%s.Delete(%d)", s, s, k))
		g.line(fmt.Sprintf("Console.Println(\"\\(len(%s)) \\(len(ix.by)) \\(len(m%s)) \\(ix.hits)\")", s, s))
		before := len(g.ix)
		g.ix[k] = v
		delete(g.ix, (k+1)%6)
		g.hits++
		after := len(g.ix)
		m := after
		if _, ok := g.ix[k]; ok {
			m--
		}
		g.out("%d %d %d %d", before, after, m, g.hits)
		// A dead Map written in place into another place: the place
		// takes it.
		q := g.name("q")
		g.line(fmt.Sprintf("let mut %s = Map[int, int]{}", q))
		g.line(fmt.Sprintf("%s = %s.Put(%d, 1)", q, q, k))
		g.line(fmt.Sprintf("ix.alt = %s.Put(%d, 2)", q, k+10))
		g.line("Console.Println(\"\\(len(ix.alt))\")")
		g.out("2")
		g.reached["map"] = true
	case 11:
		// Discarded results: the variant with no result runs the body's
		// effects in order (a loop's writes, a capability call); a callee
		// with an owned parameter keeps its wrapper, which copies the
		// slice the body writes.
		i, k := g.valid(), g.r.IntN(4)
		g.line(fmt.Sprintf("let _ = stash(&b, %d, %d)", i, k))
		g.items[i].e += k * (k - 1) / 2
		g.line(fmt.Sprintf("let _ = noisy(&b, %d)", i))
		g.out("noisy %d", i)
		g.items[i].d += 3
		g.line(fmt.Sprintf("Console.Println(show(b.items[%d]))", i))
		g.out("%s", g.items[i].show())
		zs := g.name("zs")
		g.line(fmt.Sprintf("let %s = []int{%d, 1}", zs, k))
		g.line(fmt.Sprintf("let _ = tally(%s, %d)", zs, k+1))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s[0])\")", zs))
		g.out("%d", k)
		g.reached["dropped"] = true
	case 9:
		v := g.r.IntN(30)
		g.line(fmt.Sprintf("b.items = b.items.Append(Rec{a: %d, b: \"n%d\", c: 1, d: 2, e: 3})", v, v))
		g.items = append(g.items, formRec{a: v, b: fmt.Sprintf("n%d", v), c: 1, d: 2, e: 3})
	default:
		g.line("Console.Println(digest(&b))")
		g.out("%s", g.digest())
	}
}

func (g *formGen) digest() string {
	var parts []string
	for _, r := range g.items {
		parts = append(parts, r.show())
	}
	return strings.Join(parts, ";")
}

// scanConsts reads the scan lets in their safe places (scanned) and adds
// the edge use of every other one, alternating by seed so the fixed seeds
// reach each let with and without its edge. It returns the lets with an
// edge use.
func (g *formGen) scanConsts(seed uint64) map[string]bool {
	i, k, n := g.r.IntN(6), uint64(g.r.IntN(16)), g.r.IntN(1000)-500
	g.line(fmt.Sprintf("Console.Println(scanned(%d, uint64(%d), %d))", i, k, n))
	classes := "\x00\x09\x0a\x04\x00\x10"
	g.out("%d %d %v %v %v %d %d 64", classes[i], (uint64(0xff00)>>k)&1, true, false, n < 300, n%64, n)
	edges := map[string]bool{}
	var never []string
	for j, name := range scanLets {
		if (seed+uint64(j))%2 != 0 {
			g.reached["scan-"+name+"-bare"] = true
			continue
		}
		g.reached["scan-"+name+"-edge"] = true
		edges[name] = true
		e := scanEdges[name]
		if e.never {
			never = append(never, e.src)
			continue
		}
		g.line(e.src)
		g.out("%s", e.out)
	}
	// The same lets as locals of main (localConst), read in the same safe
	// places by variable operands, with the edge use of the other half.
	g.line("let lClasses = \"\\x00\\x09\\x0a\\x04\\x00\\x10\"\n\tlet lMask uint64 = 0xff00\n\tlet lWide = 300\n\tlet lRoom = 64\n\tlet lZero = 0\n\tlet lShift = 70")
	g.line(fmt.Sprintf("let mut li = %d\n\tlet mut lk = uint64(%d)\n\tlet mut ln = %d\n\tli = li + 0\n\tlk = lk + 0\n\tln = ln + 0", i, k, n))
	g.line("Console.Println(\"\\(lClasses[li]) \\((lMask >> lk) & uint64(1)) \\(int64(ln) < int64(lWide)) \\(ln % lRoom) \\(ln + lZero) \\(ln < lShift)\")")
	g.out("%d %d %v %d %d %v", classes[i], (uint64(0xff00)>>k)&1, n < 300, n%64, n, n < 70)
	for j, name := range localLets {
		if (seed+uint64(j)+1)%2 != 0 {
			g.reached["local-"+name+"-bare"] = true
			continue
		}
		g.reached["local-"+name+"-edge"] = true
		edges[name] = true
		e := scanEdges[localOf[name]]
		src := strings.NewReplacer("scanClasses", "lClasses", "scanMask", "lMask", "scanShift", "lShift", "scanWide", "lWide", "scanRoom", "lRoom", "scanZero", "lZero", "eClasses", "leClasses", "eWide", "leWide", "eRoom", "leRoom", "eZero", "leZero").Replace(e.src)
		if e.never {
			never = append(never, src)
			continue
		}
		g.line(src)
		g.out("%s", e.out)
	}
	if len(never) > 0 {
		g.line("if b.n > 1000000 {\n\t\t" + strings.Join(never, "\n\t\t") + "\n\t}")
	}
	return edges
}

// localLets are scanLets' twins as locals of main (localConst), by the
// package let whose edge use each borrows.
var localLets = []string{"lClasses", "lMask", "lWide", "lRoom", "lZero"}

var localOf = map[string]string{"lClasses": "scanClasses", "lMask": "scanMask", "lWide": "scanWide", "lRoom": "scanRoom", "lZero": "scanZero"}

// formProgram is the program and the model's stdout.
func formProgram(seed uint64, reached map[string]bool) (src, want string) {
	src, want, _ = formProgramEdges(seed, reached)
	return src, want
}

// formProgramEdges is formProgram with the scan lets that have an edge
// use (scanConsts).
func formProgramEdges(seed uint64, reached map[string]bool) (src, want string, edges map[string]bool) {
	g := &formGen{r: rand.New(rand.NewPCG(seed, 0x464f524d)), reached: reached}
	g.items = []formRec{{3, "x", 1, 4, 1}, {5, "y", 9, 2, 6}}
	g.line("let mut b = Box[Rec]{items: []Rec{Rec{a: 3, b: \"x\", c: 1, d: 4, e: 1}, Rec{a: 5, b: \"y\", c: 9, d: 2, e: 6}}, n: 0}")
	g.line("let mut ix = Index{by: Map[int, int]{}, alt: Map[int, int]{}, hits: 0}")
	g.line("ix.hits = ix.hits + 0")
	g.ix = map[int]int{}
	// The tail of a body returned into an interface result (a union):
	// a let mut of another Go type, and a := of a call's result. Neither
	// becomes the result variable (renameTail): the body reads its fields.
	g.line("let mut h = Hook{f: func() int { 1 }}")
	g.line("h.f = func() int { 0 }")
	g.line("Console.Println(width(shapeVar(&b, 3)) + \" \" + width(shapeDef(&b, 5)))")
	g.out("4 5")
	for range 24 {
		g.stmt()
	}
	g.line("Console.Println(\"\\(h.f())\")")
	g.out("%d", g.hook)
	g.line("Console.Println(\"\\(ix.hits)\")")
	g.out("%d", g.hits)
	g.line("Console.Println(digest(&b))")
	g.out("%s", g.digest())
	g.line("Console.Println(\"\\(quiet(&b))\")")
	g.out("loud\n%d", g.items[0].a)
	edges = g.scanConsts(seed)
	switch seed % 5 { // each ending in any five consecutive seeds
	case 4:
		// A partial catch of one arm over a row of two: the other variant
		// leaves main as its typed failure, not through the arm.
		g.line("let gone = try pick2(&b, 5) catch {\n\tcase Missing:\n\t\t-1\n\t}")
		g.line("Console.Println(\"\\(gone)\")")
		g.reached["end-gone"] = true
	case 0:
		g.reached["end-normal"] = true
	case 1:
		g.line("let x = try at(&b, 9)")
		g.line("Console.Println(show(x))")
		g.reached["end-fail"] = true
	case 3:
		g.line("Console.Println(\"\\(b.items[0].a / zero)\")")
		g.reached["end-zero"] = true
	default:
		g.line("Console.Println(show(must(&b, 9)))")
		g.reached["end-unreachable"] = true
	}
	digest := "\nfunc digest(b mut Box[Rec]) string {\n\tlet mut parts = []string{}\n\tfor r in b.items {\n\t\tparts = parts.Append(show(r))\n\t}\n\tstrings.Join(parts, \";\")\n}\n"
	return "import strings\n\n" + formPrelude + digest + "func main() fails Missing | Gone needs Console {\n" + g.b.String() + "}\n", g.want.String(), edges
}

// lowerFormMode lowers src under the new lowering, or under the reference
// (ref: formLowering "reference"), and returns the lowering's counts.
func lowerFormMode(t *testing.T, name, src string, ref bool) (*lowered, knownStats) {
	t.Helper()
	saved, savedHook := formLowering, statsHook
	defer func() { formLowering, statsHook = saved, savedHook }()
	formLowering = ""
	if ref {
		formLowering = "reference"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats
}

// TestFormsAgree is the seeded property (see above).
//
// It builds Go because the question is the run of each program, its
// defect report included: the report reads Go's traceback of the built
// program, which no in-process check produces.
func TestFormsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *formSeed != 0 {
		seeds = []uint64{*formSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	var newStats, refStats knownStats
	for _, seed := range seeds {
		src, want := formProgram(seed, reached)
		runs = append(runs, run{seed, src, want})
		for _, ref := range []bool{false, true} {
			name := fmt.Sprintf("fm%d", seed)
			if ref {
				name += "ref"
			}
			p, s := lowerFormMode(t, name, src, ref)
			// The callee of quiet's catch has a capability: an
			// interruption may come back as its error, so the catch
			// keeps its type switch and re-panicking default (rows.md,
			// Catch 6) in both lowerings. The bug: soleFailure took a
			// queue's Push, whose error may be rt.Interrupted, for its
			// one variant.
			if q := goFunc(t, p.mainGo(), "quiet"); !strings.Contains(q, "switch ") || !strings.Contains(q, "panic(") {
				t.Errorf("%s: the catch of a callee with a capability lost its switch or default:\n%s", name, q)
			}
			// The promised fact of the variant with no result: it
			// returns nothing, so Go drops the value no one reads.
			if code := p.mainGo(); strings.Contains(code, "func stashDropped(") && !regexp.MustCompile(`(?m)^func stashDropped\([^)]*\) \{$`).MatchString(code) {
				t.Errorf("%s: the variant with no result of stash returns a value", name)
			}
			st := &newStats
			if ref {
				st = &refStats
			}
			st.Aliased += s.Aliased
			st.Renamed += s.Renamed
			st.Consts += s.Consts
			st.MapStores += s.MapStores
			st.SoleFailures += s.SoleFailures
			st.Dropped += s.Dropped
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the form programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		name := fmt.Sprintf("fm%d", r.seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != r.want || refOut != r.want || stderr != refErr || code != refCode {
			t.Errorf("seed %d: the lowerings and the model differ\nReplay: go test -run '^TestFormsAgree$' ./internal/sketch/lower -args -forms.seed=%d\n%s\n--- new (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, refCode, refOut, refErr, r.want)
		}
	}
	t.Logf("seeds %v: new %+v, reference %+v", seeds, newStats, refStats)
	if *formSeed != 0 {
		return
	}
	for _, path := range formPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if newStats.Aliased == 0 || newStats.Renamed == 0 || newStats.Consts == 0 || newStats.MapStores == 0 || newStats.SoleFailures == 0 || newStats.Dropped == 0 {
		t.Errorf("the new lowering used no form: %+v", newStats)
	}
	if refStats.Aliased != 0 || refStats.Renamed != 0 || refStats.Consts != 0 || refStats.MapStores != 0 || refStats.SoleFailures != 0 || refStats.Dropped != 0 {
		t.Errorf("the reference lowering used a form: %+v", refStats)
	}
}

// TestConstFoldPlaces is the in-process half of the property for the
// scan lets (scanConsts): under the new lowering a scan let is read as
// its bare Go constant exactly when the program has no edge use of it,
// and every program's Go type-checks (typecheckGo), so no fold of an edge
// use (scanClasses[9], scanMask << scanShift, uint8(scanWide), a product
// of scanRoom that overflows, % scanZero) reaches Go. TestFormsAgree runs
// the same programs and compares their output with the model.
//
// Mutants (lower ledger): const-fold-index-constant (a constant index is
// a place), const-fold-fits-any (every conversion fits) and
// const-fold-divisor-any (a zero divisor is a place) make an edge use's
// fold reach Go, which go/types refuses.
func TestConstFoldPlaces(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *formSeed != 0 {
		seeds = []uint64{*formSeed}
	}
	var programs []*lowered
	for _, seed := range seeds {
		src, _, edges := formProgramEdges(seed, map[string]bool{})
		p, _ := lowerFormMode(t, fmt.Sprintf("cf%d", seed), src, false)
		code := p.mainGo()
		for _, name := range append([]string{"scanLow"}, scanLets...) {
			if read := regexp.MustCompile(`\.Let\(` + name + `\)`).MatchString(code); read != edges[name] {
				t.Errorf("seed %d: %s read through v1.Let: %v, with an edge use: %v\nReplay: go test -run '^TestConstFoldPlaces$' ./internal/sketch/lower -args -forms.seed=%d", seed, name, read, edges[name], seed)
			}
		}
		for _, name := range localLets {
			if isConst := regexp.MustCompile(`(?m)^\s*const ` + name + ` `).MatchString(code); isConst == edges[name] {
				t.Errorf("seed %d: local %s a Go const: %v, with an edge use: %v\nReplay: go test -run '^TestConstFoldPlaces$' ./internal/sketch/lower -args -forms.seed=%d", seed, name, isConst, edges[name], seed)
			}
		}
		programs = append(programs, p)
	}
	typecheckGo(t, programs)
}
