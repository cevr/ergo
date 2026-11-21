package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var foldSeed = flag.Uint64("fold.seed", 0, "run TestKnownConstructorsAgree on this seed alone")

// foldPrelude is every TestKnownConstructorsAgree program's union U (a
// flat union, under Go's register bound so a call may carry it), its
// error Bad, and far, a recursive function the inliner never inlines:
// a return of far's value is no constructor (the general path).
const foldPrelude = `type U = A{n int} | B{s string} | C{} | D{n int}

type Bad { at int }

func far(x int) U {
	if x <= 0 {
		return C
	}
	if x % 3 == 0 {
		return D{n: x}
	}
	far(x - 2)
}

func show(u U) int {
	match u {
	case A as a:
		a.n
	case B as b:
		len(b.s) * 10
	case C:
		-1
	case D as d:
		d.n * 100
	}
}

`

// foldGen writes one TestKnownConstructorsAgree program: functions gJ
// returning U or Option[int] from small bodies (constructors, a value of
// far, a parameter), some fallible, and main's matches on their calls
// with arms that bind, take several variants, take the rest, yield a
// value, break or continue a loop, or return.
type foldGen struct {
	r       *rand.Rand
	b       strings.Builder
	main    strings.Builder
	kinds   []string // by function: "U", "opt" or "fallible"
	reached map[string]bool
}

// ctor is a constructor of U from x.
func (g *foldGen) ctor() string {
	switch g.r.IntN(4) {
	case 0:
		return fmt.Sprintf("A{n: x + %d}", g.r.IntN(5))
	case 1:
		return fmt.Sprintf("B{s: \"b\\(x)%s\"}", strings.Repeat("y", g.r.IntN(3)))
	case 2:
		return "C"
	}
	return fmt.Sprintf("D{n: x * %d}", 1+g.r.IntN(3))
}

// function writes gJ.
func (g *foldGen) function(j int) {
	kind := []string{"U", "U", "U", "opt", "fallible"}[g.r.IntN(5)]
	if j == 0 {
		kind = "U" // main's first use is a loop of folded matches
	}
	g.kinds = append(g.kinds, kind)
	switch kind {
	case "opt":
		fmt.Fprintf(&g.b, "func g%d(x int, u U) Option[int] {\n\tif x > %d {\n\t\treturn Some{x * 2}\n\t}\n\tNone\n}\n\n", j, g.r.IntN(6))
		g.reached["Option subject"] = true
		return
	case "fallible":
		fmt.Fprintf(&g.b, "func g%d(x int, u U) U fails Bad {\n\tif x == %d {\n\t\treturn Bad{at: x}\n\t}\n\t%s\n}\n\n", j, g.r.IntN(6), g.ctor())
		g.reached["fallible callee"] = true
		return
	}
	fmt.Fprintf(&g.b, "func g%d(x int, u U) U {\n", j)
	for range g.r.IntN(3) {
		fmt.Fprintf(&g.b, "\tif x %s %d {\n\t\treturn %s\n\t}\n", []string{"<", "==", ">"}[g.r.IntN(3)], g.r.IntN(8), g.ctor())
	}
	switch g.r.IntN(5) {
	case 0:
		g.b.WriteString("\tfar(x)\n")
		g.reached["general return"] = true
	case 1:
		g.b.WriteString("\tif x % 2 == 0 {\n\t\treturn u\n\t}\n\t" + g.ctor() + "\n")
		g.reached["general return"] = true
	default:
		g.b.WriteString("\t" + g.ctor() + "\n")
	}
	g.b.WriteString("}\n\n")
}

// arms are the arms of a statement match on U that adds to acc, in a loop
// when loop holds (an arm may break or continue it).
func (g *foldGen) arms(loop bool) string {
	var b strings.Builder
	ctl := func() string {
		if !loop {
			return ""
		}
		switch g.r.IntN(5) {
		case 0:
			g.reached["arm breaks"] = true
			return "\t\tif acc > " + fmt.Sprint(20+g.r.IntN(200)) + " {\n\t\t\tbreak\n\t\t}\n"
		case 1:
			g.reached["arm continues"] = true
			return "\t\tif acc % 2 == 0 {\n\t\t\tcontinue\n\t\t}\n"
		}
		return ""
	}
	switch g.r.IntN(3) {
	case 0:
		// Every variant, each bound.
		b.WriteString("\tcase A as a:\n\t\tacc = acc + a.n\n" + ctl() + "\tcase B as v:\n\t\tacc = acc + len(v.s)\n" + ctl() + "\tcase C:\n\t\tacc = acc + 7\n" + ctl() + "\tcase D as d:\n\t\tacc = acc * 3 + d.n\n" + ctl())
		g.reached["binding arm"] = true
	case 1:
		// Two variants in one arm, the rest by a wildcard.
		b.WriteString("\tcase A, D:\n\t\tacc = acc + 11\n" + ctl() + "\tcase _:\n\t\tacc = acc + 5\n" + ctl())
		g.reached["several variants in an arm"], g.reached["wildcard arm"] = true, true
	default:
		b.WriteString("\tcase B as v:\n\t\tacc = acc + len(v.s) * 2\n" + ctl() + "\tcase _:\n\t\tacc = acc - 1\n" + ctl())
		g.reached["binding arm"], g.reached["wildcard arm"] = true, true
	}
	return b.String()
}

// use writes main's use of gJ: a loop of statement matches, or a function
// hJ whose tail match yields a value (or returns from an arm), called in
// a loop.
func (g *foldGen) use(j int) {
	call := fmt.Sprintf("g%d(k, D{n: k})", j)
	switch g.kinds[j] {
	case "opt":
		fmt.Fprintf(&g.main, "\tfor k in 0..9 {\n\t\tmatch %s {\n\t\tcase Some[int] as s:\n\t\t\tacc = acc * 7 %% 100003 + s.Value\n\t\tcase None:\n\t\t\tacc = acc + 1\n\t\t}\n\t}\n", call)
		return
	case "fallible":
		fmt.Fprintf(&g.b, "func h%d(k int) int fails Bad {\n\tmatch try %s {\n\tcase A as a:\n\t\ta.n\n\tcase _:\n\t\t3\n\t}\n}\n\n", j, call)
		fmt.Fprintf(&g.main, "\tfor k in 0..9 {\n\t\tlet r = h%d(k) catch {\n\t\tcase Bad as e:\n\t\t\t-e.at\n\t\t}\n\t\tacc = acc * 7 %% 100003 + r\n\t}\n", j)
		return
	}
	form := g.r.IntN(3)
	if j == 0 {
		form = 0
	}
	switch form {
	case 0:
		fmt.Fprintf(&g.main, "\tfor k in 0..9 {\n\tmatch %s {\n%s\t}\n\tacc = acc %% 100003 + 1\n\t}\n", call, strings.ReplaceAll(g.arms(true), "\n\t", "\n\t\t"))
		g.reached["statement match in a loop"] = true
	case 1:
		inner := fmt.Sprintf("\tmatch %s {\n%s\t}\n", call, g.arms(false))
		if g.r.IntN(2) == 0 {
			// The folded match ends an arm of a match Go writes as a
			// switch: its end label comes just before the next case.
			inner = "\tmatch far(k) {\n\tcase C:\n" + inner + "\tcase _:\n\t\tacc = acc + 2\n\t}\n"
			g.reached["folded match ends a case"] = true
		}
		fmt.Fprintf(&g.b, "func h%d(k int) int {\n\tlet mut acc = k\n%s\tacc\n}\n\n", j, inner)
		fmt.Fprintf(&g.main, "\tfor k in 0..9 {\n\t\tacc = acc * 7 %% 100003 + h%d(k)\n\t}\n", j)
		g.reached["statement match"] = true
	default:
		fmt.Fprintf(&g.b, "func h%d(k int) int {\n\tmatch %s {\n\tcase A as a:\n\t\tif a.n > 5 {\n\t\t\treturn a.n * 2\n\t\t}\n\t\ta.n\n\tcase C:\n\t\t-3\n\tcase _ :\n\t\tshow(%s)\n\t}\n}\n\n", j, call, call)
		fmt.Fprintf(&g.main, "\tfor k in 0..9 {\n\t\tacc = acc * 7 %% 100003 + h%d(k)\n\t}\n", j)
		g.reached["arm yields a value"], g.reached["arm returns"] = true, true
	}
}

// foldProgram is the program of seed and whether it ends in a defect.
func foldProgram(seed uint64, reached map[string]bool) (src string, defect bool) {
	g := &foldGen{r: rand.New(rand.NewPCG(seed, 0x666f6c64)), reached: reached}
	n := 5 + g.r.IntN(4)
	for j := range n {
		g.function(j)
	}
	g.main.WriteString("func main() needs Console {\n\tlet mut acc = 0\n")
	for j := range n {
		g.use(j)
		fmt.Fprintf(&g.main, "\tConsole.Println(\"g%d \\(acc)\")\n", j)
	}
	defect = seed%2 == 0
	if defect {
		// A defect inside a folded arm.
		g.b.WriteString("func pick(x int) U {\n\tif x > 2 {\n\t\treturn A{n: x}\n\t}\n\tC\n}\n\nfunc boom(k int) int {\n\tlet z = []int{1}\n\tmatch pick(k) {\n\tcase A as a:\n\t\tz[a.n]\n\tcase _:\n\t\tz[k]\n\t}\n}\n\n")
		g.main.WriteString("\tConsole.Println(\"boom \\(boom(0))\")\n\tConsole.Println(\"boom \\(boom(4))\")\n")
		reached["defect"] = true
	}
	g.main.WriteString("}\n")
	return foldPrelude + g.b.String() + g.main.String(), defect
}

// TestKnownConstructorsAgree is the seeded property of case of the known
// constructor across an inlined call (knownmatch.go): each seed writes
// functions returning a flat union U or an Option[int] from small bodies
// (constructors, a recursive function's value, a parameter), some
// fallible, and matches on their calls whose arms bind, take several
// variants, take the rest, yield, break, continue or return; even seeds
// end in a defect raised inside a folded arm. The lowering folds some
// match of every seed, and takes the tag switch for a general return in
// some (knownStats.Folded, FoldedGeneral), the reference (foldLowering
// "off") none, and the two programs print the same, exit alike and
// report a defect alike. A failure prints the seed, the Ergo source and
// both outputs.
// Replay: go test -run '^TestKnownConstructorsAgree$' ./internal/sketch/lower -args -fold.seed=N
//
// It builds Go because the question is where the built jumps go at run
// time (each variant to its arm, the loop controls, the defect); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestKnownConstructorsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *foldSeed != 0 {
		seeds = []uint64{*foldSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	general := 0
	for _, seed := range seeds {
		src, defect := foldProgram(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "off"} {
			var stats knownStats
			saved, savedHook := foldLowering, statsHook
			foldLowering = mode
			statsHook = func(s knownStats) { stats = s }
			p := mustLower(t, fmt.Sprintf("fold%d%s", seed, mode), src)
			foldLowering, statsHook = saved, savedHook
			switch {
			case mode == "off" && stats.Folded != 0:
				t.Errorf("seed %d: the reference folds %d matches", seed, stats.Folded)
			case mode == "" && stats.Folded == 0:
				t.Errorf("seed %d: no match is folded\n%s", seed, src)
			}
			if mode == "" {
				general += stats.FoldedGeneral
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the fold programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("fold%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("fold%doff", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Contains(on, "g0 ")
		if on != off || !sameReport(onErr, offErr) || onCode != offCode || !ended {
			t.Errorf("seed %d: the fold and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestKnownConstructorsAgree$' ./internal/sketch/lower -args -fold.seed=%d\n%s\n--- fold (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *foldSeed == 0 {
		if general == 0 {
			t.Errorf("no folded match takes the tag switch of a general return")
		}
		var missing []string
		for _, k := range []string{"general return", "Option subject", "fallible callee", "binding arm", "several variants in an arm", "wildcard arm", "arm breaks", "arm continues", "statement match in a loop", "statement match", "arm yields a value", "arm returns", "folded match ends a case", "defect"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}
