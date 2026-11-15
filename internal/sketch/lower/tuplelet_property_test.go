package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of the tuple let (tuplelet.go). Each seed writes one
// program whose loop over 0..11 destructures, at each step, an if, a match
// over a three-variant union or a call into three names with a tuple
// pattern, and prints them. An arm yields a tuple literal, a call's tuple
// (some calls inlined, whose returns then write the names), a nested if of
// either, a block whose let comes first, or leaves with continue;
// some patterns bind _ for an element, and some arms read names that an
// earlier let bound. The program runs under the new lowering and under the
// reference (formLowering "reference"); stdout, stderr and the exit code
// must be the same. A failing seed prints the Ergo source, both outputs and
// its replay command. The fixed seeds reach every path of tupleLetPaths,
// the new lowering takes the form, and the reference never does.

var tupleLetSeed = flag.Uint64("tuplelet.seed", 0, "run TestTupleLetsAgree on this seed alone")

// tupleLetPaths are the generator's branches; the fixed seeds reach each.
var tupleLetPaths = []string{"match", "if", "literal", "call", "nested", "block", "continue", "wildcard", "outer", "direct"}

const tupleLetPrelude = `type Circle { r int }
type Rect { w int, h int }
type Dot {}
type Shape = Circle | Rect | Dot

func parts(n int) (string, int, bool) {
	if n > 6 {
		return ("big", n, true)
	}
	("small", n * 2, false)
}

func lbl(n int) string {
	if n > 3 {
		return "hi"
	}
	"lo"
}

func trio(n int) (string, int, bool) {
	if n % 4 == 0 {
		return (lbl(n), n, true)
	}
	("t", n + 1, false)
}

func twice(n int) (string, int, bool) { (lbl(n * 2), n * 2, n > 5) }

func pick(i int) Shape {
	if i % 3 == 0 {
		return Circle{r: i}
	}
	if i % 3 == 1 {
		return Rect{w: i, h: i + 1}
	}
	Dot
}

`

type tupleLetGen struct {
	r       *rand.Rand
	b       strings.Builder
	reached map[string]bool
	k       int
	outer   string // a string name an earlier let bound in this step, or ""
}

func (g *tupleLetGen) path(p string) { g.reached[p] = true }

// arm is an arm's body at the indent ind: its statements, then its tail.
func (g *tupleLetGen) arm(ind string, depth int, loop bool) string {
	n := 5
	if depth > 0 {
		n = 3 // no nesting below the first level
	}
	if !loop {
		n-- // no continue outside the loop
	}
	switch g.r.IntN(n) {
	case 0:
		g.path("literal")
		first := fmt.Sprintf("%q", []string{"p", "q", "r"}[g.r.IntN(3)])
		if g.outer != "" && g.r.IntN(2) == 0 {
			g.path("outer")
			first = g.outer
		}
		return ind + fmt.Sprintf("(%s, n + %d, n > %d)\n", first, g.r.IntN(9), g.r.IntN(12))
	case 1:
		g.path("call")
		return ind + fmt.Sprintf("%s(n + %d)\n", []string{"parts", "trio", "twice"}[g.r.IntN(3)], g.r.IntN(5))
	case 2:
		g.path("block")
		return ind + fmt.Sprintf("let q = n * %d\n", 1+g.r.IntN(4)) + ind + "(\"blk\", q, q > 10)\n"
	case 3:
		g.path("nested")
		return ind + fmt.Sprintf("if n %% 2 == %d {\n", g.r.IntN(2)) + g.arm(ind+"\t", depth+1, loop) + ind + "} else {\n" + g.arm(ind+"\t", depth+1, loop) + ind + "}\n"
	default:
		g.path("continue")
		return ind + "continue\n"
	}
}

// let writes one destructuring let of the step and its print.
func (g *tupleLetGen) let(ind string) {
	g.k++
	a, b, c := fmt.Sprintf("a%d", g.k), fmt.Sprintf("b%d", g.k), fmt.Sprintf("c%d", g.k)
	pat, show := "("+a+", "+b+", "+c+")", "\\("+a+") \\("+b+") \\("+c+")"
	if g.r.IntN(4) == 0 {
		g.path("wildcard")
		pat, show = "("+a+", _, "+c+")", "\\("+a+") \\("+c+")"
	}
	in := ind + "\t"
	var form string
	switch g.r.IntN(3) {
	case 0:
		g.path("match")
		form = "match pick(n) {\n" +
			ind + "case Circle:\n" + g.arm(in, 0, false) +
			ind + "case Rect:\n" + g.arm(in, 0, true) +
			ind + "case Dot:\n" + g.arm(in, 0, true) + ind + "}"
	case 1:
		g.path("if")
		form = fmt.Sprintf("if n > %d {\n", g.r.IntN(12)) + g.arm(in, 0, false) + ind + "} else {\n" + g.arm(in, 0, true) + ind + "}"
	default:
		g.path("direct")
		form = fmt.Sprintf("%s(n + %d)", []string{"parts", "trio", "twice"}[g.r.IntN(3)], g.r.IntN(5))
	}
	g.b.WriteString(ind + "let " + pat + " = " + form + "\n")
	g.b.WriteString(ind + "Console.Println(\"" + fmt.Sprint(g.k) + ": " + show + "\")\n")
	g.outer = a
}

func tupleLetProgram(seed uint64, reached map[string]bool) string {
	g := &tupleLetGen{r: rand.New(rand.NewPCG(seed, 31)), reached: reached}
	g.b.WriteString("func main() needs Console {\n\tfor n in []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {\n")
	for range 2 + g.r.IntN(3) {
		g.let("\t\t")
	}
	g.b.WriteString("\t}\n}\n")
	return tupleLetPrelude + g.b.String()
}

// lowerTupleLetMode lowers src under the new lowering or the reference and
// returns the counts of tuple lets the form took and of inlined calls that
// wrote a let's names.
func lowerTupleLetMode(t *testing.T, name, src string, ref bool) (*lowered, int, int) {
	t.Helper()
	p, s := lowerFormMode(t, name, src, ref)
	return p, s.TupleLets, s.TupleInlines
}

// TestTupleLetsAgree is the seeded property (see above). It also checks the
// form's promised fact in process: under the new lowering no function of
// main.go spells an anonymous struct, the tuple value the reference makes.
//
// It builds Go because the question is the run of each program under both
// lowerings; no in-process check runs the lowered Go.
func TestTupleLetsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *tupleLetSeed != 0 {
		seeds = []uint64{*tupleLetSeed}
	}
	reached := map[string]bool{}
	srcs := map[uint64]string{}
	var programs []*lowered
	newLets, refLets, newInlines := 0, 0, 0
	for _, seed := range seeds {
		src := tupleLetProgram(seed, reached)
		srcs[seed] = src
		for _, ref := range []bool{false, true} {
			name := fmt.Sprintf("tl%d", seed)
			if ref {
				name += "ref"
			}
			p, n, in := lowerTupleLetMode(t, name, src, ref)
			if ref {
				refLets += n + in
			} else {
				newLets += n
				newInlines += in
				if main := goFunc(t, p.mainGo(), "main_1"); strings.Contains(main, "struct {") {
					t.Errorf("seed %d: the new lowering still makes a tuple value:\n%s", seed, main)
				}
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the tuple let programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		name := fmt.Sprintf("tl%d", seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != refOut || stderr != refErr || code != refCode || out == "" {
			t.Errorf("seed %d: the lowerings differ\nReplay: go test -run '^TestTupleLetsAgree$' ./internal/sketch/lower -args -tuplelet.seed=%d\n%s\n--- new (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, srcs[seed], code, out, stderr, refCode, refOut, refErr)
		}
	}
	if *tupleLetSeed != 0 {
		return
	}
	for _, path := range tupleLetPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if newLets == 0 || newInlines == 0 || refLets != 0 {
		t.Errorf("tuple lets taken: new %d (inlined calls %d), reference %d; want some and none", newLets, newInlines, refLets)
	}
}
