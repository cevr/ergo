package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The hand-over's seeded property (OWN1; handover.go). Each seed writes a
// program over a record Bag{Items []int, N int} that grows, shares, cuts
// and rebinds values through every site the three shortcuts decide:
//
//   - push(b, v), an Append on a parameter's field: a one-extender site
//     (own-lineage), called on the newest value of a line of pushes and on
//     an older one (two extenders of one array), on a cut b.Items[0:k] and
//     on a let mut root;
//   - the same Append written inline on a let that stays live;
//   - grow(b, v), an Append on a field of a let mut copy that is dead after
//     it (own-field);
//   - doc = slices.Concat(doc, x.Items) on a let mut root, and Concat on a
//     dead local and on a live let (own-consume, and its refusal);
//   - writes of an element of a let mut root that took a pushed value, so
//     a root that shared a one-extender array shows it.
//
// The generator is also the model: it runs each statement on Go values
// with value semantics (every bind copies) and computes the line the
// program prints. The program is lowered three ways: the rule (the run
// build, which takes the shortcuts), the copying lowering (valueLowering
// "copy", the reference, which takes none) and the test build, whose
// checking forms copy and poison; the first two print the model's line,
// and the test build's test asserts it.
//
// A failure prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=N
var handoverSeed = flag.Uint64("handover.seed", 0, "run TestHandoverAgrees on this seed alone")

// hbag is a model Bag.
type hbag struct {
	items []int
	n     int
}

func (b hbag) copy() hbag { return hbag{items: append([]int(nil), b.items...), n: b.n} }

// hdig is the program's dig on the model: the length and a hash of xs.
func hdig(xs []int) string {
	s := 0
	for _, x := range xs {
		s = (s*31 + x) % 1000003
	}
	return fmt.Sprintf("%d/%d", len(xs), s)
}

// handoverGen writes one program and runs it on the model.
type handoverGen struct {
	r       *rand.Rand
	b       strings.Builder
	out     strings.Builder // the model's output
	bags    []hbag          // the lets b0, b1, … of run
	m       hbag            // the root m
	doc     []int           // the root doc
	reached map[string]bool
}

func (g *handoverGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

// bind adds the let b<len> = expr with the model value v.
func (g *handoverGen) bind(expr string, v hbag) {
	g.line(fmt.Sprintf("let b%d = %s", len(g.bags), expr))
	g.bags = append(g.bags, v.copy())
}

func (g *handoverGen) pick() int {
	// The newest bindings most often, so lines of pushes grow long.
	n := len(g.bags)
	if g.r.IntN(3) > 0 {
		return n - 1 - g.r.IntN(min(n, 2))
	}
	return g.r.IntN(n)
}

func (g *handoverGen) print(name string, xs []int) {
	g.line(fmt.Sprintf("out = out + dig(%s) + \" \"", name))
	g.out.WriteString(hdig(xs) + " ")
}

func (g *handoverGen) stmt() {
	i := g.pick()
	src := g.bags[i]
	v := g.r.IntN(90) + 1
	push := func(b hbag, v int) hbag {
		return hbag{items: append(append([]int(nil), b.items...), v), n: b.n + 1}
	}
	ring := func(b hbag, v int) hbag {
		b = b.copy()
		if len(b.items) < 4 {
			b.items = append(b.items, v)
			return b
		}
		b.items[b.n%4] = v
		b.n++
		return b
	}
	switch k := g.r.IntN(14); k {
	case 12:
		g.reached["ring on a live let"] = true
		g.bind(fmt.Sprintf("ring(b%d, %d)", i, v), ring(src, v))
	case 13:
		g.reached["root rings"] = true
		g.line(fmt.Sprintf("m = ring(m, %d)", v))
		g.m = ring(g.m, v)
	case 11:
		// Two extenders of the newest value: the second must copy.
		g.reached["branch"] = true
		i = len(g.bags) - 1
		src = g.bags[i]
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v), push(src, v))
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v+1), push(src, v+1))
	case 0, 1, 2:
		g.reached["push"] = true
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v), push(src, v))
	case 3:
		g.reached["inline append on a live let"] = true
		g.bind(fmt.Sprintf("Bag{N: b%d.N + 1, Items: b%d.Items.Append(%d)}", i, i, v), push(src, v))
	case 4:
		g.reached["grow"] = true
		g.bind(fmt.Sprintf("grow(b%d, %d)", i, v), push(src, v))
	case 5:
		if len(src.items) == 0 {
			return
		}
		g.reached["cut"] = true
		k := g.r.IntN(len(src.items) + 1)
		g.bind(fmt.Sprintf("Bag{N: 0, Items: b%d.Items[0:%d]}", i, k), hbag{items: src.items[:k]})
	case 6:
		g.reached["root takes a push"] = true
		g.line(fmt.Sprintf("m = push(b%d, %d)", i, v))
		g.m = push(src, v)
		if len(g.m.items) > 0 {
			j := g.r.IntN(len(g.m.items))
			g.line(fmt.Sprintf("m.Items[%d] = %d", j, 500+v))
			g.m.items[j] = 500 + v
		}
	case 7:
		g.reached["root pushes"] = true
		g.line(fmt.Sprintf("m = push(m, %d)", v))
		g.m = push(g.m, v)
	case 8:
		g.reached["concat on a root"] = true
		g.line(fmt.Sprintf("doc = slices.Concat(doc, b%d.Items)", i))
		g.doc = append(g.doc, src.items...)
		if len(g.doc) > 0 {
			j := g.r.IntN(len(g.doc))
			g.line(fmt.Sprintf("doc[%d] = %d", j, 700+v))
			g.doc[j] = 700 + v
		}
	case 9:
		g.reached["concat on a live let"] = true
		j := g.pick()
		g.bind(fmt.Sprintf("Bag{N: 0, Items: slices.Concat(b%d.Items, b%d.Items)}", i, j), hbag{items: append(append([]int(nil), src.items...), g.bags[j].items...)})
	case 10:
		g.reached["concat on a dead local"] = true
		n := len(g.bags)
		g.line(fmt.Sprintf("let t%d = []int{%d, %d}", n, v, v+1))
		g.bind(fmt.Sprintf("Bag{N: 0, Items: slices.Concat(t%d, b%d.Items)}", n, i), hbag{items: append([]int{v, v + 1}, src.items...)})
	}
	if g.r.IntN(3) == 0 {
		j := g.r.IntN(len(g.bags))
		g.print(fmt.Sprintf("b%d.Items", j), g.bags[j].items)
	}
}

// handoverProgram is seed's program and the line it must print.
func handoverProgram(seed uint64, reached map[string]bool) (string, string) {
	g := &handoverGen{r: rand.New(rand.NewPCG(seed, 0x6f776e31)), reached: reached}
	g.b.WriteString(`import slices

type Bag { Items []int, N int }

func dig(xs []int) string {
	let mut s = 0
	for x in xs {
		s = (s * 31 + x) % 1000003
	}
	"\(len(xs))/\(s)"
}

// push extends a parameter's field: a one-extender site.
func push(b Bag, v int) Bag {
	Bag{Items: b.Items.Append(v), N: b.N + 1}
}

// grow extends a field of its own copy, dead after the Append.
func grow(b Bag, v int) Bag {
	let mut c = b
	c.N = c.N + 1
	Bag{N: c.N, Items: c.Items.Append(v)}
}

// ring takes its parameter over through its fields (handover.go,
// fieldOwned): a ring of four slots, as GP's ring-copy.
func ring(b Bag, v int) Bag {
	if len(b.Items) < 4 {
		return Bag{Items: b.Items.Append(v), N: b.N}
	}
	let mut items = b.Items
	items[b.N % 4] = v
	Bag{Items: items, N: b.N + 1}
}

func run() string {
	let mut out = ""
	let mut m = Bag{Items: []int{}, N: 0}
	let mut doc = []int{}
	let b0 = Bag{Items: []int{}, N: 0}
`)
	g.bags = []hbag{{items: []int{}}}
	g.m = hbag{items: []int{}}
	for range 80 {
		g.stmt()
	}
	for i, b := range g.bags {
		g.print(fmt.Sprintf("b%d.Items", i), b.items)
	}
	g.print("m.Items", g.m.items)
	g.print("doc", g.doc)
	want := g.out.String()
	g.b.WriteString("\tout\n}\n\nfunc main() needs Console {\n\tConsole.Println(run())\n}\n\n")
	g.b.WriteString(fmt.Sprintf("test Agrees {\n\tassert run() == %q\n}\n", want))
	return g.b.String(), want + "\n"
}

// TestHandoverAgrees is the seeded property (see above).
//
// It builds Go because it compares the runs of built programs with a
// model: the question is the values the program computes at run time,
// where an in-place write either shows through another binding or does
// not; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestHandoverAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *handoverSeed != 0 {
		seeds = []uint64{*handoverSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
		name      string
		test      bool
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := handoverProgram(seed, reached)
		name := fmt.Sprintf("ho%d", seed)
		programs = append(programs, lowerValueMode(t, name, src, true))
		runs = append(runs, run{seed, src, want, name, false})
		programs = append(programs, lowerValueMode(t, name+"copy", src, true, "copy"))
		runs = append(runs, run{seed, src, want, name + "copy", false})
		programs = append(programs, lowerTestBuild(t, name+"test", src))
		runs = append(runs, run{seed, src, want, name + "test", true})
	}
	// The rule takes every shortcut somewhere: else the property proves
	// nothing about it.
	for _, form := range []string{"ergo.LineageAppend(", "ergo.SlicesConcatOwned[", "ringOwned(", "ergo.AppendPoison(", "ergo.LineageAppendCheck("} {
		found := false
		for _, p := range programs {
			if strings.Contains(p.mainGo(), form) {
				found = true
			}
		}
		if !found {
			t.Errorf("no lowering of seeds %v calls %s", seeds, form)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the hand-over programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if r.test {
			if code != 0 {
				t.Errorf("seed %d, %s: the test build's assertion of the model fails (exit %d)\nReplay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=%d\n%s\n--- test run\n%s%s", r.seed, r.name, code, r.seed, r.src, out, stderr)
			}
			continue
		}
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the value model differ\nReplay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	if *handoverSeed == 0 {
		for _, s := range []string{"push", "branch", "ring on a live let", "root rings", "inline append on a live let", "grow", "cut", "root takes a push", "root pushes", "concat on a root", "concat on a live let", "concat on a dead local"} {
			if !reached[s] {
				t.Errorf("seeds %v never write %q", seeds, s)
			}
		}
	}
}
