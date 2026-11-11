package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
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
	"soft-missing", "later", "consts", "loop", "map", "end-normal", "end-fail", "end-unreachable", "end-zero", "end-gone", "pick2",
}

const formPrelude = `type Rec { a int, b string, c int, d int, e int }

type Box[T] { items []T, n int }

type Missing { At int }

let limit = 7
let label = "lim"
let zero = 0
let big = 9223372036854775807

func at[T](b *Box[T], i int) T fails Missing {
	if i < 0 || i >= len(b.items) {
		return Missing{At: i}
	}
	b.items[i]
}

func must[T](b *Box[T], i int) T {
	at(b, i) catch {
	case Missing:
		Unreachable("no item \(i)")
	}
}

func orFirst[T](b *Box[T], i int) T {
	at(b, i) catch {
	case Missing:
		return b.items[0]
	}
}

func viaLet[T](b *Box[T], i int) T {
	let r = must(b, i)
	r
}

func bumped[T](b *Box[T], i int, f func(r *T)) T {
	let r = must(b, i)
	f(&b.items[i])
	r
}

func mixed(b *Box[Rec], i int, k int) Rec {
	let mut r = must(b, i)
	r.c = r.c + k
	r
}

func soft[T](b *Box[T], i int, d T) T {
	at(b, i) catch {
	case Missing:
		d
	}
}

func later(b *Box[Rec], i int) func() int {
	let r = must(b, i)
	func() int { r.a + r.c }
}

func keyed[T](b *Box[T], i int) int {
	let a = i * 3 + b.n
	let r = Rec{a: a, b: "k", c: 1, d: 0, e: 0}
	b.n = r.a - a
	a
}

func counted[T](b *Box[T], i int) int {
	let n = i * 5 + b.n
	b.n = b.n * 1
	n
}

type Gone {}

func pick2(b *Box[Rec], i int) int fails Missing | Gone {
	if i == 5 {
		return Gone
	}
	let r = try at(b, i)
	r.a
}

func loud(b *Box[Rec], i int) int fails Missing needs Console {
	Console.Println("loud")
	let r = try at(b, i)
	r.a
}

func quiet(b *Box[Rec]) int needs Console {
	loud(b, 0) catch {
	case Missing:
		-1
	}
}

type Index { by Map[int, int], alt Map[int, int], hits int }

func drop(x *Index, k int) {
	x.by = x.by.Delete(k)
	x.hits = x.hits + 1
}

func show(r Rec) string { "\(r.a) \(r.b) \(r.c) \(r.d) \(r.e)" }

type Hook { f func() int }

func hooked(b *Box[Rec], h *Hook, i int) Rec {
	let r = must(b, i)
	h.f = func() int { r.a }
	r
}

type Sq { a int, w int }
type Ci { a int }
type Shape = Sq | Ci

func sq(n int) Sq { Sq{a: n, w: n} }

func shapeVar[T](b *Box[T], n int) Shape {
	let mut s = Sq{a: n, w: 1}
	s.w = s.w + n
	s
}

func shapeDef[T](b *Box[T], n int) Shape {
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
	switch g.r.IntN(12) {
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
		g.line(fmt.Sprintf("let %s = bumped(&b, %d, func(r *Rec) { r.d = r.d + %d })", v, i, k))
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

// formProgram is the program and the model's stdout.
func formProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &formGen{r: rand.New(rand.NewPCG(seed, 0x464f524d)), reached: reached}
	g.items = []formRec{{3, "x", 1, 4, 1}, {5, "y", 9, 2, 6}}
	g.line("let mut b = Box[Rec]{items: []Rec{Rec{a: 3, b: \"x\", c: 1, d: 4, e: 1}, Rec{a: 5, b: \"y\", c: 9, d: 2, e: 6}}, n: 0}")
	g.line("let mut ix = Index{by: Map[int, int]{}, alt: Map[int, int]{}, hits: 0}")
	g.ix = map[int]int{}
	// The tail of a body returned into an interface result (a union):
	// a let mut of another Go type, and a := of a call's result. Neither
	// becomes the result variable (renameTail): the body reads its fields.
	g.line("let mut h = Hook{f: func() int { 0 }}")
	g.line("Console.Println(width(shapeVar(&b, 3)) + \" \" + width(shapeDef(&b, 5)))")
	g.out("4 5")
	for range 24 {
		g.stmt()
	}
	g.line("Console.Println(digest(&b))")
	g.out("%s", g.digest())
	g.line("Console.Println(\"\\(quiet(&b))\")")
	g.out("loud\n%d", g.items[0].a)
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
	digest := "\nfunc digest(b *Box[Rec]) string {\n\tlet mut parts = []string{}\n\tfor r in b.items {\n\t\tparts = parts.Append(show(r))\n\t}\n\tstrings.Join(parts, \";\")\n}\n"
	return "import strings\n\n" + formPrelude + digest + "func main() fails Missing | Gone needs Console {\n" + g.b.String() + "}\n", g.want.String()
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
			st := &newStats
			if ref {
				st = &refStats
			}
			st.Aliased += s.Aliased
			st.Renamed += s.Renamed
			st.Consts += s.Consts
			st.MapStores += s.MapStores
			st.SoleFailures += s.SoleFailures
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
	if newStats.Aliased == 0 || newStats.Renamed == 0 || newStats.Consts == 0 || newStats.MapStores == 0 || newStats.SoleFailures == 0 {
		t.Errorf("the new lowering used no form: %+v", newStats)
	}
	if refStats.Aliased != 0 || refStats.Renamed != 0 || refStats.Consts != 0 || refStats.MapStores != 0 || refStats.SoleFailures != 0 {
		t.Errorf("the reference lowering used a form: %+v", refStats)
	}
}
