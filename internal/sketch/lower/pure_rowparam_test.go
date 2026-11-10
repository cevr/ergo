package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// fu4b rowparam-pure: purity follows what a body does, one rule for every
// function. A row type parameter (flag.CommandUsage[C, E]) makes no
// function impure by itself; a call through a value whose row holds it, a
// call that reaches a host extern, a call of a user extern, or a call of
// an impure function does.

var pureRowSeed = flag.Uint64("purerow.seed", 0, "run TestRowParamPurityFollowsBody on this seed alone")

// pureRowSeeds are the fixed seeds; each writes one program.
var pureRowSeeds = []uint64{1, 2, 3, 4, 5, 6}

// pureRowPrelude declares a record generic over a row (its F fails E) and
// a user extern (upper). io.Host.IsTerminal takes the io.Host provider and has
// no row, and its body is a host extern: a call of it adds nothing to the
// caller's rows and is still no pure call.
const pureRowPrelude = `import bits
import io

type Bad {}

type Box[E] {
	N int,
	F func(n int) int fails E,
}

extern go strings.ToUpper func upper(s string) string

`

// pureRowFn is one generated function and the model's facts about it.
type pureRowFn struct {
	generic bool // it has a type parameter T besides the row E
	fails   bool // its row is fails E
	impure  bool
}

// pureRowGen writes functions p0, p1, … over an io.Host and a Box[E].
type pureRowGen struct {
	r     *rand.Rand
	fns   []pureRowFn
	kinds map[string]bool // statement kinds written, for the reach check
	lets  int             // the locals named so far
}

// call writes a call of p<j> with the argument a.
func (g *pureRowGen) call(j int, a string) string {
	if g.fns[j].generic {
		return fmt.Sprintf("p%d(%s, h, b, %s)", j, a, a)
	}
	return fmt.Sprintf("p%d(h, b, %s)", j, a)
}

// stmt writes one statement into f's body and updates f's facts.
func (g *pureRowGen) stmt(f *pureRowFn) string {
	switch k := g.r.IntN(7); {
	case k == 0:
		g.kinds["arith"] = true
		return "\tacc = acc * 3 + b.N\n"
	case k == 1:
		g.kinds["pure std"] = true
		return "\tacc += bits.OnesCount(uint(acc * acc))\n"
	case k == 2 && len(g.fns) > 0:
		j := g.r.IntN(len(g.fns))
		callee := g.fns[j]
		if callee.impure {
			f.impure = true
		}
		g.kinds[fmt.Sprintf("call pure %v fails %v", !callee.impure, callee.fails)] = true
		if callee.fails {
			f.fails = true
			g.lets++
			return fmt.Sprintf("\tlet r%d = try %s\n\tacc += r%d\n", g.lets, g.call(j, "acc"), g.lets)
		}
		return "\tacc += " + g.call(j, "acc") + "\n"
	case k == 3:
		g.kinds["value"] = true
		f.impure, f.fails = true, true
		g.lets++
		return fmt.Sprintf("\tlet r%d = try b.F(acc)\n\tacc += r%d\n", g.lets, g.lets)
	case k == 4:
		g.kinds["host"] = true
		f.impure = true
		return "\tif h.IsTerminal() {\n\t\tacc += 1\n\t}\n"
	case k == 5:
		g.kinds["user extern"] = true
		f.impure = true
		return "\tacc += len(upper(\"ab\"))\n"
	default:
		g.kinds["loop"] = true
		return "\tfor i in 0.." + fmt.Sprint(1+g.r.IntN(3)) + " {\n\t\tacc += i\n\t}\n"
	}
}

// fn writes the next function.
func (g *pureRowGen) fn() string {
	f := pureRowFn{generic: g.r.IntN(2) == 0}
	var body strings.Builder
	for range 1 + g.r.IntN(3) {
		body.WriteString(g.stmt(&f))
	}
	k := len(g.fns)
	g.fns = append(g.fns, f)
	tparams, params := "[E]", "h io.Host, b Box[E], n int"
	if f.generic {
		tparams, params = "[T, E]", "x T, h io.Host, b Box[E], n int"
	}
	rows := ""
	if f.fails {
		rows = " fails E"
	}
	return fmt.Sprintf("func p%d%s(%s) int%s {\n\tlet mut acc = n\n%s\tacc\n}\n\n", k, tparams, params, rows, body.String())
}

// pureRowProgram is a program of k functions and a main that calls each.
func pureRowProgram(seed uint64, k int) (string, []pureRowFn, map[string]bool) {
	g := &pureRowGen{r: rand.New(rand.NewPCG(seed, 0x50e4)), kinds: map[string]bool{}}
	var b strings.Builder
	b.WriteString(pureRowPrelude)
	for range k {
		b.WriteString(g.fn())
	}
	// main uses bits itself, so a seed with no bits call still checks.
	b.WriteString("func main() needs Console | Ambient {\n\tlet h = io.NewHost()\n\tlet b = Box[Bad]{N: 2, F: func(n int) int fails Bad { n + 1 }}\n\tConsole.Println(\"ones \\(bits.OnesCount(3))\")\n")
	for j, f := range g.fns {
		call := g.call(j, "3")
		if f.fails {
			call = "(" + call + " catch {\n\tcase Bad:\n\t\t0\n\t})"
		}
		fmt.Fprintf(&b, "\tlet v%d = %s\n\tConsole.Println(\"p%d \\(v%d)\")\n", j, call, j, j)
	}
	b.WriteString("}\n")
	return b.String(), g.fns, g.kinds
}

// TestRowParamPurityFollowsBody is the seeded property of fu4b: every
// generated function has a row type parameter E; the model says it is
// pure exactly when its body calls no value, no host extern, no user
// extern and no impure function, and each such function lowers with no
// environment while every other keeps one. The programs go through go vet
// and go build (no link); it builds Go because a caller that passes the environment to a pure
// callee, or none to an impure one, fails there; go/types alone would need
// the runtime module loaded as go build loads it. A failing seed prints
// the Ergo source.
// Replay: go test -run '^TestRowParamPurityFollowsBody$' ./internal/sketch/lower -args -purerow.seed=N
func TestRowParamPurityFollowsBody(t *testing.T) {
	seeds := pureRowSeeds
	if *pureRowSeed != 0 {
		seeds = []uint64{*pureRowSeed}
	}
	const funcs = 8
	reached := map[string]bool{}
	var programs []*lowered
	var pure, impure int
	for _, seed := range seeds {
		src, fns, kinds := pureRowProgram(seed, funcs)
		for k := range kinds {
			reached[k] = true
		}
		o := mustLower(t, fmt.Sprintf("pr%d", seed), src)
		programs = append(programs, o)
		got := envParams(t, o.mainGo())
		for j, f := range fns {
			name := fmt.Sprintf("p%d", j)
			env, ok := got[name]
			switch {
			case !ok:
				t.Errorf("seed %d: no Go function %s\nsource:\n%s", seed, name, src)
			case env != f.impure:
				t.Errorf("seed %d: %s takes the environment %v; the model says impure %v\nReplay: go test -run '^TestRowParamPurityFollowsBody$' ./internal/sketch/lower -args -purerow.seed=%d\nsource:\n%s\nGo:\n%s", seed, name, env, f.impure, seed, src, goFunc(t, o.mainGo(), name))
			}
			if f.impure {
				impure++
			} else {
				pure++
			}
		}
	}
	if *pureRowSeed == 0 {
		// The fixed seeds reach every statement kind and both verdicts.
		for _, k := range []string{"arith", "pure std", "loop", "value", "host", "user extern", "call pure true fails false", "call pure false fails false", "call pure false fails true"} {
			if !reached[k] {
				t.Errorf("no generated function has the statement kind %q (reached %v)", k, reached)
			}
		}
		if pure == 0 || impure == 0 {
			t.Errorf("pure %d, impure %d functions: the seeds must reach both verdicts", pure, impure)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	buildAll(t, programs, false) // reports a vet or build failure itself
	t.Logf("seeds %v: %d pure and %d impure functions agree with the model", seeds, pure, impure)
}
