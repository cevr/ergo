package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The seeded property of known callbacks (known.go) and result-alias
// summaries (alias.go). Each seed writes one program over three let mut
// roots: a Box[int], a Box[[]int] (a generic record whose elements are
// slices) and a Pair (nested value fields). It reads them through pure
// generic and concrete functions whose results depend on their argument
// (at, whole, firstOf, inner) or do not (count, shape), through a borrow
// (get), and with a write in a later operand (sumWith(bi, bump(&bi))),
// keeps the results and writes the roots after; it calls functions that
// take callbacks with pure literals, with literals that capture a local,
// with a function value bound to a let, and with a literal that calls one
// (both opaque), through forwarding wrappers (fwd, fwd2), with a failure
// that is caught (update's Missing), and a fold whose callback has a
// result; and it stops two tasks that loop forever through a callback,
// one pure and one opaque: an opaque callback is still interruptible, and
// a known variant keeps its loop's point.
//
// The program runs under the new lowering and under the reference
// (callbackLowering "opaque", aliasLowering "type"), and each output must
// equal the model's, a Go fold of the same statements. A failing seed
// prints the Ergo source, the output and the model, and its replay
// command. The fixed seeds must reach every path in knownPaths, and the
// counts of the lowerings (knownStats) show the rules ran: the new one
// makes known sites, variants, closed frames, shared arguments, result
// copies and borrow copies; the reference makes no known site and shares
// no argument by a summary.

var knownSeed = flag.Uint64("known.seed", 0, "run TestKnownCallbacksAgree on this seed alone")

// knownPaths are the generator's branches; the fixed seeds reach each.
var knownPaths = []string{
	"at", "whole", "firstOf", "inner", "get", "count-shape", "in-flight", "in-flight-deps",
	"write-inner", "write-elem", "write-nested", "write-replace",
	"update-ok", "update-missing", "each-pure", "each-capture", "each-opaque-let", "each-opaque-lit",
	"fwd-ok", "fwd-missing", "fwd2", "fold-pure", "fold-opaque", "closed", "via", "via2", "at2", "keep", "print",
}

const knownPrelude = `import queue
import task

type Box[T] { items []T, n int }

type Pair { a []int, b Box[int] }

type Missing { At int }

func at[T](b Box[T], i int) T { b.items[i] }

func count[T](b Box[T]) int { len(b.items) + b.n }

func shape[T](b Box[T]) Box[int] { Box[int]{items: []int{len(b.items)}, n: b.n} }

func whole[T](b Box[T]) Box[T] { b }

func firstOf(p Pair) []int { p.a }

func inner(p Pair) Box[int] { p.b }

func get[T](b *Box[T], i int) T { b.items[i] }

// via, via2 and at2 return a part of b through a let, an assignment and
// a call: each depends on b.
func via[T](b Box[T]) T {
	let x = b.items[0]
	x
}

func via2[T](b Box[T], d T) T {
	let mut x = d
	if len(b.items) > 0 {
		x = b.items[0]
	}
	x
}

func at2[T](b Box[T], i int) T { at(b, i) }

// keep returns its callback (escaping), so it has no known variant.
func keep(b *Box[int], f escaping func(v *int)) func(v *int) {
	f(&b.items[0])
	f
}

func sumWith(b Box[int], k int) int { dbi(b) + k }

func bump(b *Box[int]) int {
	b.items[0] = b.items[0] + 100
	0
}

func bumps(b *Box[[]int]) int {
	b.items[1][0] = b.items[1][0] + 100
	1
}

func update[T](b *Box[T], i int, f func(v *T)) fails Missing {
	if i >= len(b.items) {
		return Missing{At: i}
	}
	f(&b.items[i])
}

func each[T](b *Box[T], f func(v *T)) {
	for i in 0..len(b.items) {
		f(&b.items[i])
	}
}

func fold[T](b Box[T], z int, f func(acc int, v T) int) int {
	let mut acc = z
	for x in b.items {
		acc = f(acc, x)
	}
	acc
}

func fwd[T](b *Box[T], i int, f func(v *T)) {
	update(b, i, f) catch {
	case Missing:
		b.n = b.n + 1
	}
}

func fwd2[T](b *Box[T], f func(v *T)) {
	fwd(b, 0, f)
	each(b, f)
}

// bumpAll is a closed frame: opaque only through its known site.
func bumpAll(b *Box[int], c int) {
	each(b, func(v *int) { *v = *v + c })
	fwd(b, 1, func(v *int) { *v = *v * 2 % 10007 })
}

func spin[T](b *Box[T], f func(v *T)) {
	for {
		f(&b.items[0])
	}
}

func ds(s []int) int {
	let mut t = len(s) * 1000
	for i, v in s {
		t = t + (i + 1) * v
	}
	t
}

func dbi(b Box[int]) int { ds(b.items) * 3 + b.n }

func dbs(b Box[[]int]) int {
	let mut t = len(b.items) * 1000 + b.n
	for i, s in b.items {
		t = t + (i + 1) * ds(s)
	}
	t
}

func dp(p Pair) int { ds(p.a) * 5 + dbi(p.b) }

`

// The model's digests, as the prelude's.
func kds(s []int) int {
	t := len(s) * 1000
	for i, v := range s {
		t += (i + 1) * v
	}
	return t
}

type kbox struct {
	items []int
	n     int
}

type kboxs struct {
	items [][]int
	n     int
}

func (b kbox) digest() int { return kds(b.items)*3 + b.n }

func (b kboxs) digest() int {
	t := len(b.items)*1000 + b.n
	for i, s := range b.items {
		t += (i + 1) * kds(s)
	}
	return t
}

func (b kbox) clone() kbox { return kbox{slices.Clone(b.items), b.n} }

func (b kboxs) clone() kboxs {
	out := kboxs{n: b.n}
	for _, s := range b.items {
		out.items = append(out.items, slices.Clone(s))
	}
	return out
}

// knownGen writes one program and folds the model.
type knownGen struct {
	r       *rand.Rand
	b       strings.Builder
	want    strings.Builder
	reached map[string]bool
	n       int
	bi      kbox
	bs      kboxs
	pa      []int
	pb      kbox
	kept    []keptVal // values bound by a let, printed at the end
	ints    []string
	fns     []string // let-bound callbacks func(v *int)
	fnAdd   map[string]int
}

type keptVal struct {
	name   string
	digest string // the Ergo digest function
	value  func() int
}

func (g *knownGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *knownGen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *knownGen) out(format string, args ...any) { fmt.Fprintf(&g.want, format+"\n", args...) }

func (g *knownGen) small() int { return g.r.IntN(9) + 1 }

func (g *knownGen) stmt() {
	switch g.r.IntN(25) {
	case 0:
		i := g.r.IntN(2)
		v := g.name("s")
		g.line(fmt.Sprintf("let %s = at(bs, %d)", v, i))
		d := kds(g.bs.items[i])
		g.kept = append(g.kept, keptVal{v, "ds", func() int { return d }})
		g.reached["at"] = true
	case 1:
		v := g.name("w")
		g.line(fmt.Sprintf("let %s = whole(bs)", v))
		d := g.bs.digest()
		g.kept = append(g.kept, keptVal{v, "dbs", func() int { return d }})
		g.reached["whole"] = true
	case 2:
		v := g.name("f")
		g.line(fmt.Sprintf("let %s = firstOf(p)", v))
		d := kds(g.pa)
		g.kept = append(g.kept, keptVal{v, "ds", func() int { return d }})
		g.reached["firstOf"] = true
	case 3:
		v := g.name("q")
		g.line(fmt.Sprintf("let %s = inner(p)", v))
		d := g.pb.digest()
		g.kept = append(g.kept, keptVal{v, "dbi", func() int { return d }})
		g.reached["inner"] = true
	case 4:
		i := g.r.IntN(2)
		v := g.name("g")
		g.line(fmt.Sprintf("let %s = get(&bs, %d)", v, i))
		d := kds(g.bs.items[i])
		g.kept = append(g.kept, keptVal{v, "ds", func() int { return d }})
		g.reached["get"] = true
	case 5:
		v := g.name("k")
		g.line(fmt.Sprintf("let %s = count(bs) + ds(shape(bs).items)", v))
		d := len(g.bs.items) + g.bs.n + kds([]int{len(g.bs.items)})
		g.ints = append(g.ints, v)
		g.kept = append(g.kept, keptVal{v, "", func() int { return d }})
		g.reached["count-shape"] = true
	case 6:
		v := g.name("k")
		g.line(fmt.Sprintf("let %s = sumWith(bi, bump(&bi))", v))
		d := g.bi.digest()
		g.bi.items[0] += 100
		g.ints = append(g.ints, v)
		g.kept = append(g.kept, keptVal{v, "", func() int { return d }})
		g.reached["in-flight"] = true
	case 7:
		v := g.name("s")
		g.line(fmt.Sprintf("let %s = at(bs, bumps(&bs))", v))
		d := kds(g.bs.items[1])
		g.bs.items[1][0] += 100
		g.kept = append(g.kept, keptVal{v, "ds", func() int { return d }})
		g.reached["in-flight-deps"] = true
	case 8:
		i := g.r.IntN(2)
		j := g.r.IntN(len(g.bs.items[i]))
		c := g.small()
		g.line(fmt.Sprintf("bs.items[%d][%d] = %d", i, j, c))
		g.bs.items[i][j] = c
		g.reached["write-inner"] = true
	case 9:
		i := g.r.IntN(len(g.bi.items))
		c := g.small()
		g.line(fmt.Sprintf("bi.items[%d] = %d", i, c))
		g.bi.items[i] = c
		g.reached["write-elem"] = true
	case 10:
		c := g.small()
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("p.a[0] = %d", c))
			g.pa[0] = c
		} else {
			g.line(fmt.Sprintf("p.b.items[0] = %d", c))
			g.pb.items[0] = c
		}
		g.reached["write-nested"] = true
	case 11:
		i, c := g.r.IntN(2), g.small()
		g.line(fmt.Sprintf("bs.items[%d] = []int{%d, %d}", i, c, c+1))
		g.bs.items[i] = []int{c, c + 1}
		g.reached["write-replace"] = true
	case 12:
		i, c := g.r.IntN(5), g.small()
		g.line(fmt.Sprintf("update(&bi, %d, func(v *int) { *v = *v + %d }) catch {", i, c))
		g.line("case Missing as m:")
		g.line("\tConsole.Println(\"missing \\(m.At)\")")
		g.line("}")
		if i < len(g.bi.items) {
			g.bi.items[i] += c
			g.reached["update-ok"] = true
		} else {
			g.out("missing %d", i)
			g.reached["update-missing"] = true
		}
	case 13:
		c := g.small()
		g.line(fmt.Sprintf("each(&bi, func(v *int) { *v = *v * %d %% 10007 })", c))
		for i := range g.bi.items {
			g.bi.items[i] = g.bi.items[i] * c % 10007
		}
		g.reached["each-pure"] = true
	case 14:
		if len(g.ints) == 0 {
			return
		}
		k := g.ints[g.r.IntN(len(g.ints))]
		var d int
		for _, x := range g.kept {
			if x.name == k {
				d = x.value()
			}
		}
		g.line(fmt.Sprintf("each(&bi, func(v *int) { *v = (*v + %s) %% 10007 })", k))
		for i := range g.bi.items {
			g.bi.items[i] = (g.bi.items[i] + d) % 10007
		}
		g.reached["each-capture"] = true
	case 15, 16:
		var f string
		if len(g.fns) == 0 || g.r.IntN(3) == 0 {
			f = g.name("h")
			c := g.small()
			g.line(fmt.Sprintf("let %s = func(v *int) { *v = *v + %d }", f, c))
			g.fns = append(g.fns, f)
			g.fnAdd[f] = c
		} else {
			f = g.fns[g.r.IntN(len(g.fns))]
		}
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("each(&bi, %s)", f))
			g.reached["each-opaque-let"] = true
		} else {
			g.line(fmt.Sprintf("each(&bi, func(v *int) { %s(v) })", f))
			g.reached["each-opaque-lit"] = true
		}
		for i := range g.bi.items {
			g.bi.items[i] += g.fnAdd[f]
		}
	case 17:
		i, c := []int{0, 1, 2, 7}[g.r.IntN(4)], g.small()
		g.line(fmt.Sprintf("fwd(&bi, %d, func(v *int) { *v = *v - %d })", i, c))
		if i < len(g.bi.items) {
			g.bi.items[i] -= c
			g.reached["fwd-ok"] = true
		} else {
			g.bi.n++
			g.reached["fwd-missing"] = true
		}
	case 18:
		c := g.small()
		g.line(fmt.Sprintf("fwd2(&bs, func(v *[]int) { *v = []int{len(*v), %d} })", c))
		g.bs.items[0] = []int{len(g.bs.items[0]), c}
		for i := range g.bs.items {
			g.bs.items[i] = []int{len(g.bs.items[i]), c}
		}
		g.reached["fwd2"] = true
	case 19:
		v, z, c := g.name("k"), g.small(), g.small()
		g.line(fmt.Sprintf("let %s = fold(bi, %d, func(acc int, v int) int { acc + v * %d })", v, z, c))
		d := z
		for _, x := range g.bi.items {
			d += x * c
		}
		g.ints = append(g.ints, v)
		g.kept = append(g.kept, keptVal{v, "", func() int { return d }})
		g.reached["fold-pure"] = true
	case 20:
		v, z, c := g.name("k"), g.small(), g.small()
		f := g.name("m")
		g.line(fmt.Sprintf("let %s = func(acc int, v int) int { acc * 2 %% 10007 + v * %d }", f, c))
		g.line(fmt.Sprintf("let %s = fold(bi, %d, %s)", v, z, f))
		d := z
		for _, x := range g.bi.items {
			d = d*2%10007 + x*c
		}
		g.ints = append(g.ints, v)
		g.kept = append(g.kept, keptVal{v, "", func() int { return d }})
		g.reached["fold-opaque"] = true
	case 22:
		for _, fn := range []string{"via", "via2", "at2"} {
			v := g.name("s")
			switch fn {
			case "at2":
				g.line(fmt.Sprintf("let %s = at2(bs, 0)", v))
			case "via2":
				g.line(fmt.Sprintf("let %s = via2(bs, []int{7})", v))
			default:
				g.line(fmt.Sprintf("let %s = via(bs)", v))
			}
			d := kds(g.bs.items[0])
			g.kept = append(g.kept, keptVal{v, "ds", func() int { return d }})
			g.reached[fn] = true
		}
		// A write through the root follows, so a part bound above that
		// still shares bs shows the write.
		c := g.small()
		g.line(fmt.Sprintf("bs.items[0][0] = %d", c))
		g.bs.items[0][0] = c
	case 23:
		c := g.small()
		f := g.name("h")
		g.line(fmt.Sprintf("let %s = keep(&bi, func(v *int) { *v = *v + %d })", f, c))
		g.bi.items[0] += c
		g.line(fmt.Sprintf("each(&bi, %s)", f))
		for i := range g.bi.items {
			g.bi.items[i] += c
		}
		g.reached["keep"] = true
	case 21:
		c := g.small()
		g.line(fmt.Sprintf("bumpAll(&bi, %d)", c))
		for i := range g.bi.items {
			g.bi.items[i] += c
		}
		g.bi.items[1] = g.bi.items[1] * 2 % 10007
		g.reached["closed"] = true
	default:
		g.line("Console.Println(\"\\(dbi(bi)) \\(dbs(bs)) \\(dp(p))\")")
		g.out("%d %d %d", g.bi.digest(), g.bs.digest(), kds(g.pa)*5+g.pb.digest())
		g.reached["print"] = true
	}
}

// knownProgram is the program and the model's output of seed.
func knownProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &knownGen{r: rand.New(rand.NewPCG(seed, 0x4b4e4f57)), reached: reached, fnAdd: map[string]int{}}
	g.bi = kbox{items: []int{1, 2, 3}}
	g.bs = kboxs{items: [][]int{{1}, {2, 3}}}
	g.pa, g.pb = []int{1}, kbox{items: []int{5}}
	g.line("let mut bi = Box[int]{items: []int{1, 2, 3}, n: 0}")
	g.line("let mut bs = Box[[]int]{items: [][]int{[]int{1}, []int{2, 3}}, n: 0}")
	g.line("let mut p = Pair{a: []int{1}, b: Box[int]{items: []int{5}, n: 0}}")
	for range 28 {
		g.stmt()
	}
	// Every root is written once more, after every read.
	g.line("bi.n = bi.n + 1")
	g.line("bs.n = bs.n + 1")
	g.line("p.b.n = p.b.n + 1")
	g.bi.n++
	g.bs.n++
	g.pb.n++
	for _, k := range g.kept {
		if k.digest == "" {
			g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", k.name))
		} else {
			g.line(fmt.Sprintf("Console.Println(\"\\(%s(%s))\")", k.digest, k.name))
		}
		g.out("%d", k.value())
	}
	g.line("Console.Println(\"\\(dbi(bi)) \\(dbs(bs)) \\(dp(p))\")")
	g.out("%d %d %d", g.bi.digest(), g.bs.digest(), kds(g.pa)*5+g.pb.digest())
	// Two tasks loop forever through spin; the stop ends each at its
	// loop's point: one with a known pure literal, one with an opaque
	// function value.
	for i, cb := range []string{"func(v *int) { *v = *v + 1 }", "h"} {
		g.line(fmt.Sprintf("let r%d = queue.New[int](0)", i))
		g.line(fmt.Sprintf("let t%d = task.Go() {", i))
		g.line("\tlet mut b = Box[int]{items: []int{0}, n: 0}")
		if cb == "h" {
			g.line("\tlet h = func(v *int) { *v = *v + 2 }")
		}
		g.line(fmt.Sprintf("\tr%d.Push(1) catch {", i))
		g.line("\tcase queue.Closed:")
		g.line("\t}")
		g.line(fmt.Sprintf("\tspin(&b, %s)", cb))
		g.line("}")
		g.line(fmt.Sprintf("r%d.Pop() catch {", i))
		g.line("case queue.Closed:")
		g.line("}")
		g.line(fmt.Sprintf("t%d.Stop()", i))
		g.line(fmt.Sprintf("Console.Println(\"stopped %d\")", i))
		g.out("stopped %d", i)
	}
	return knownPrelude + "func main() needs Console {\n" + g.b.String() + "}\n", g.want.String()
}

// lowerKnownMode lowers src under the new lowering, or under the reference
// (ref): callbackLowering "opaque" and aliasLowering "type". It returns the
// lowering's counts.
func lowerKnownMode(t *testing.T, name, src string, ref bool) (*lowered, knownStats) {
	t.Helper()
	savedCb, savedAlias, savedHook := callbackLowering, aliasLowering, statsHook
	defer func() { callbackLowering, aliasLowering, statsHook = savedCb, savedAlias, savedHook }()
	callbackLowering, aliasLowering = "", ""
	if ref {
		callbackLowering, aliasLowering = "opaque", "type"
	}
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	return mustLower(t, name, src), stats
}

func (s *knownStats) add(o knownStats) {
	s.Sites += o.Sites
	s.Variants += o.Variants
	s.ClosedEntries += o.ClosedEntries
	s.ArgsShared += o.ArgsShared
	s.ResultCopies += o.ResultCopies
	s.BorrowCopies += o.BorrowCopies
}

// TestKnownCallbacksAgree is the seeded property (see above).
//
// It builds Go because it compares the runs of built programs with a
// model, and stops tasks that loop forever: the question is behaviour at
// run time, interruption included, which no in-process check observes.
func TestKnownCallbacksAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *knownSeed != 0 {
		seeds = []uint64{*knownSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
		name      string
	}
	var runs []run
	var programs []*lowered
	var newStats, refStats knownStats
	for _, seed := range seeds {
		src, want := knownProgram(seed, reached)
		for _, ref := range []bool{false, true} {
			name := fmt.Sprintf("kc%d", seed)
			if ref {
				name += "ref"
			}
			p, s := lowerKnownMode(t, name, src, ref)
			if ref {
				refStats.add(s)
			} else {
				newStats.add(s)
			}
			programs = append(programs, p)
			runs = append(runs, run{seed, src, want, name})
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the known-callback programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the model differ\nReplay: go test -run '^TestKnownCallbacksAgree$' ./internal/sketch/lower -args -known.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	t.Logf("seeds %v: new %+v, reference %+v", seeds, newStats, refStats)
	if *knownSeed != 0 {
		return
	}
	for _, path := range knownPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
	if newStats.Sites == 0 || newStats.Variants == 0 || newStats.ClosedEntries == 0 || newStats.ArgsShared == 0 || newStats.ResultCopies == 0 || newStats.BorrowCopies == 0 {
		t.Errorf("the new lowering did not run every rule: %+v", newStats)
	}
	if refStats.Sites != 0 || refStats.Variants != 0 || refStats.ClosedEntries != 0 || refStats.ArgsShared != 0 || refStats.BorrowCopies == 0 {
		t.Errorf("the reference lowering used a summary or a known site: %+v", refStats)
	}
}
