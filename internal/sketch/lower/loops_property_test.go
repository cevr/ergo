package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

var loopSeed = flag.Uint64("loops.seed", 0, "run TestLoopIdiomsAgree on this seed alone")

// loopGen writes the cases of one TestLoopIdiomsAgree program. Each case
// is a function caseK(g *Grid, …) whose body is one counted loop, called
// on a fresh Grid; main prints the Grid after each. The generator keeps
// every index in bounds, by its own model of the loop, except in the last
// case of a defect program, which raises the loop's defect.
type loopGen struct {
	r *rand.Rand
	// lens are the lengths of the Grid's nums, more and cells; arr has 6.
	nums, more, cells int
	funcs, calls      strings.Builder
	cases             int
	// idioms is the cases the lowering should write as a call.
	idioms  int
	reached map[string]bool
	// fills counts the fill cases written.
	fills int
}

// The offsets an index takes, as written around the loop variable i and
// the offset parameter p.
var loopOffsets = []string{"i", "i + %s", "%s + i", "i - %s"}

// index is the index text of form f with offset parameter p, and its
// value at i for the offset value v.
func loopIndex(f int, p string) (string, func(i, v int) int) {
	text := loopOffsets[f]
	if strings.Contains(text, "%s") {
		text = fmt.Sprintf(text, p)
	}
	return text, func(i, v int) int {
		switch f {
		case 0:
			return i
		case 3:
			return i - v
		}
		return i + v
	}
}

// inBounds reports that idx(i, v) is in [0, n) for every i in [lo, hi).
func inBounds(lo, hi, v, n int, idx func(i, v int) int) bool {
	for i := lo; i < hi; i++ {
		if k := idx(i, v); k < 0 || k >= n {
			return false
		}
	}
	return true
}

// bounds draws lo, hi and the offset of a loop whose indices fit in n
// (or, with defect, do not), as idx computes them. One draw in eight is
// an empty range.
func (g *loopGen) bounds(n int, idx func(i, v int) int, defect bool) (lo, hi, v int) {
	for {
		lo, hi, v = g.r.IntN(n+3)-1, g.r.IntN(n+3)-1, g.r.IntN(5)-2
		if g.r.IntN(8) != 0 && hi <= lo {
			continue
		}
		if inBounds(lo, hi, v, n, idx) != defect {
			return
		}
	}
}

// add writes a case: its function, and main's call of it with args.
func (g *loopGen) add(fn string, args []int, idiom bool, keys ...string) {
	name := fmt.Sprintf("case%d", g.cases)
	g.cases++
	g.funcs.WriteString(strings.Replace(fn, "CASE", name, 1) + "\n")
	var list []string
	for _, a := range args {
		list = append(list, fmt.Sprint(a))
	}
	fmt.Fprintf(&g.calls, "\tg = fresh()\n\t%s(&g, %s)\n\tConsole.Println(\"%s \" + show(&g))\n", name, strings.Join(list, ", "), name)
	if idiom {
		g.idioms++
	}
	for _, k := range keys {
		g.reached[k] = true
	}
}

// fill writes `PLACE[idx] = value` over a nums or cells place.
func (g *loopGen) fill(defect bool) {
	f := g.r.IntN(len(loopOffsets))
	idx, at := loopIndex(f, "c")
	cells := g.r.IntN(2) == 0
	n, place, elem := g.nums, "g.nums", "int"
	values := []string{"v", "7", "seven", "v + c", "i", "v - v"}
	if cells {
		n, place, elem = g.cells, "g.cells", "cell"
		values = []string{"Cell{a: v, b: c}", "blank", "alias", "Cell{a: v - v, b: c - c}", "empty"}
	}
	// The values take turns, so the fixed seeds write each.
	g.fills++
	value := values[(g.fills+g.r.IntN(2))%len(values)]
	if defect && (value == "alias" || value == "i") {
		value = values[0] // the defect case is an idiom
	}
	label := value
	local := !cells && g.r.IntN(3) == 0
	lo, hi, c := g.bounds(n, at, defect)
	for defect && !(lo < hi && at(hi-1, c) >= n) {
		// The defect case runs past the end, where the idiom's guard
		// tests the length.
		lo, hi, c = g.bounds(n, at, defect)
	}
	pre, post := "", ""
	if local {
		pre, post, place = "\tlet mut xs = g.nums\n", "\tg.nums = xs\n", "xs"
	}
	if value == "alias" {
		// p points into the cells the loop writes: a store to cell 0
		// changes what p reads after it, so the loop stays a loop.
		pre += "\tlet p = &g.cells[0]\n"
		value = "Cell{a: p.b, b: p.a + v}"
		if !defect {
			lo, hi, c, f = 0, 1+g.r.IntN(n), 0, 0
			idx, _ = loopIndex(0, "c")
		}
	}
	fn := fmt.Sprintf("func CASE(g *Grid, lo int, hi int, c int, v int) {\n%s\tfor i in lo..hi {\n\t\t%s[%s] = %s\n\t}\n%s}\n", pre, place, idx, value, post)
	idiom := !strings.Contains(value, "p.") && value != "i"
	keys := []string{"fill " + elem, "offset " + loopOffsets[f], fmt.Sprintf("fill local %v", local), "fill value " + label}
	if lo >= hi {
		keys = append(keys, "empty")
	}
	g.add(fn, []int{lo, hi, c, 1 + g.r.IntN(9)}, idiom, keys...)
}

// move writes `g.nums[idx d] = SRC[idx s]`, SRC g.nums (a move) or g.more
// (no move: two slices).
func (g *loopGen) move(defect bool) {
	fd, fs := g.r.IntN(len(loopOffsets)), g.r.IntN(len(loopOffsets))
	di, dat := loopIndex(fd, "d")
	si, sat := loopIndex(fs, "s")
	other := g.r.IntN(5) == 0
	src, sn := "g.nums", g.nums
	if other {
		src, sn = "g.more", g.more
	}
	var lo, hi, d, s int
	for {
		lo, hi, d = g.bounds(g.nums, dat, false)
		s = g.r.IntN(5) - 2
		if defect != (inBounds(lo, hi, d, g.nums, dat) && inBounds(lo, hi, s, sn, sat)) {
			break
		}
	}
	fn := fmt.Sprintf("func CASE(g *Grid, lo int, hi int, d int, s int) {\n\tfor i in lo..hi {\n\t\tg.nums[%s] = %s[%s]\n\t}\n}\n", di, src, si)
	keys := []string{"offset " + loopOffsets[fd], "offset " + loopOffsets[fs]}
	switch {
	case other:
		keys = append(keys, "move other")
	case lo >= hi:
		keys = append(keys, "empty")
	case !defect:
		a, b := dat(lo, d), sat(lo, s)
		if a > b && a < sat(hi, s) {
			keys = append(keys, "move forward overlap")
		} else {
			keys = append(keys, "move memmove")
		}
	}
	g.add(fn, []int{lo, hi, d, s}, !other, keys...)
}

// collect writes `out = out.Append(SRC[idx])`, SRC g.more, g.arr or out.
func (g *loopGen) collect(defect bool) {
	f := g.r.IntN(len(loopOffsets))
	idx, at := loopIndex(f, "c")
	srcs := []string{"g.more", "g.arr", "out"}
	src := srcs[g.r.IntN(len(srcs))]
	if defect && src == "out" {
		// out grows as the loop appends, so an index past its first
		// length may be in range when the loop reads it.
		src = "g.more"
	}
	n := map[string]int{"g.more": g.more, "g.arr": 6, "out": 2}[src]
	lo, hi, c := g.bounds(n, at, defect)
	fn := fmt.Sprintf("func CASE(g *Grid, lo int, hi int, c int) {\n\tlet mut out = []int{100, 101}\n\tfor i in lo..hi {\n\t\tout = out.Append(%s[%s])\n\t}\n\tg.nums = out\n}\n", src, idx)
	keys := []string{"collect " + src, "offset " + loopOffsets[f]}
	if lo >= hi {
		keys = append(keys, "empty")
	}
	g.add(fn, []int{lo, hi, c}, true, keys...)
}

// loopProgram is the program of seed: loopCases cases, and with defect a
// last case whose loop raises a defect.
func loopProgram(seed uint64, reached map[string]bool) (src string, idioms int, defect bool) {
	g := &loopGen{r: rand.New(rand.NewPCG(seed, 0x6c6f6f70)), reached: reached}
	g.nums, g.more, g.cells = 5+g.r.IntN(5), 5+g.r.IntN(5), 3+g.r.IntN(4)
	kinds := []func(bool){g.fill, g.move, g.collect}
	for range loopCases {
		kinds[g.r.IntN(len(kinds))](false)
	}
	defect = seed%2 == 0
	if defect {
		k := int(seed/2) % len(kinds)
		kinds[k](true)
		reached[[]string{"defect fill", "defect move", "defect collect"}[k]] = true
	}
	seq := func(n, base int) string {
		var parts []string
		for i := range n {
			parts = append(parts, fmt.Sprint(base+i))
		}
		return strings.Join(parts, ", ")
	}
	var cells []string
	for i := range g.cells {
		cells = append(cells, fmt.Sprintf("Cell{a: %d, b: %d}", i, 10+i))
	}
	var b strings.Builder
	b.WriteString("type Cell { a int, b int }\n\ntype Grid { nums []int, more []int, cells []Cell, arr [6]int }\n\n")
	b.WriteString("let seven = 7\n\nlet blank = Cell{a: -1, b: -2}\n\nlet empty = Cell{a: 0, b: 0}\n\n")
	fmt.Fprintf(&b, "func fresh() Grid {\n\tGrid{nums: []int{%s}, more: []int{%s}, cells: []Cell{%s}, arr: [6]int{%s}}\n}\n\n", seq(g.nums, 1), seq(g.more, 20), strings.Join(cells, ", "), seq(6, 40))
	b.WriteString("func show(g *Grid) string {\n\tlet mut s = \"\"\n\tfor x in g.nums {\n\t\ts = s + \"\\(x),\"\n\t}\n\tfor x in g.cells {\n\t\ts = s + \" \\(x.a)/\\(x.b)\"\n\t}\n\ts\n}\n\n")
	b.WriteString(g.funcs.String())
	b.WriteString("func main() needs Console {\n\tlet mut g = fresh()\n" + g.calls.String() + "}\n")
	return b.String(), g.idioms, defect
}

// loopCases is the cases of one program before its defect case.
const loopCases = 16

// TestLoopIdiomsAgree is the seeded property of the loop idioms
// (loops.go): each seed writes a program of fills, moves and collects
// with every offset form, empty ranges, zero values (a clear), forward overlaps, a value read
// through a pointer into the written cells, sources that are another
// slice, an array or the slice appended to, and on even seeds a last loop
// that raises a defect. It lowers the program with the idioms and with
// the reference (loopLowering "plain"); the idiom build writes each
// eligible loop as a call (knownStats.Loops) and the reference none, and
// the two programs print the same and exit alike. A failure prints the
// seed, the Ergo source and both outputs.
// Replay: go test -run '^TestLoopIdiomsAgree$' ./internal/sketch/lower -args -loops.seed=N
//
// It builds Go because the question is the behaviour of the built loops
// and calls at run time (bounds, overlap, defects); no in-process check
// observes it, and Ergo has no reference interpreter yet (test-speed T8).
func TestLoopIdiomsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *loopSeed != 0 {
		seeds = []uint64{*loopSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, idioms, defect := loopProgram(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "plain"} {
			var loops int
			saved, savedHook := loopLowering, statsHook
			loopLowering = mode
			statsHook = func(s knownStats) { loops = s.Loops }
			p := mustLower(t, fmt.Sprintf("loops%d%s", seed, mode), src)
			loopLowering, statsHook = saved, savedHook
			want := idioms
			if mode == "plain" {
				want = 0
			}
			if loops != want {
				t.Errorf("seed %d, lowering %q: %d loops written as a call, want %d\n%s", seed, mode, loops, want, src)
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the loop programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("loops%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("loops%dplain", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Count(on, "\n") == loopCases
		if on != off || onErr != offErr || onCode != offCode || !ended {
			t.Errorf("seed %d: the idioms and the reference differ (or the defect case did not end the program)\nReplay: go test -run '^TestLoopIdiomsAgree$' ./internal/sketch/lower -args -loops.seed=%d\n%s\n--- idioms (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *loopSeed == 0 {
		want := []string{"fill int", "fill cell", "fill local true", "fill local false", "fill value v", "fill value 7", "fill value seven", "fill value v + c", "fill value i", "fill value Cell{a: v, b: c}", "fill value blank", "fill value alias", "fill value v - v", "fill value Cell{a: v - v, b: c - c}", "fill value empty",
			"move memmove", "move forward overlap", "move other", "collect g.more", "collect g.arr", "collect out", "empty", "defect fill", "defect move", "defect collect"}
		for _, f := range loopOffsets {
			want = append(want, "offset "+f)
		}
		var missing []string
		for _, k := range want {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		slices.Sort(missing)
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v; reached %v", missing, reached)
		}
	}
}
