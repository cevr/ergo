package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of monomorphic copies (mono.go; MONO1). Each seed
// writes one program over a fixed set of generic functions, each with a
// loop so the Ergo inliner leaves its call a call:
//   - twist[A, B] instantiates swap at [A, B] and then at [B, A] inside
//     itself (a nested instance whose arguments are the copy's own,
//     swapped), and mk at [A, B];
//   - both[T Show] takes a witness, and pairUp[T Show] passes its own
//     witness on to both (a copy calling a copy with a witness);
//   - big[T ordered] and total[T number] use the operators of a
//     type-set constraint;
//   - depth[T] calls itself (recursion at one instance);
//   - add and size are methods of the generic record Box[T], called with
//     a dot (a method's copy is a plain function of its receiver);
//   - an instance used as a value, twist[int, string].
//
// The statements call them at random type arguments drawn from int,
// string and the record Card, so one function has instances of other Go
// types at one call site shape (Pair[int, string] and Pair[string, int]).
// The program runs under the new lowering and under the reference
// (genericLowering "stencil"); stdout must equal the model's, a Go fold
// of the same statements, under both. A failing seed prints the Ergo
// source, both outputs and its replay command. The fixed seeds reach
// every path in monoPaths; the new lowering makes copies and the
// reference makes none.
//
// Mutants it kills (testdata/mutants.json): mono-shares-unequal-bodies,
// mono-wrong-argument-in-nested-instance, mono-method-copy-drops-receiver.

var monoSeed = flag.Uint64("mono.seed", 0, "run TestGenericLoweringsAgree on this seed alone")

// monoPaths are the generator's branches; the fixed seeds reach each.
var monoPaths = []string{
	"twist-int", "twist-string", "twist-Card", "both-Card", "both-Tag", "pairUp", "big-int", "big-string",
	"total-int", "total-int64", "depth-int", "depth-string", "method-int", "method-string", "value",
}

const monoPrelude = `// law-exempt: the property's operation type.
type Show {
	Text() string,
}

type Card { N int }

type Tag { S string }

func Text(c Card) string { "c\(c.N)" }

func Text(t Tag) string { "t" + t.S }

type Pair[A, B] { a A, b B }

type Box[T] { items []T, n int }

func mk[A, B](a A, b B) Pair[A, B] { Pair[A, B]{a: a, b: b} }

func swap[A, B](p Pair[A, B]) Pair[B, A] { Pair[B, A]{a: p.b, b: p.a} }

func twist[A, B](a A, b B, k int) A {
	let mut p = mk(a, b)
	for _ in 0..k {
		p = swap(swap(p))
	}
	p.a
}

func both[T Show](x T, y T, k int) string {
	let mut s = ""
	for _ in 0..k {
		s = s + x.Text() + "|" + y.Text() + ";"
	}
	s
}

func pairUp[T Show](x T, k int) string {
	let mut s = "<"
	for _ in 0..k {
		s = s + both(x, x, 1)
	}
	s + ">"
}

func big[T ordered](xs []T) T {
	let mut m = xs[0]
	for x in xs {
		if x > m {
			m = x
		}
	}
	m
}

func total[T number](xs []T) T {
	let mut s = xs[0] - xs[0]
	for x in xs {
		s = s + x
	}
	s
}

func depth[T](b Box[T], k int) int {
	if k <= 0 {
		return len(b.items) + b.n
	}
	let mut d = 0
	for _ in b.items {
		d = d + 1
	}
	depth[T](b, k - 1) + d
}

func add[T](b *Box[T], v T, k int) {
	for _ in 0..k {
		b.items = b.items.Append(v)
	}
	b.n = b.n + k
}

func size[T](b Box[T]) int {
	let mut c = 0
	for _ in b.items {
		c = c + 1
	}
	c * 10 + b.n
}
`

// monoGen writes one program and its model.
type monoGen struct {
	r       *rand.Rand
	b       strings.Builder
	want    strings.Builder
	reached map[string]bool
	names   int
}

func (g *monoGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *monoGen) out(format string, args ...any) {
	fmt.Fprintf(&g.want, format+"\n", args...)
}

func (g *monoGen) name(base string) string {
	g.names++
	return fmt.Sprintf("%s%d", base, g.names)
}

// value is a random value of the type kind (int, string or Card): its
// Ergo literal, the text the program prints for it and the model's text.
func (g *monoGen) value(kind string) (lit, show, text string) {
	n := g.r.IntN(50)
	switch kind {
	case "int":
		return fmt.Sprint(n), "", fmt.Sprint(n)
	case "string":
		s := string(rune('a'+n%26)) + fmt.Sprint(n)
		return fmt.Sprintf("%q", s), "", s
	}
	return fmt.Sprintf("Card{N: %d}", n), ".N", fmt.Sprint(n)
}

var monoKinds = []string{"int", "string", "Card"}

func (g *monoGen) stmt() {
	k := g.r.IntN(4)
	switch g.r.IntN(9) {
	case 0:
		ka, kb := monoKinds[g.r.IntN(3)], monoKinds[g.r.IntN(3)]
		a, show, text := g.value(ka)
		b, _, _ := g.value(kb)
		g.reached["twist-"+ka] = true
		g.line(fmt.Sprintf("Console.Println(\"\\(twist(%s, %s, %d)%s)\")", a, b, k, show))
		g.out("%s", text)
	case 1:
		if g.r.IntN(2) == 0 {
			x, y := g.r.IntN(9), g.r.IntN(9)
			g.reached["both-Card"] = true
			g.line(fmt.Sprintf("Console.Println(both(Card{N: %d}, Card{N: %d}, %d))", x, y, k))
			g.out("%s", strings.Repeat(fmt.Sprintf("c%d|c%d;", x, y), k))
		} else {
			x, y := string(rune('a'+g.r.IntN(26))), string(rune('a'+g.r.IntN(26)))
			g.reached["both-Tag"] = true
			g.line(fmt.Sprintf("Console.Println(both(Tag{S: %q}, Tag{S: %q}, %d))", x, y, k))
			g.out("%s", strings.Repeat(fmt.Sprintf("t%s|t%s;", x, y), k))
		}
	case 2:
		x := g.r.IntN(9)
		g.reached["pairUp"] = true
		g.line(fmt.Sprintf("Console.Println(pairUp(Card{N: %d}, %d))", x, k))
		g.out("<%s>", strings.Repeat(fmt.Sprintf("c%d|c%d;", x, x), k))
	case 3:
		xs := g.ints(1 + g.r.IntN(4))
		if g.r.IntN(2) == 0 {
			g.reached["big-int"] = true
			m := xs[0]
			for _, x := range xs {
				m = max(m, x)
			}
			g.line(fmt.Sprintf("Console.Println(\"\\(big([]int{%s}))\")", joinInts(xs)))
			g.out("%d", m)
		} else {
			ss := make([]string, len(xs))
			lits := make([]string, len(xs))
			m := ""
			for i, x := range xs {
				ss[i] = fmt.Sprintf("s%d", x)
				lits[i] = fmt.Sprintf("%q", ss[i])
				if i == 0 || ss[i] > m {
					m = ss[i]
				}
			}
			g.reached["big-string"] = true
			g.line(fmt.Sprintf("Console.Println(big([]string{%s}))", strings.Join(lits, ", ")))
			g.out("%s", m)
		}
	case 4:
		xs := g.ints(1 + g.r.IntN(4))
		s := 0
		for _, x := range xs {
			s += x
		}
		typ := []string{"int", "int64"}[g.r.IntN(2)]
		g.reached["total-"+typ] = true
		g.line(fmt.Sprintf("Console.Println(\"\\(total([]%s{%s}))\")", typ, joinInts(xs)))
		g.out("%d", s)
	case 5:
		n, extra := g.r.IntN(4), g.r.IntN(5)
		kind := []string{"int", "string"}[g.r.IntN(2)]
		g.reached["depth-"+kind] = true
		items := make([]string, n)
		for i := range items {
			items[i], _, _ = g.value(kind)
		}
		g.line(fmt.Sprintf("Console.Println(\"\\(depth(Box[%s]{items: []%s{%s}, n: %d}, %d))\")", kind, kind, strings.Join(items, ", "), extra, k))
		g.out("%d", n+extra+k*n)
	case 6, 7:
		kind := []string{"int", "string"}[g.r.IntN(2)]
		g.reached["method-"+kind] = true
		b := g.name("b")
		v, _, _ := g.value(kind)
		g.line(fmt.Sprintf("let mut %s = Box[%s]{items: []%s{}, n: 0}", b, kind, kind))
		g.line(fmt.Sprintf("%s.add(%s, %d)", b, v, k))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s.size())\")", b))
		g.out("%d", k*10+k)
	default:
		f := g.name("f")
		a, b := g.r.IntN(50), g.r.IntN(50)
		g.reached["value"] = true
		g.line(fmt.Sprintf("let %s = twist[int, string]", f))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s(%d, \"v%d\", %d))\")", f, a, b, k))
		g.out("%d", a)
	}
}

func (g *monoGen) ints(n int) []int {
	xs := make([]int, n)
	for i := range xs {
		xs[i] = g.r.IntN(100) - 30
	}
	return xs
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}

// monoProgram is the program of seed and the model's stdout.
func monoProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &monoGen{r: rand.New(rand.NewPCG(seed, 0x4d4f4e4f)), reached: reached}
	for range 16 {
		g.stmt()
	}
	return monoPrelude + "\nfunc main() needs Console {\n" + g.b.String() + "}\n", g.want.String()
}

// lowerMonoMode lowers src under the new lowering, or under the reference
// (ref: genericLowering "stencil"), and returns the copies it made.
func lowerMonoMode(t *testing.T, name, src string, ref bool) (*lowered, int) {
	t.Helper()
	saved, savedHook := genericLowering, statsHook
	defer func() { genericLowering, statsHook = saved, savedHook }()
	genericLowering = ""
	if ref {
		genericLowering = "stencil"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats.Taken["mono-instance"]
}

// TestGenericLoweringsAgree is the seeded property (see above).
//
// It builds Go because the question is what each copy computes when it
// runs: go/types says only that the copies compile, not that a copy reads
// the fields, calls the witness and recurses as the stenciled function
// does.
func TestGenericLoweringsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *monoSeed != 0 {
		seeds = []uint64{*monoSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	copies, refCopies := 0, 0
	for _, seed := range seeds {
		src, want := monoProgram(seed, reached)
		runs = append(runs, run{seed, src, want})
		p, n := lowerMonoMode(t, fmt.Sprintf("mono%d", seed), src, false)
		pr, nr := lowerMonoMode(t, fmt.Sprintf("mono%dref", seed), src, true)
		copies, refCopies = copies+n, refCopies+nr
		programs = append(programs, p, pr)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the generic programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		name := fmt.Sprintf("mono%d", r.seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != r.want || refOut != r.want || code != 0 || refCode != 0 {
			t.Errorf("seed %d: the lowerings and the model differ\nReplay: go test -run '^TestGenericLoweringsAgree$' ./internal/sketch/lower -args -mono.seed=%d\n%s\n--- monomorphic (exit %d)\n%s%s\n--- stenciled (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, refCode, refOut, refErr, r.want)
		}
	}
	t.Logf("seeds %v: copies %d, reference copies %d", seeds, copies, refCopies)
	if *monoSeed != 0 {
		return
	}
	for _, path := range monoPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if copies == 0 {
		t.Error("the new lowering made no copy")
	}
	if refCopies != 0 {
		t.Errorf("the reference lowering made %d copies", refCopies)
	}
}
