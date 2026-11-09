package lower

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// lw-pure-prelude (G-ex-e-5, G-ex-f-6): the prelude's Append, Get and
// Delete are pure std bodies (runtime/v1 data.go), so a function whose only
// calls are theirs, with loops and arithmetic, lowers to a Go function with
// no environment and no task poll, and its output is unchanged. The
// reference lowering, with their calls opaque, is
// check.SetPureCollectionsForTest(false).

// e5Source is the examples-wave repro e5.
const e5Source = `func evens(n int) []int {
	let mut out = []int{}
	for i in 0..n {
		if i % 2 == 0 {
			out = out.Append(i)
		}
	}
	out
}

func lookup(m Map[string, int], k string) int {
	match m.Get(k) {
	case Some[int] as s:
		s.Value
	case None:
		0
	}
}

func drop(m Map[string, int], k string) int {
	len(m.Delete(k))
}

func main() needs Console {
	let m = Map[string, int]{"a": 1, "b": 2}
	Console.Println("\(len(evens(10))) \(lookup(m, "a")) \(drop(m, "a"))")
}
`

// lowerPureCollections lowers src with the prelude's collections pure or,
// for the reference, opaque.
func lowerPureCollections(t *testing.T, name, src string, pure bool) *lowered {
	t.Helper()
	restore := check.SetPureCollectionsForTest(pure)
	defer restore()
	return mustLower(t, name, src)
}

// TestPurePreludeEmits builds Go because it checks what the built program
// prints and how it exits; no in-process check observes that, and Ergo has
// no reference interpreter yet (test-speed T8).
func TestPurePreludeEmits(t *testing.T) {
	needsBuild(t)
	// lookup's match on m.Get(k) keeps its Get call, and so the Get
	// wrapper, under the reference Get lowering (getmatch.go); the comma-ok
	// form reads the Map with no call (TestGetMatchEmits).
	saved := getLowering
	defer func() { getLowering = saved }()
	getLowering = "switch"
	marked := lowerPureCollections(t, "e5on", e5Source, true).mainGo()
	for name, head := range map[string]string{
		"evens":  "func evens(n int) []int {",
		"lookup": "func lookup(m map[string]int, k string) int {",
		"drop":   "func drop(m map[string]int, k string) int {",
	} {
		fn := goFunc(t, marked, name)
		if !strings.HasPrefix(fn, head) {
			t.Errorf("%s: want %q, got\n%s", name, head, fn)
		}
		if strings.Contains(fn, "env") || strings.Contains(fn, ".Check()") {
			t.Errorf("%s reads an environment or polls:\n%s", name, fn)
		}
	}
	for _, wrapper := range []string{"func Append[T any](xs []T, x T) []T {", "func Get[K comparable, V any](m map[K]V, k K) Option[V] {", "func Delete[K comparable, V any](m map[K]V, k K) map[K]V {"} {
		if !strings.Contains(marked, wrapper) {
			t.Errorf("no pure wrapper %q in\n%s", wrapper, marked)
		}
	}
	// The reference keeps the environment and the polls.
	ref := lowerPureCollections(t, "e5off", e5Source, false).mainGo()
	if fn := goFunc(t, ref, "evens"); !strings.HasPrefix(fn, "func evens(env *Env, n int) []int {") || strings.Count(fn, ".Check()") < 2 {
		t.Errorf("the reference evens lost its environment or its polls:\n%s", fn)
	}
	exes := buildAll(t, []*lowered{lowerPureCollections(t, "e5on", e5Source, true), lowerPureCollections(t, "e5off", e5Source, false)}, true)
	for _, name := range []string{"e5on", "e5off"} {
		if out, stderr, code := runExe(t, exes[name]); out != "5 1 1\n" || code != 0 {
			t.Errorf("%s: %q %q exit %d; want \"5 1 1\\n\"", name, out, stderr, code)
		}
	}
}

var purePreludeSeed = flag.Uint64("pureprelude.seed", 0, "run TestPurePreludeAgrees on this seed alone")

// purePreludeSeeds are the fixed seeds; each writes one program.
var purePreludeSeeds = []uint64{1, 2, 3, 4}

// purePreludeGen writes random functions over loops, the prelude's Append,
// Get and Delete, Map index writes, arithmetic and calls of the earlier
// functions, each of them pure by the rule of stdlib.md Pure bodies.
type purePreludeGen struct {
	r     *rand.Rand
	funcs int // the functions written so far, which a later one may call
}

func (g *purePreludeGen) expr(loop []string) string {
	choices := []string{"n", "acc % 7", "len(out)", "len(mm)", fmt.Sprint(g.r.IntN(5))}
	choices = append(choices, loop...)
	return choices[g.r.IntN(len(choices))]
}

func (g *purePreludeGen) stmts(ind string, depth int, loop []string) string {
	var b strings.Builder
	for range 1 + g.r.IntN(3) {
		b.WriteString(g.stmt(ind, depth, loop))
	}
	return b.String()
}

func (g *purePreludeGen) stmt(ind string, depth int, loop []string) string {
	kinds := 7
	if depth == 0 {
		kinds = 5 // no further nesting
	}
	switch k := g.r.IntN(kinds + 1); {
	case k == 0:
		return ind + "out = out.Append(" + g.expr(loop) + ")\n"
	case k == 1:
		return ind + "mm = mm.Delete(" + g.expr(loop) + ")\n"
	case k == 2:
		return ind + "match mm.Get(" + g.expr(loop) + ") {\n" + ind + "case Some[int] as s:\n" + ind + "\tacc += s.Value\n" + ind + "case None:\n" + ind + "\tacc += " + fmt.Sprint(1+g.r.IntN(3)) + "\n" + ind + "}\n"
	case k == 3:
		return ind + "mm[" + g.expr(loop) + "] = " + g.expr(loop) + "\n"
	case k == 4:
		if g.funcs > 0 {
			return ind + fmt.Sprintf("acc += p%d(%s, out, mm)\n", g.r.IntN(g.funcs), g.expr(loop))
		}
		return ind + "acc = acc * 3 + " + g.expr(loop) + "\n"
	case k == 5:
		return ind + "acc = acc * 3 + " + g.expr(loop) + "\n"
	case k == 6:
		v := fmt.Sprintf("i%d", depth)
		return ind + "for " + v + " in 0.." + fmt.Sprint(1+g.r.IntN(4)) + " {\n" + ind + "\tacc += " + v + "\n" + g.stmts(ind+"\t", depth-1, append(loop, v)) + ind + "}\n"
	default:
		return ind + "if " + g.expr(loop) + " % 2 == 0 {\n" + g.stmts(ind+"\t", depth-1, loop) + ind + "}\n"
	}
}

// fn writes the next function, p<k>.
func (g *purePreludeGen) fn() string {
	name := fmt.Sprintf("p%d", g.funcs)
	body := "\tlet mut acc = n\n\tlet mut out = xs\n\tlet mut mm = m\n\tacc += 1\n\tout = out.Append(n)\n\tmm = mm.Delete(-1)\n" + g.stmts("\t", 2, nil) + "\tacc + len(out) * 100 + len(mm) * 10000\n"
	g.funcs++
	return "func " + name + "(n int, xs []int, m Map[int, int]) int {\n" + body + "}\n"
}

// purePreludeProgram is a program of k functions and a main that calls
// each on fresh arguments and prints one line per call.
func purePreludeProgram(seed uint64, k int) string {
	g := &purePreludeGen{r: rand.New(rand.NewPCG(seed, 0x7070))}
	var b strings.Builder
	for range k {
		b.WriteString(g.fn() + "\n")
	}
	b.WriteString("func main() needs Console {\n")
	for i := range k {
		for c := range 2 {
			key := g.r.IntN(5)
			fmt.Fprintf(&b, "\tConsole.Println(\"p%d.%d \\(p%d(%d, []int{%d, %d}, Map[int, int]{%d: %d, %d: %d}))\")\n",
				i, c, i, g.r.IntN(6), g.r.IntN(5), g.r.IntN(5), key, g.r.IntN(9), key+1+g.r.IntN(3), g.r.IntN(9))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// TestPurePreludeAgrees is the seeded property of lw-pure-prelude: each
// generated program prints the same lines lowered with the prelude's
// collections pure and with the reference (their calls opaque), and in the
// pure build every generated function takes no environment and polls no
// task, while the reference's take one. A failing seed prints the Ergo
// source and both outputs.
// Replay: go test -run '^TestPurePreludeAgrees$' ./internal/sketch/lower -args -pureprelude.seed=N
//
// It builds Go because it compares the runs of built programs with a
// reference (another lowering, a fold, or Go's own package), and the
// question is behaviour at run time; no in-process check observes that, and
// Ergo has no reference interpreter yet (test-speed T8).
func TestPurePreludeAgrees(t *testing.T) {
	needsBuild(t)
	seeds := purePreludeSeeds
	if *purePreludeSeed != 0 {
		seeds = []uint64{*purePreludeSeed}
	}
	const funcs = 8
	envParam := regexp.MustCompile(`^func p\d+\(env \*Env, n int`)
	var programs []*lowered
	srcs := map[uint64]string{}
	for _, seed := range seeds {
		src := purePreludeProgram(seed, funcs)
		srcs[seed] = src
		on := lowerPureCollections(t, fmt.Sprintf("pp%don", seed), src, true)
		off := lowerPureCollections(t, fmt.Sprintf("pp%doff", seed), src, false)
		for i := range funcs {
			name := fmt.Sprintf("p%d", i)
			if fn := goFunc(t, on.mainGo(), name); !strings.HasPrefix(fn, "func "+name+"(n int, xs []int, m map[int]int) int {") || strings.Contains(fn, ".Check()") || strings.Contains(fn, "env") {
				t.Errorf("seed %d: %s is not pure in the marked build:\n%s\nsource:\n%s", seed, name, fn, src)
			}
			if fn := goFunc(t, off.mainGo(), name); !envParam.MatchString(fn) {
				t.Errorf("seed %d: the reference %s takes no environment:\n%s", seed, name, fn)
			}
		}
		programs = append(programs, on, off)
	}
	if t.Failed() {
		t.FailNow()
	}
	// The fixed seeds reach every statement kind of the generator.
	if *purePreludeSeed == 0 {
		all := strings.Join(slices.Collect(maps.Values(srcs)), "\n")
		for _, kind := range []string{"out = out.Append(", "mm = mm.Delete(", "match mm.Get(", "\tmm[", "(n int, xs []int, m Map[int, int]) int {", "acc += p", "acc = acc * 3", "\tfor i", "\tif "} {
			if !strings.Contains(all, kind) {
				t.Errorf("no generated function has %q", kind)
			}
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the programs do not build (seeds %v)", seeds)
	}
	lines := 0
	for _, seed := range seeds {
		on, _, onCode := runExe(t, exes[fmt.Sprintf("pp%don", seed)])
		off, _, offCode := runExe(t, exes[fmt.Sprintf("pp%doff", seed)])
		if on != off || onCode != 0 || offCode != 0 {
			t.Errorf("seed %d: the pure build and the reference differ (exit %d, %d).\nReplay: go test -run '^TestPurePreludeAgrees$' ./internal/sketch/lower -args -pureprelude.seed=%d\nsource:\n%s\npure:\n%s\nreference:\n%s", seed, onCode, offCode, seed, srcs[seed], on, off)
		}
		lines += strings.Count(on, "\n")
	}
	t.Logf("seeds %v: %d functions, %d lines agree", seeds, funcs*len(seeds), lines)
}
