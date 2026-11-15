package lower

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of Ergo-side inlining (inline.go). Each seed writes
// one program over a let mut Box[int], a Box[string] and a let mut Map
// (OPT6: an owned parameter rebound and kept live (grow), std's
// maps.Update with a named pure callback, rebound and kept, and a named
// callback that is pure (triple) or not (viaTriple)). Its statements
// call small functions the rule inlines: a fallible generic read (at)
// caught in place, a callback update whose literal is substituted
// (update, bump), nested inlined calls (twice through pick through at),
// early returns (clampOr), an operand that reads a place a later
// inlined operand writes (diff, swapIn), a let mut read by value and written through
// a borrow in one call (keep), a by-value argument read after a later
// write (sumOf), dot calls on a place (b.clampAt) and on a borrow, and a
// call the rule refuses (a loop, total). The non-generic leaves (clampAt,
// sign) are left to Go's inliner (goLeaf); the helpers whose inlining a
// path checks (raw, swapIn, diff) are generic, so they stay inlined. The program ends in one of seven
// ways: with a division by zero in a named pure callback (apply, crash), normally, with a typed failure of main (try at), with a defect
// raised by an Unreachable three inlined calls deep (deep, mustAt), with
// an index defect in an inlined body inside an exit action (guarded), or
// with an index defect in an inlined body (deep, raw).
//
// Some statements, and sometimes the ending, sit in a loop of one
// iteration: a loop path, where the size test (OPT8) inlines the bodies
// it refuses elsewhere.
//
// The program runs under the new lowering, under the rule before the
// size test (inlineCostLowering "budget") and under the reference
// (inlineLowering "call"); stdout must equal the model's, a Go fold of the
// same statements, and the exit code and standard error (the typed
// failure, or the defect with its Ergo frames) must be the same under
// both. A failing seed prints the Ergo source, both outputs and its replay
// command. The fixed seeds reach every path in inlinePaths, the new
// lowering inlines calls and substitutes literals, and the reference does
// neither.

var inlineSeed = flag.Uint64("inline.seed", 0, "run TestInlineAgrees on this seed alone")

// inlinePaths are the generator's branches; the fixed seeds reach each.
var inlinePaths = []string{
	"pick-ok", "pick-missing", "bump-ok", "bump-missing", "clamp-neg", "clamp-hi", "clamp-in",
	"twice", "sign", "sumOf", "dot-place", "dot-borrow", "total", "strs", "print", "spill", "keep",
	"own-rebind", "own-kept", "update-named-rebind", "update-named-kept", "named-cb", "impure-cb",
	"end-normal", "end-fail", "end-unreachable", "end-index", "end-literal", "end-exit", "end-named", "loop",
}

const inlinePrelude = `import maps
import scope

type Box[T] { items []T, n int }

type Missing { At int }

func at[T](b *Box[T], i int) T fails Missing {
	if i < 0 || i >= len(b.items) {
		return Missing{At: i}
	}
	b.items[i]
}

func update[T](b *Box[T], i int, f func(v *T)) fails Missing {
	if i < 0 || i >= len(b.items) {
		return Missing{At: i}
	}
	f(&b.items[i])
}

func pick(b *Box[int], i int) int {
	at(b, i) catch {
	case Missing:
		-1
	}
}

func bump(b *Box[int], i int, d int) {
	update(b, i, func(v *int) { *v = *v * 3 % 1009 + d }) catch {
	case Missing:
		b.n = b.n + 1
	}
}

func twice(b *Box[int], i int) int {
	let x = pick(b, i)
	x + pick(b, i + 1)
}

func clampAt(b *Box[int], i int) int {
	if i < 0 {
		return 0
	}
	if i >= len(b.items) {
		return b.n
	}
	b.items[i]
}

func clampOr[T](b *Box[T], i int, d T) T {
	if i < 0 {
		return d
	}
	if i >= len(b.items) {
		return d
	}
	b.items[i]
}

func sign(x int) int {
	if x < 0 {
		return -1
	} else if x > 0 {
		return 1
	}
	0
}

func sumOf[T](b Box[T], w func(x T) int) int {
	w(b.items[0]) + b.n
}

func total(b *Box[int]) int {
	let mut s = 0
	for x in b.items {
		s = s + x
	}
	s
}

func mustAt(b *Box[int], i int) int {
	at(b, i) catch {
	case Missing:
		Unreachable("no item \(i)")
	}
}

func raw[T](b *Box[T], i int) T { b.items[i] }

func deep(b *Box[int], i int, unreachable bool) int {
	if unreachable {
		return mustAt(b, i) + 1
	}
	raw(b, i) + 1
}

func poke(b *Box[int], i int, k int) {
	update(b, i, func(v *int) {
		*v = 100 / k
	}) catch {
	case Missing:
	}
}

func swapIn[T](b *Box[T], i int, v T) T {
	let old = b.items[i]
	b.items[i] = v
	old
}

func diff[T number](x T, y T) T { x - y }

func keep[T](v T, p *T, w T) T {
	*p = w
	v
}

type Probe = Hit{At int} | Lost{At int}

func probeAt[T](p Probe, b *Box[T]) int {
	match p {
	case Hit as h:
		h.At + b.n
	case Lost:
		-1
	}
}

func guarded(b *Box[int]) {
	scope.OnExit() {
		let _ = deep(b, 9, false)
	}
}

func digest(b *Box[int]) int {
	let mut d = b.n
	for x in b.items {
		d = (d * 31 + x) % 1000003
	}
	d
}

// grow owns m (values.go, V5): inlined, it takes m as passed where the
// call hands it over, and a copy where m stays live.
func grow(m Map[int, int], k int) Map[int, int] {
	let mut c = m
	c[k] = k * 2 + 1
	c
}

func bumpOpt(old Option[int]) int {
	match old {
	case Some[int] as v:
		v.Value + 7
	case None:
		300
	}
}

func triple(x int) int { x * 3 % 1009 }

// viaTriple calls a function value: not pure, so an inlined body does not
// call it directly.
func viaTriple(x int) int {
	let h = triple
	h(x)
}

func crash(x int) int { 100 / (x - x) }

func apply(b *Box[int], i int, f func(x int) int) int {
	if i < 0 || i >= len(b.items) {
		return -1
	}
	f(b.items[i])
}

func dmap(m Map[int, int]) int {
	let mut d = 0
	for k, v in m {
		d = d + k * 7 + v
	}
	d
}
`

// inlineGen writes one program and its model.
type inlineGen struct {
	r       *rand.Rand
	b       strings.Builder
	want    strings.Builder
	items   []int
	n       int
	strs    []string
	mm      map[int]int
	reached map[string]bool
	names   int
}

// dmap is the model of the program's dmap.
func dmap(m map[int]int) int {
	d := 0
	for k, v := range m {
		d += k*7 + v
	}
	return d
}

// mapStmt writes one statement over the Map mm (grow, maps.Update) or a
// named callback (apply), and runs it on the model.
func (g *inlineGen) mapStmt(which int) {
	k := g.r.IntN(5)
	switch which {
	case 0:
		g.line(fmt.Sprintf("mm = grow(mm, %d)", k))
		g.mm[k] = k*2 + 1
		g.line("Console.Println(\"\\(dmap(mm))\")")
		g.out("%d", dmap(g.mm))
		g.reached["own-rebind"] = true
	case 1:
		// The argument stays live and is written after the call.
		v := g.name("g")
		g.line(fmt.Sprintf("let %s = grow(mm, %d)", v, k))
		g.line(fmt.Sprintf("mm[%d] = 5", k+10))
		kept := maps.Clone(g.mm)
		kept[k] = k*2 + 1
		g.mm[k+10] = 5
		g.line(fmt.Sprintf("Console.Println(\"\\(dmap(%s)) \\(dmap(mm))\")", v))
		g.out("%d %d", dmap(kept), dmap(g.mm))
		g.reached["own-kept"] = true
	case 2:
		up := maps.Clone(g.mm)
		if old, ok := up[k]; ok {
			up[k] = old + 7
		} else {
			up[k] = 300
		}
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("mm = maps.Update(mm, %d, bumpOpt)", k))
			g.mm = up
			g.line("Console.Println(\"\\(dmap(mm))\")")
			g.out("%d", dmap(g.mm))
			g.reached["update-named-rebind"] = true
		} else {
			v := g.name("u")
			g.line(fmt.Sprintf("let %s = maps.Update(mm, %d, bumpOpt)", v, k))
			g.line(fmt.Sprintf("Console.Println(\"\\(dmap(%s)) \\(dmap(mm))\")", v))
			g.out("%d %d", dmap(up), dmap(g.mm))
			g.reached["update-named-kept"] = true
		}
	default:
		i := g.index()
		f, path := "triple", "named-cb"
		if g.r.IntN(2) == 0 {
			f, path = "viaTriple", "impure-cb"
		}
		g.line(fmt.Sprintf("Console.Println(\"\\(apply(&b, %d, %s))\")", i, f))
		if v, ok := g.at(i); ok {
			g.out("%d", v*3%1009)
		} else {
			g.out("-1")
		}
		g.reached[path] = true
	}
}

func (g *inlineGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *inlineGen) out(format string, args ...any) {
	fmt.Fprintf(&g.want, format+"\n", args...)
}

func (g *inlineGen) name(base string) string {
	g.names++
	return fmt.Sprintf("%s%d", base, g.names)
}

// index is an index in range or out of it, either side.
func (g *inlineGen) index() int { return []int{-1, 0, 1, 2, 3, 5}[g.r.IntN(6)] }

func (g *inlineGen) at(i int) (int, bool) {
	if i < 0 || i >= len(g.items) {
		return 0, false
	}
	return g.items[i], true
}

func (g *inlineGen) pick(i int) int {
	if v, ok := g.at(i); ok {
		return v
	}
	return -1
}

func (g *inlineGen) digest() int {
	d := g.n
	for _, x := range g.items {
		d = (d*31 + x) % 1000003
	}
	return d
}

func (g *inlineGen) stmt() {
	switch n := g.r.IntN(18); n {
	case 14, 15, 16, 17:
		g.mapStmt(n - 14)
	case 0:
		i := g.index()
		g.line(fmt.Sprintf("Console.Println(\"\\(pick(&b, %d))\")", i))
		g.out("%d", g.pick(i))
		if _, ok := g.at(i); ok {
			g.reached["pick-ok"] = true
		} else {
			g.reached["pick-missing"] = true
		}
	case 1:
		i, d := g.index(), g.r.IntN(50)
		g.line(fmt.Sprintf("bump(&b, %d, %d)", i, d))
		if _, ok := g.at(i); ok {
			g.items[i] = g.items[i]*3%1009 + d
			g.reached["bump-ok"] = true
		} else {
			g.n++
			g.reached["bump-missing"] = true
		}
	case 2:
		i := g.index()
		g.line(fmt.Sprintf("Console.Println(\"\\(clampAt(&b, %d))\")", i))
		switch v, ok := g.at(i); {
		case i < 0:
			g.out("0")
			g.reached["clamp-neg"] = true
		case !ok:
			g.out("%d", g.n)
			g.reached["clamp-hi"] = true
		default:
			g.out("%d", v)
			g.reached["clamp-in"] = true
		}
	case 3:
		i := g.index()
		v := g.name("t")
		g.line(fmt.Sprintf("let %s = twice(&b, %d)", v, i))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", v))
		g.out("%d", g.pick(i)+g.pick(i+1))
		g.reached["twice"] = true
	case 4:
		x := g.r.IntN(7) - 3
		g.line(fmt.Sprintf("Console.Println(\"\\(sign(%d) + sign(pick(&b, 0) - %d))\")", x, x))
		s := func(x int) int {
			switch {
			case x < 0:
				return -1
			case x > 0:
				return 1
			}
			return 0
		}
		g.out("%d", s(x)+s(g.pick(0)-x))
		g.reached["sign"] = true
	case 5:
		// The by-value argument is read before the write that follows.
		v, c := g.name("s"), g.r.IntN(9)+1
		g.line(fmt.Sprintf("let %s = sumOf(b, func(x int) int { x * %d })", v, c))
		want := 0
		if len(g.items) > 0 {
			want = g.items[0]*c + g.n
		}
		g.line("b.items[0] = b.items[0] + 1000")
		g.items[0] += 1000
		g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", v))
		g.out("%d", want)
		g.reached["sumOf"] = true
	case 6:
		i := g.index()
		g.line(fmt.Sprintf("Console.Println(\"\\(b.clampOr(%d, -5))\")", i))
		g.out("%d", g.clampOr(i))
		g.reached["dot-place"] = true
	case 7:
		i := g.index()
		p := g.name("p")
		g.line(fmt.Sprintf("let %s = &b", p))
		g.line(fmt.Sprintf("Console.Println(\"\\(%s.clampOr(%d, -5) + %s.clampOr(%d, -5))\")", p, i, p, i+1))
		g.out("%d", g.clampOr(i)+g.clampOr(i+1))
		g.reached["dot-borrow"] = true
	case 8:
		g.line("Console.Println(\"\\(total(&b))\")")
		t := 0
		for _, x := range g.items {
			t += x
		}
		g.out("%d", t)
		g.reached["total"] = true
	case 9:
		i := []int{0, 1, 4}[g.r.IntN(3)]
		w := g.name("w")
		g.line(fmt.Sprintf("let %s = at(&ws, %d) catch {\n\tcase Missing:\n\t\t\"none\"\n\t}", w, i))
		g.line("Console.Println(" + w + ")")
		if i < len(g.strs) {
			g.out("%s", g.strs[i])
		} else {
			g.out("none")
		}
		g.reached["strs"] = true
	case 12:
		// The first operand reads a place the second one, an inlined
		// call, writes: the operand is evaluated first, as the call's is.
		v := g.r.IntN(20)
		g.line(fmt.Sprintf("Console.Println(\"\\(diff(b.items[0], swapIn(&b, 0, %d)))\")", v))
		g.out("0")
		g.items[0] = v
		g.reached["spill"] = true
	case 13:
		// A let mut read by value and written through a borrow in one
		// call: the parameter keeps the value it had at the call.
		c, k, w := g.name("c"), g.r.IntN(20), g.r.IntN(20)+20
		g.line(fmt.Sprintf("let mut %s = %d", c, k))
		g.line(fmt.Sprintf("let k%s = keep(%s, &%s, %d)", c, c, c, w))
		g.line(fmt.Sprintf("Console.Println(\"\\(k%s) \\(%s)\")", c, c))
		g.out("%d %d", k, w)
		g.reached["keep"] = true
		// A variant operand of a union parameter: its Go type is not the
		// parameter's, so it is converted, not substituted.
		h := g.name("h")
		g.line(fmt.Sprintf("let %s = Hit{At: %d}", h, k))
		g.line(fmt.Sprintf("Console.Println(\"\\(probeAt(%s, &b))\")", h))
		g.out("%d", k+g.n)
	case 10:
		v := g.r.IntN(20)
		g.line(fmt.Sprintf("b.items = b.items.Append(%d)", v))
		g.items = append(g.items, v)
	default:
		g.line("Console.Println(\"\\(digest(&b))\")")
		g.out("%d", g.digest())
		g.reached["print"] = true
	}
}

func (g *inlineGen) clampOr(i int) int {
	if v, ok := g.at(i); ok {
		return v
	}
	return -5
}

func (g *inlineGen) clamp(i int) int {
	v, ok := g.at(i)
	switch {
	case i < 0:
		return 0
	case !ok:
		return g.n
	}
	return v
}

// inlineProgram is the program, the model's stdout and how it ends.
func inlineProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &inlineGen{r: rand.New(rand.NewPCG(seed, 0x494e4c4e)), reached: reached}
	g.items = []int{4, 7, 1}
	g.strs = []string{"a", "bc"}
	g.line("let mut b = Box[int]{items: []int{4, 7, 1}, n: 0}")
	g.line("let mut ws = Box[string]{items: []string{\"a\", \"bc\"}, n: 0}")
	g.line("ws.n = 1")
	g.line("let mut mm = Map[int, int]{}")
	g.line("mm[0] = 1")
	g.line("mm = maps.Update(mm, 0, bumpOpt)")
	g.mm = map[int]int{0: 8}
	// A separate stream puts a statement on a loop path (a loop of one
	// iteration), where the size test inlines the larger bodies (OPT8),
	// and keeps the statements each fixed seed writes.
	loops := rand.New(rand.NewPCG(seed, 0x4c4f4f50))
	for range 24 {
		if loops.IntN(3) == 0 {
			g.onLoop(g.stmt)
		} else {
			g.stmt()
		}
	}
	g.line("Console.Println(\"\\(digest(&b)) \\(dmap(mm))\")")
	g.out("%d %d", g.digest(), dmap(g.mm))
	end := func() {
		g.ending(seed)
	}
	if loops.IntN(2) == 0 {
		g.onLoop(end)
	} else {
		end()
	}
	return inlinePrelude + "func main() fails Missing needs Console {\n" + g.b.String() + "}\n", g.want.String()
}

// onLoop writes the statements of write inside a loop that runs once.
func (g *inlineGen) onLoop(write func()) {
	v := g.name("once")
	g.line("let mut " + v + " = true")
	g.line("for " + v + " {")
	g.line(v + " = false")
	write()
	g.line("}")
	g.reached["loop"] = true
}

// ending writes the end of the program.
func (g *inlineGen) ending(seed uint64) {
	switch seed % 7 { // each ending in any seven consecutive seeds
	case 0:
		g.reached["end-normal"] = true
	case 6:
		// A division by zero in a named pure function an inlined body
		// calls directly.
		g.line("Console.Println(\"\\(apply(&b, 0, crash))\")")
		g.reached["end-named"] = true
	case 5:
		// An index defect in an inlined body inside an exit action: the
		// Go closure around the action calls it at the position of the
		// inlined call, which the report writes once.
		g.line("guarded(&b)")
		g.reached["end-exit"] = true
	case 4:
		// A division by zero in a literal substituted into an inlined
		// body, itself inlined.
		g.line("poke(&b, 0, 0)")
		g.reached["end-literal"] = true
	case 1:
		g.line("let x = try at(&b, 9)")
		g.line("Console.Println(\"\\(x)\")")
		g.reached["end-fail"] = true
	case 2:
		g.line("Console.Println(\"\\(deep(&b, 9, true))\")")
		g.reached["end-unreachable"] = true
	default:
		g.line("Console.Println(\"\\(deep(&b, 9, false))\")")
		g.reached["end-index"] = true
	}
}

// lowerInlineMode lowers src under the new lowering, or under the
// reference (ref: inlineLowering "call"), and returns the lowering's
// counts.
func lowerInlineMode(t *testing.T, name, src string, ref bool) (*lowered, knownStats) {
	t.Helper()
	saved, savedHook := inlineLowering, statsHook
	defer func() { inlineLowering, statsHook = saved, savedHook }()
	inlineLowering = ""
	if ref {
		inlineLowering = "call"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats
}

// TestInlineAgrees is the seeded property (see above).
//
// It builds Go because the question is the run of each program, its
// defect report included: the report reads Go's traceback of the built
// program, which no in-process check produces.
func TestInlineAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *inlineSeed != 0 {
		seeds = []uint64{*inlineSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	// Three lowerings of each program: the rule (""), the rule before
	// OPT8's size test ("budget", inlineCostLowering) and no inlining
	// ("ref", inlineLowering "call").
	modes := []string{"", "budget", "ref"}
	stats := map[string]*knownStats{}
	for _, m := range modes {
		stats[m] = &knownStats{}
	}
	for _, seed := range seeds {
		src, want := inlineProgram(seed, reached)
		runs = append(runs, run{seed, src, want})
		for _, m := range modes {
			saved := inlineCostLowering
			if m == "budget" {
				inlineCostLowering = "budget"
			}
			p, s := lowerInlineMode(t, fmt.Sprintf("in%d%s", seed, m), src, m == "ref")
			inlineCostLowering = saved
			stats[m].Inlined += s.Inlined
			stats[m].Substituted += s.Substituted
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the inline programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		name := fmt.Sprintf("in%d", r.seed)
		out, stderr, code := runExe(t, exes[name])
		for _, m := range modes[1:] {
			refOut, refErr, refCode := runExe(t, exes[name+m])
			if out != r.want || refOut != r.want || stderr != refErr || code != refCode {
				t.Errorf("seed %d: the lowering and the %s lowering or the model differ\nReplay: go test -run '^TestInlineAgrees$' ./internal/sketch/lower -args -inline.seed=%d\n%s\n--- inlined (exit %d)\n%s%s\n--- %s (exit %d)\n%s%s\n--- model\n%s", r.seed, m, r.seed, r.src, code, out, stderr, m, refCode, refOut, refErr, r.want)
			}
		}
	}
	t.Logf("seeds %v: new %+v, budget %+v, reference %+v", seeds, *stats[""], *stats["budget"], *stats["ref"])
	if *inlineSeed != 0 {
		return
	}
	for _, path := range inlinePaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if s := stats[""]; s.Inlined == 0 || s.Substituted == 0 {
		t.Errorf("the new lowering inlined nothing: %+v", *s)
	}
	// The size test refuses sites the budget rule takes (off a loop path).
	if stats["budget"].Inlined <= stats[""].Inlined {
		t.Errorf("the size test refused no site: new %+v, budget %+v", *stats[""], *stats["budget"])
	}
	if s := stats["ref"]; s.Inlined != 0 || s.Substituted != 0 {
		t.Errorf("the reference lowering inlined: %+v", *s)
	}
}
