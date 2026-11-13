package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var uiSkipSeed = flag.Uint64("uiskip.seed", 0, "run TestUISkipAgrees on this seed alone")

// uiSkipCommon is the part of TestUISkipAgrees's program no seed changes:
// the state, the printers of a pass, and the growth guard's list.
const uiSkipCommon = `import ui

type Item { Id int, Text string, Done bool }

type St { A int, It Item, Items []Item }

func rows(a int) []int {
	let mut out = []int{}
	for i in 0..a%4 {
		out = out.Append(i*3 + a%2)
	}
	out
}

func b(v bool) string {
	if v {
		return "1"
	}
	"0"
}

func nodeText(n ui.Node[int]) string {
	match n {
	case ui.TextNode as x:
		"text \(x.Value)"
	case ui.CheckNode[int] as x:
		"check \(x.Label) \(b(x.Checked)) \(x.On)"
	case ui.PressableNode[int] as x:
		"press \(x.Label) \(x.On)"
	case ui.ToggleNode as x:
		"toggle \(x.Label) \(b(x.On))"
	case ui.RowNode:
		"row"
	case ui.ColumnNode:
		"column"
	case _:
		"other"
	}
}

func shot(shapes []ui.Shape[int], indent string) string {
	let mut out = ""
	for s in shapes {
		out = out + indent + nodeText(s.Node) + "\n" + shot(s.Kids, indent+"  ")
	}
	out
}

func show(ops []ui.Op[int]) string {
	let mut out = ""
	for op in ops {
		match op {
		case ui.Insert[int]:
			out = out + "I"
		case ui.Move[int]:
			out = out + "M"
		case ui.Remove[int]:
			out = out + "R"
		case ui.Set[int]:
			out = out + "S"
		}
	}
	out
}

func ids(f *ui.Frame[int], id ui.Id[int]) []ui.Id[int] {
	let mut out = []ui.Id[int]{}
	for k in ui.Kids(f, id) {
		out = out.Append(k)
		for x in ids(f, k) {
			out = out.Append(x)
		}
	}
	out
}

func sent(o Option[int]) string {
	match o {
	case Some[int] as s:
		"\(s.Value)"
	case None:
		"none"
	}
}

func step(f *ui.Frame[int], view func(f *ui.Frame[int], s St), s St, press int) needs Console {
	let ops = ui.Render(f, view, s)
	Console.Println("ops \(show(ops)) len \(ui.Len(f))")
	Console.Println(shot(ui.FrameShape(f), ""))
	Console.Println("runs \(ui.GroupRuns(f))")
	if press >= 0 {
		let all = ids(f, ui.Root(f))
		let h = ui.Handle(f, ui.Press[int]{Id: all[press%len(all)]})
		Console.Println("press \(sent(h.Signal)) \(b(h.Changed))")
	}
}

func card(f *ui.Frame[int], it Item) {
	ui.Row(f, ui.Plain()) {
		ui.Check(f, it.Text, it.Done, it.Id)
		ui.Text(f, "#\(it.Id)")
	}
}

func list(f *ui.Frame[int], xs []Item) {
	ui.Each(f, xs, func(it Item) ui.Key { ui.ByInt(it.Id) }) func(it Item) {
		card(f, it)
	}
}

func listed(n int, changed int) []Item {
	let mut xs = []Item{}
	for i in 0..n {
		if i == changed {
			xs = xs.Append(Item{Id: i, Text: "changed", Done: true})
		} else {
			xs = xs.Append(Item{Id: i, Text: "r\(i)", Done: false})
		}
	}
	xs
}

// growth renders n cards, then the same n with one changed, then one:
// the runs of each pass and the groups kept after the last.
func growth(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, list, listed(n, -1))
	let first = ui.GroupRuns(&f)
	let _ = ui.Render(&f, list, listed(n, n/2))
	let changed = ui.GroupRuns(&f)
	let _ = ui.Render(&f, list, listed(1, -1))
	Console.Println("growth \(n) \(first) \(changed) \(ui.GroupsKept(&f))")
}
`

// uiSkipGen writes one seed's components s<seed>c<i>(f, a, it), each a
// view whose calls of later components are groups when their arguments
// are names, selections or constants; and its view.
type uiSkipGen struct {
	r     *rand.Rand
	seed  uint64
	comps int
	out   strings.Builder
	flags int
}

func (g *uiSkipGen) name(i int) string { return fmt.Sprintf("s%dc%d", g.seed, i) }

// call is a call of a component after i, or "" when i is the last.
func (g *uiSkipGen) call(i int, args string) string {
	if i+1 >= g.comps {
		return ""
	}
	return fmt.Sprintf("%s(f, %s)", g.name(i+1+g.r.IntN(g.comps-i-1)), args)
}

func (g *uiSkipGen) stmts(i, depth int, indent string) {
	for range 1 + g.r.IntN(3) {
		g.stmt(i, depth, indent)
	}
}

func (g *uiSkipGen) stmt(i, depth int, indent string) {
	w := func(format string, args ...any) { fmt.Fprintf(&g.out, indent+format+"\n", args...) }
	block := func(head string) {
		w("%s {", head)
		g.stmts(i, depth+1, indent+"\t")
		w("}")
	}
	kind := g.r.IntN(12)
	if depth >= 2 && kind >= 3 && kind <= 5 {
		kind = 0
	}
	switch kind {
	case 0:
		w(`ui.Text(f, "c%d \(a) \(it.Text)")`, i)
	case 1:
		w("ui.Check(f, it.Text, it.Done, it.Id)")
	case 2:
		g.flags++
		w("let fl%d = ui.NewFlag(f, a%%2 == 0)", g.flags)
		w(`ui.Toggle(f, fl%d, "t%d")`, g.flags, i)
	case 3:
		block(fmt.Sprintf(`ui.Pressable(f, "p%d", a)`, i))
	case 4:
		block("ui.Row(f, ui.Plain())")
	case 5:
		block(fmt.Sprintf("if a%%%d == %d", 2+g.r.IntN(2), g.r.IntN(2)))
	default:
		args := []string{"a, it", "it.Id, it", "3, it", "a+1, it"}[g.r.IntN(4)]
		c := g.call(i, args)
		if c == "" {
			w(`ui.Text(f, "leaf \(it.Id)")`)
			return
		}
		switch kind {
		case 6, 7:
			w("%s", c)
		case 8:
			// A call of the same component at two sites under one parent,
			// the first conditional: the second's identity count changes.
			w("if a%%2 == 0 {\n%s\t%s\n%s}", indent, c, indent)
			w("%s", c)
		case 9:
			w("for k in 0..a%%3 {\n%s\t%s\n%s}", indent, g.call(i, "k, it"), indent)
		default:
			w("ui.Each(f, rows(a), func(k int) ui.Key { ui.ByInt(k) }) func(k int) {\n%s\t%s\n%s}", indent, g.call(i, "k, it"), indent)
		}
	}
}

// program writes the components and the view s<seed>view.
func (g *uiSkipGen) program() {
	for i := range g.comps {
		fmt.Fprintf(&g.out, "\nfunc %s(f *ui.Frame[int], a int, it Item) {\n", g.name(i))
		g.stmts(i, 0, "\t")
		g.out.WriteString("}\n")
	}
	fmt.Fprintf(&g.out, `
func s%dview(f *ui.Frame[int], s St) {
	%s(f, s.A, s.It)
	ui.Column(f, ui.Plain()) {
		ui.Each(f, s.Items, func(it Item) ui.Key { ui.ByInt(it.Id) }) func(it Item) {
			%s(f, s.A, it)
		}
	}
}
`, g.seed, g.name(0), g.name(1))
}

// states is a run of passes: each the last state with a few edits, or
// the same, and a press after it (-1 none).
func (g *uiSkipGen) states(n int) []string {
	type item struct {
		id   int
		text string
		done bool
	}
	lit := func(it item) string {
		return fmt.Sprintf("Item{Id: %d, Text: %q, Done: %v}", it.id, it.text, it.done)
	}
	a, top := g.r.IntN(6), item{1, "top", false}
	var items []item
	for k := range 1 + g.r.IntN(4) {
		items = append(items, item{k, fmt.Sprintf("i%d", k), false})
	}
	next := len(items)
	var out []string
	for range n {
		switch g.r.IntN(7) {
		case 0:
			a = g.r.IntN(6)
		case 1:
			if len(items) > 0 {
				k := g.r.IntN(len(items))
				items[k].done = !items[k].done
			}
		case 2:
			top.text = fmt.Sprintf("top%d", g.r.IntN(3))
		case 3:
			items = append(items, item{next, fmt.Sprintf("i%d", next), false})
			next++
		case 4:
			if len(items) > 1 {
				k := g.r.IntN(len(items))
				items = append(items[:k:k], items[k+1:]...)
			}
		case 5:
			g.r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
		}
		var xs []string
		for _, it := range items {
			xs = append(xs, lit(it))
		}
		press := -1
		if g.r.IntN(3) == 0 {
			press = g.r.IntN(40)
		}
		out = append(out, fmt.Sprintf("St{A: %d, It: %s, Items: []Item{%s}}, %d", a, lit(top), strings.Join(xs, ", "), press))
	}
	return out
}

// TestUISkipAgrees is the property of skipping (P-ui; stdlib.md, UI 13):
// a program of generated components, rendered pass after pass over
// edited states with presses between, writes the same tree, the same
// patches and the same signals with component calls lowered as groups as
// with every call run (SetUISkipForTest(false), the reference). The
// components nest, repeat at one site in loops and Each rows, change
// their node counts with their arguments, and some read a flag, so are
// never kept; a press may flip one. A failing seed is replayed with
// -uiskip.seed=N, and the program's text is in the failure.
//
// It also holds that skipping happens, as counted work: a pass that
// returns the same arguments runs fewer components than the first pass,
// and one changed card of n runs one component, at n 8 and 64; the
// groups kept follow the cards when they drop to one (growth).
//
// It builds Go because the subject is the lowered program's behaviour
// over passes and events, which only runs.
//
// Mutants (testdata/mutants.json): ui-group-args-ignored,
// ui-group-impure-kept, ui-group-count-unchecked, ui-group-parent-unmapped,
// ui-group-slots-leak, ui-skip-never.
func TestUISkipAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *uiSkipSeed != 0 {
		seeds = []uint64{*uiSkipSeed}
	}
	var src strings.Builder
	src.WriteString(uiSkipCommon)
	var main strings.Builder
	main.WriteString("\nfunc main() needs Console {\n")
	for _, seed := range seeds {
		g := &uiSkipGen{r: rand.New(rand.NewPCG(seed, 0x51c9)), seed: seed, comps: 5}
		g.program()
		src.WriteString(g.out.String())
		fmt.Fprintf(&main, "\tConsole.Println(\"seed %d\")\n\tlet mut f%d = ui.NewFrame[int]()\n", seed, seed)
		for _, s := range g.states(10) {
			fmt.Fprintf(&main, "\tstep(&f%d, s%dview, %s)\n", seed, seed, s)
		}
	}
	main.WriteString("\tgrowth(8)\n\tgrowth(64)\n}\n")
	src.WriteString(main.String())
	skip := mustLower(t, "uiskip_groups", src.String())
	restore := SetUISkipForTest(false)
	ref := mustLower(t, "uiskip_reference", src.String())
	restore()
	exes := buildAll(t, []*lowered{skip, ref}, true)
	if exes == nil {
		return
	}
	out := map[string]string{}
	for _, prog := range []string{"uiskip_groups", "uiskip_reference"} {
		stdout, stderr, code := runExe(t, exes[prog])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s\n%s", prog, code, firstLines(stderr, 8), src.String())
		}
		out[prog] = stdout
	}
	// The behaviour, without the counted work.
	behaviour := func(s string) []string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "runs ") && !strings.HasPrefix(l, "growth ") {
				keep = append(keep, l)
			}
		}
		return keep
	}
	got, want := behaviour(out["uiskip_groups"]), behaviour(out["uiskip_reference"])
	seed := ""
	for i := 0; i < len(got) || i < len(want); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if strings.HasPrefix(w, "seed ") {
			seed = w
		}
		if g != w {
			t.Fatalf("%s, line %d: groups %q, reference %q (replay with -uiskip.seed=N)\n%s", seed, i, g, w, src.String())
		}
	}
	// Counted work: the first pass of each seed against the passes after.
	var first, later, seeds0 int
	var growth []string
	for _, l := range strings.Split(out["uiskip_groups"], "\n") {
		switch {
		case strings.HasPrefix(l, "seed "):
			seeds0 = 0
		case strings.HasPrefix(l, "runs "):
			var n int
			fmt.Sscanf(l, "runs %d", &n)
			if seeds0 == 0 {
				first += n
			} else {
				later += n
			}
			seeds0++
		case strings.HasPrefix(l, "growth "):
			growth = append(growth, l)
		}
	}
	if first == 0 || later >= first*9 {
		t.Errorf("the groups build ran %d components in first passes and %d in the 9 later ones of each seed: no component was skipped", first, later)
	}
	if want := []string{"growth 8 8 1 1", "growth 64 64 1 1"}; strings.Join(growth, "|") != strings.Join(want, "|") {
		t.Errorf("growth: %q, want %q (n, first runs, runs after one change, groups kept at one card)", growth, want)
	}
}
