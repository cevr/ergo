package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of the scalar locals (scalar.go). Each seed writes
// one program over a union Shape of the Go interface form (a slice field
// keeps it out of the flat form). A let mut of it is carried by a loop
// and written by a call that the rule inlines (grow, whose parameter is
// bound to the local), by a variant literal and in a match arm; a let of
// an inlined call's result is matched (a join local: grow's returns are
// variant literals, shrink's are a literal, a call's value and a typed
// failure); the local is read by a call that is not inlined (show: a
// gather), by an inlined call whose later operand assigns it (keep: the
// operand is read first), and in a return inside a loop (final). One
// seed in four makes a closure name the local, which keeps it a union
// value.
//
// The program runs under the new lowering and under the reference
// (scalarLowering "off"); stdout, standard error and the exit code must
// be the same. A failing seed prints the Ergo source, both outputs and
// its replay command. The fixed seeds reach every path in scalarPaths,
// the new lowering writes scalar locals, join locals and bound
// parameters, and the reference writes none.

var scalarSeed = flag.Uint64("scalar.seed", 0, "run TestScalarLocalsAgree on this seed alone")

// scalarPaths are the generator's branches; the fixed seeds reach each.
var scalarPaths = []string{"grow", "join", "join-try", "print", "later", "literal", "arm", "closure", "end-fail", "end-normal"}

const scalarPrelude = `type Shape = Dot{} | Seg{N int, Tag string} | Poly{Xs []int, Name string, K int}

type Bad { At int }

func grow(s Shape, k int) Shape {
	match s {
	case Dot:
		Seg{N: k, Tag: "s"}
	case Seg as g:
		if g.N > 3 {
			return Poly{Xs: []int{g.N, k}, Name: g.Tag, K: 1}
		}
		Seg{N: g.N + k, Tag: g.Tag}
	case Poly as p:
		if len(p.Xs) > 4 {
			return Dot
		}
		Poly{Xs: p.Xs.Append(k), Name: p.Name, K: p.K + 1}
	}
}

func shrink(s Shape, k int) Shape fails Bad {
	if k == 13 {
		return Bad{At: k}
	}
	match s {
	case Dot:
		Dot
	case Seg as g:
		grow(Dot, g.N)
	case Poly as p:
		Seg{N: len(p.Xs) + p.K, Tag: p.Name}
	}
}

func score(s Shape) int {
	match s {
	case Dot:
		0
	case Seg as g:
		g.N
	case Poly as p:
		len(p.Xs) * 10 + p.K
	}
}

func show(s Shape) string {
	let mut out = ""
	for i in []int{0} {
		out = match s {
		case Dot:
			"dot"
		case Seg as g:
			"seg \(g.N) \(g.Tag) \(i)"
		case Poly as p:
			"poly \(len(p.Xs)) \(p.Name) \(p.K)"
		}
	}
	out
}

func keep(s Shape, n int) int {
	score(s) + n
}

func final(xs []int) Shape {
	let mut at Shape = Dot
	for x in xs {
		at = grow(at, x)
		if x == 7 {
			return at
		}
	}
	at
}

`

// scalarProgram is the program of seed.
func scalarProgram(seed uint64, reached map[string]bool) string {
	r := rand.New(rand.NewPCG(seed, 0x5343414c))
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, "\t\t"+format+"\n", args...) }
	xs := make([]string, 6)
	fails := false
	for i := range xs {
		x := r.IntN(15)
		if seed%3 == 0 && i == 2 {
			x = 11 // shrink fails at x + i == 13
		}
		fails = fails || x+i == 13
		xs[i] = fmt.Sprint(x)
	}
	list := "[]int{" + strings.Join(xs, ", ") + "}"
	b.WriteString("func main() fails Bad needs Console {\n")
	b.WriteString("\tlet mut at Shape = Seg{N: 1, Tag: \"a\"}\n")
	fmt.Fprintf(&b, "\tfor i, x in %s {\n", list)
	tried := false
	for n := range 4 + r.IntN(4) {
		switch k := r.IntN(8); k {
		case 0:
			line("at = grow(at, x)")
			reached["grow"] = true
		case 1:
			line("let d%d = grow(at, i)", n)
			line("match d%d {", n)
			line("case Dot:")
			line("\tat = Dot")
			line("case Seg as g:")
			line("\tat = g")
			line("case Poly as p:")
			line("\tConsole.Println(show(p))")
			line("}")
			reached["join"], reached["arm"] = true, true
		case 2:
			line("let e%d = try shrink(at, x + i)", n)
			line("match e%d {", n)
			line("case Dot:")
			line("\tConsole.Println(\"e dot\")")
			line("case Seg as g:")
			line("\tConsole.Println(\"e seg \\(g.N)\")")
			line("case Poly as p:")
			line("\tConsole.Println(\"e poly \\(p.K)\")")
			line("}")
			reached["join-try"], tried = true, true
		case 3:
			line("Console.Println(\"\\(score(at)) \\(show(at))\")")
			reached["print"] = true
		case 4:
			line("Console.Println(\"\\(keep(at, if x > 6 { at = Dot; 1 } else { 0 }))\")")
			reached["later"] = true
		case 5:
			line("at = Poly{Xs: []int{x, i}, Name: \"l\", K: x}")
			reached["literal"] = true
		default:
			line("Console.Println(show(at))")
			reached["print"] = true
		}
	}
	line("Console.Println(\"\\(i) \\(score(at))\")")
	if seed%4 == 3 {
		line("let f = func() int { score(at) }")
		line("Console.Println(\"\\(f())\")")
		reached["closure"] = true
	}
	b.WriteString("\t}\n")
	b.WriteString("\tConsole.Println(show(at))\n")
	fmt.Fprintf(&b, "\tConsole.Println(show(final(%s)))\n", list)
	b.WriteString("}\n")
	// shrink fails where x + i is 13: the end is a typed failure.
	if tried && fails {
		reached["end-fail"] = true
	} else {
		reached["end-normal"] = true
	}
	return scalarPrelude + b.String()
}

// lowerScalarMode lowers src under the new lowering, or under the
// reference (ref: scalarLowering "off"), and returns the lowering's
// counts.
func lowerScalarMode(t *testing.T, name, src string, ref bool) (*lowered, knownStats) {
	t.Helper()
	saved, savedHook := scalarLowering, statsHook
	defer func() { scalarLowering, statsHook = saved, savedHook }()
	scalarLowering = ""
	if ref {
		scalarLowering = "off"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats
}

// TestScalarLocalsAgree is the seeded property (see above).
//
// It builds Go because the question is the run of each program: the
// scalar form is a different Go program, and only its run shows that it
// writes the same output, failure and exit code as the union value.
func TestScalarLocalsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *scalarSeed != 0 {
		seeds = []uint64{*scalarSeed}
	}
	reached := map[string]bool{}
	srcs := map[uint64]string{}
	var programs []*lowered
	var got, ref knownStats
	for _, seed := range seeds {
		src := scalarProgram(seed, reached)
		srcs[seed] = src
		for _, off := range []bool{false, true} {
			name := fmt.Sprintf("sc%d", seed)
			if off {
				name += "ref"
			}
			p, s := lowerScalarMode(t, name, src, off)
			if off {
				ref.Scalars += s.Scalars
				ref.Joins += s.Joins
				ref.Aliases += s.Aliases
			} else {
				got.Scalars += s.Scalars
				got.Joins += s.Joins
				got.Aliases += s.Aliases
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the scalar programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		name := fmt.Sprintf("sc%d", seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != refOut || stderr != refErr || code != refCode {
			t.Errorf("seed %d: the lowerings differ\nReplay: go test -run '^TestScalarLocalsAgree$' ./internal/sketch/lower -args -scalar.seed=%d\n%s\n--- scalar (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, srcs[seed], code, out, stderr, refCode, refOut, refErr)
		}
	}
	t.Logf("seeds %v: new %+v, reference %+v", seeds, got, ref)
	if *scalarSeed != 0 {
		return
	}
	for _, path := range scalarPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if got.Scalars == 0 || got.Joins == 0 || got.Aliases == 0 {
		t.Errorf("the new lowering wrote no scalar local, join local or bound parameter: %+v", got)
	}
	if ref.Scalars != 0 || ref.Joins != 0 || ref.Aliases != 0 {
		t.Errorf("the reference wrote scalar locals: %+v", ref)
	}
}
