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

type Stop { V int }

func stopIf(c bool, v int) fails Stop {
	if c {
		return Stop{V: v}
	}
}

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
		case ui.Define:
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

// framed is a component around a card: when only its tag changes it runs
// and the card in it, whose argument stayed, is written again.
func framed(f *ui.Frame[int], it Item, tag int) {
	ui.Column(f, ui.Plain()) {
		ui.Text(f, "tag \(tag)")
		card(f, it)
	}
}

func framedView(f *ui.Frame[int], s St) {
	framed(f, s.It, s.A)
}

// nested renders framedView at a 0, 0 (framed written again) and 1: the
// components the last pass ran (the card is kept through the pass that
// wrote framed again).
func nested() needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 2, Text: "y", Done: false}
	let _ = ui.Render(&f, framedView, St{A: 0, It: it, Items: []Item{}})
	let _ = ui.Render(&f, framedView, St{A: 0, It: it, Items: []Item{}})
	let _ = ui.Render(&f, framedView, St{A: 1, It: it, Items: []Item{}})
	Console.Println("nested \(ui.GroupRuns(&f))")
}

func empty(f *ui.Frame[int], s St) {
	ui.Text(f, "empty \(s.A)")
}

// headed is the cards under a header that shows a: a pass that changes
// a changes no row.
func headed(f *ui.Frame[int], s St) {
	ui.Text(f, "tick \(s.A)")
	ui.Column(f, ui.Plain()) {
		list(f, s.Items)
	}
}

// tally counts its calls in c: an argument that calls it shows how often
// the argument was evaluated.
func tally(c *Counter) int {
	c.N = c.N + 1
	c.N / 3
}

func shown(f *ui.Frame[int], n int) {
	ui.Text(f, "shown \(n)")
}

func talliedView(f *ui.Frame[int], c *Counter) {
	shown(f, tally(c))
}

// tallied renders a component whose argument calls tally, over four
// passes: the argument is evaluated once a pass, whether the call runs or
// is written again (its value changes every third call).
func tallied() needs Console {
	let mut f = ui.NewFrame[int]()
	let mut c = Counter{N: 0}
	for _ in 0..4 {
		let _ = ui.Render(&f, talliedView, &c)
	}
	Console.Println("tallied \(c.N)")
	Console.Println(shot(ui.FrameShape(&f), ""))
}

func labelled(f *ui.Frame[int], id int, label string) {
	ui.Row(f, ui.Plain()) {
		ui.Text(f, label)
		ui.Check(f, "c", false, id)
	}
}

// computed is the cards of elsewhere written with a computed label, a
// call that is a group since NS2a: a change of the header alone runs none.
func computed(f *ui.Frame[int], s St) {
	ui.Text(f, "tick \(s.A)")
	ui.Column(f, ui.Plain()) {
		ui.Each(f, s.Items, func(it Item) ui.Key { ui.ByInt(it.Id) }) func(it Item) {
			labelled(f, it.Id, "row \(it.Id) \(it.Text)")
		}
	}
}

// allChange renders n cards, then n cards each with a new text: every
// component runs (groups and reference alike), and the nodes compared, the
// components run and the groups kept are counted.
func allChange(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, computed, St{A: 0, It: it, Items: listed(n, -1)})
	let mut xs = []Item{}
	for i in 0..n {
		xs = xs.Append(Item{Id: i, Text: "new", Done: false})
	}
	let _ = ui.Render(&f, computed, St{A: 0, It: it, Items: xs})
	Console.Println("allchange \(n) compared \(ui.Compared(&f))")
	Console.Println("allcount \(n) \(ui.GroupRuns(&f)) \(ui.GroupsKept(&f))")
	let _ = ui.Render(&f, computed, St{A: 1, It: it, Items: xs})
	Console.Println("allcount \(n) header \(ui.GroupRuns(&f)) \(ui.Compared(&f))")
}

// elsewhere renders n cards under a header, then again with only the
// header changed: the components run and the nodes Commit compared then.
func elsewhere(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let xs = listed(n, -1)
	let _ = ui.Render(&f, headed, St{A: 0, It: it, Items: xs})
	let _ = ui.Render(&f, headed, St{A: 1, It: it, Items: xs})
	Console.Println("elsewhere \(n) \(ui.GroupRuns(&f)) \(ui.Compared(&f))")
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

type Counter { N int }

// Wide is a record with a slice: a component that reads no slice of it
// compares the fields it reads (NS2b).
type Wide { It Item, Tags []int, N int, M int }

func label(w Wide) string {
	"n\(w.N)"
}

// wideA reads It and, through label, N: never Tags or M.
func wideA(f *ui.Frame[int], w Wide) {
	ui.Text(f, "wide \(w.It.Text) \(label(w))")
}

// wideB reads Tags, a slice: no group.
func wideB(f *ui.Frame[int], w Wide) {
	ui.Text(f, "tags \(len(w.Tags))")
}

func wideView(f *ui.Frame[int], w Wide) {
	wideA(f, w)
}

// cards is a list holder: a component whose argument is a slice, a group
// compared by its structural equality against a copy it keeps (NS2c).
func cards(f *ui.Frame[int], xs []Item) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, xs, func(it Item) ui.Key { ui.ByInt(it.Id) }) func(it Item) {
			ui.Text(f, "card \(it.Text) \(b(it.Done))")
		}
	}
}

func cardsView(f *ui.Frame[int], s St) {
	ui.Text(f, "tick \(s.A)")
	cards(f, s.Items)
}

// Bag is a record with a nested slice, read whole by bagged.
type Bag { Name string, Tags []int }

func bagged(f *ui.Frame[int], g Bag) {
	ui.Text(f, "bag \(g.Name) \(len(g.Tags))")
}

// cardsGuard renders n cards under a header, then the header alone
// changed, then one card's text: the components run and the nodes
// compared after each of the last two, and the nodes a pass looked up
// by index (none: the children come back in order). Mutants
// ui-prior-place-uncounted and ui-cut-nkids-kept.
func cardsGuard(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, cardsView, St{A: 0, It: it, Items: listed(n, -1)})
	let _ = ui.Render(&f, cardsView, St{A: 1, It: it, Items: listed(n, -1)})
	Console.Println("cards \(n) tick \(ui.GroupRuns(&f)) \(ui.Compared(&f)) \(ui.Unplaced(&f)) \(ui.Swept(&f))")
	let _ = ui.Render(&f, cardsView, St{A: 1, It: it, Items: listed(n, n/2)})
	Console.Println("cards \(n) edit \(ui.GroupRuns(&f)) \(ui.Unplaced(&f))")
}

// headedOnce is headed with a component after the cards while a is 0.
func headedOnce(f *ui.Frame[int], s St) {
	headed(f, s)
	if s.A == 0 {
		shown(f, 1)
	}
}

// dropped renders n cards then the header alone changed, the component
// after them gone: the view runs and frees that component's group, and keeps the
// cards' groups through their list's, written again. Mutant
// ui-group-inner-unstamped.
func dropped(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, headedOnce, St{A: 0, It: it, Items: listed(n, -1)})
	let _ = ui.Render(&f, headedOnce, St{A: 1, It: it, Items: listed(n, -1)})
	Console.Println("dropped \(n) \(ui.GroupsKept(&f)) \(ui.GroupRuns(&f))")
}

// trailedView is a header, the list's rows as its own children, and a
// line after them.
func trailedView(f *ui.Frame[int], s St) {
	ui.Text(f, "tick \(s.A)")
	list(f, s.Items)
	ui.Text(f, "end")
}

// trailed renders n cards between a header and a line, then the header
// alone changed: the nodes the pass looked up by index (none: the line
// after the cards' block is at its place). Mutant ui-block-one-node.
func trailed(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, trailedView, St{A: 0, It: it, Items: listed(n, -1)})
	let _ = ui.Render(&f, trailedView, St{A: 1, It: it, Items: listed(n, -1)})
	Console.Println("trailed \(n) \(ui.Unplaced(&f))")
}

// shrunk renders n cards as components under a header, then the cards
// less the last, then the header alone changed: the groups kept, which
// lost the last card's, and the kept entries the last pass walked (none:
// it kept every held group). Mutants ui-keep-sweep-always,
// ui-keep-sweep-never and ui-keep-nheld-drift.
func shrunk(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, headed, St{A: 0, It: it, Items: listed(n, -1)})
	let _ = ui.Render(&f, headed, St{A: 0, It: it, Items: listed(n-1, -1)})
	let _ = ui.Render(&f, headed, St{A: 1, It: it, Items: listed(n-1, -1)})
	Console.Println("shrunk \(n) \(ui.GroupsKept(&f)) \(ui.Swept(&f))")
}

// looped writes n texts at one site: tops that share a site and row.
func looped(f *ui.Frame[int], n int) {
	for k in 0..n {
		ui.Text(f, "loop \(k)")
	}
}

func loopedView(f *ui.Frame[int], s St) {
	looped(f, 3)
}

// loopedTwice renders looped twice the same: the second pass keeps it.
func loopedTwice() needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, loopedView, St{A: 0, It: it, Items: []Item{}})
	let _ = ui.Render(&f, loopedView, St{A: 1, It: it, Items: []Item{}})
	Console.Println("looped \(ui.GroupRuns(&f))")
}

// cutView writes a boundary that stops after writing, between two
// texts in a column: its arm writes at the place the cut gave back.
func cutView(f *ui.Frame[int], s St) {
	ui.Column(f, ui.Plain()) {
		ui.Text(f, "a")
		ui.Boundary(f) {
			ui.Text(f, "b")
			try stopIf(true, s.A)
		} catch {
		case Stop:
			ui.Text(f, "c")
		}
		ui.Text(f, "d")
	}
}

// cutTwice renders cutView twice: the second pass finds every old child
// at its place, the arm's and the one after it too: only the text the
// body wrote before it stopped, never an old child, is looked up by index.
func cutTwice() needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, cutView, St{A: 0, It: it, Items: []Item{}})
	let _ = ui.Render(&f, cutView, St{A: 1, It: it, Items: []Item{}})
	Console.Println("cut \(ui.Unplaced(&f))")
}

// marks shows its first card, then writes it through a let mut of its
// parameter, dead after it, so in place, into the array it was called
// with.
func marks(f *ui.Frame[int], xs []Item) {
	ui.Text(f, "first \(xs[0].Text)")
	let mut ys = xs
	ys[0] = Item{Id: 0, Text: "seen", Done: true}
	ui.Text(f, "after \(ys[0].Text)")
}

func marksView(f *ui.Frame[int], s St) {
	marks(f, s.Items)
}

// tagged takes two values (compared as fields of one), then writes its
// tags in place through a let mut of its parameter.
func tagged(f *ui.Frame[int], name string, tags []int) {
	ui.Text(f, "tag \(name) \(tags[0])")
	let mut t = tags
	t[0] = 99
	ui.Text(f, "t \(t[0])")
}

func taggedView(f *ui.Frame[int], g Bag) {
	tagged(f, g.Name, g.Tags)
}

// aliased renders marks over three cards, then over the cards marks
// wrote: the group's kept copy is its own, so the write in place did not
// reach it, the arguments differ and the new first card shows.
func aliased() needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 0, Text: "", Done: false}
	let _ = ui.Render(&f, marksView, St{A: 0, It: it, Items: listed(3, -1)})
	let mut xs = listed(3, -1)
	xs[0] = Item{Id: 0, Text: "seen", Done: true}
	let _ = ui.Render(&f, marksView, St{A: 0, It: it, Items: xs})
	Console.Println(shot(ui.FrameShape(&f), "aliased "))
	let mut g = ui.NewFrame[int]()
	let _ = ui.Render(&g, taggedView, Bag{Name: "x", Tags: []int{1, 2}})
	let _ = ui.Render(&g, taggedView, Bag{Name: "x", Tags: []int{99, 2}})
	Console.Println(shot(ui.FrameShape(&g), "aliased "))
}

// subset renders wideA as only Tags changes, then M (read by no one),
// then N (read through label): the components run and the groups kept
// after each of the last three passes.
func subset() needs Console {
	let mut f = ui.NewFrame[int]()
	let it = Item{Id: 1, Text: "x", Done: false}
	for k in 0..3 {
		let _ = ui.Render(&f, wideView, Wide{It: it, Tags: rows(k+1), N: 1, M: 0})
	}
	Console.Println("subset \(ui.GroupRuns(&f)) \(ui.GroupsKept(&f))")
	let _ = ui.Render(&f, wideView, Wide{It: it, Tags: []int{}, N: 1, M: 5})
	Console.Println("subset \(ui.GroupRuns(&f)) \(ui.GroupsKept(&f))")
	let _ = ui.Render(&f, wideView, Wide{It: it, Tags: []int{}, N: 2, M: 5})
	Console.Println("subset \(ui.GroupRuns(&f)) \(ui.GroupsKept(&f))")
	Console.Println(shot(ui.FrameShape(&f), "wide "))
}

func counted(f *ui.Frame[int], c *Counter) {
	ui.Text(f, "n \(c.N)")
}

func counterView(f *ui.Frame[int], c *Counter) {
	counted(f, c)
}

// flagged is a component whose Same holds a toggle: in a pass that
// changes a but not a%2 it runs and skips the Same, so it read no cell,
// yet the cell under the Same is state its arguments do not say.
func flagged(f *ui.Frame[int], a int) {
	ui.Same(f, ui.ByInt(a%2)) {
		let fl = ui.NewFlag(f, false)
		ui.Toggle(f, fl, "t")
	}
}

func flaggedView(f *ui.Frame[int], s St) {
	flagged(f, s.A)
}

func checked(f *ui.Frame[int], n int) {
	ui.Check(f, "c", false, n)
}

// fenced puts a component under a Fieldset that a%2 disables: the
// component's arguments stay, its node's disabled state does not.
func fenced(f *ui.Frame[int], s St) {
	ui.Fieldset(f, s.A%2 == 1, "") {
		checked(f, 7)
	}
}

// pressLast presses the last node of f's tree in depth-first order (a
// view's only control here), once.
func pressLast(f *ui.Frame[int]) needs Console {
	let all = ids(f, ui.Root(f))
	let h = ui.Handle(f, ui.Press[int]{Id: all[len(all)-1]})
	Console.Println("pressed \(sent(h.Signal)) \(b(h.Changed))")
}

// fixed renders flaggedView at a 0, 2 (the Same skipped), a press of
// its toggle, 2 again; and fenced at a 0, a press, 1, a press.
func fixed() needs Console {
	let it = Item{Id: 1, Text: "x", Done: false}
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, flaggedView, St{A: 0, It: it, Items: []Item{}})
	let _ = ui.Render(&f, flaggedView, St{A: 2, It: it, Items: []Item{}})
	pressLast(&f)
	let _ = ui.Render(&f, flaggedView, St{A: 2, It: it, Items: []Item{}})
	Console.Println(shot(ui.FrameShape(&f), "flagged "))
	let mut g = ui.NewFrame[int]()
	let _ = ui.Render(&g, fenced, St{A: 0, It: it, Items: []Item{}})
	pressLast(&g)
	let _ = ui.Render(&g, fenced, St{A: 1, It: it, Items: []Item{}})
	pressLast(&g)
	let mut h = ui.NewFrame[int]()
	let _ = ui.Render(&h, fencedTwo, St{A: 0, It: it, Items: []Item{}})
	pressLast(&h)
	let _ = ui.Render(&h, fencedTwo, St{A: 1, It: it, Items: []Item{}})
	pressLast(&h)
	for a in 0..3 {
		step(&h, cutKept, St{A: a, It: it, Items: []Item{}}, -1)
	}
}

// checks writes two checks at one site: tops that do not each take the
// first count of their identity, so a group of them is never deferred.
func checks(f *ui.Frame[int], n int) {
	for k in 0..2 {
		ui.Check(f, "c", false, n+k)
	}
}

// fencedTwo is fenced over checks: the disabled state is checked on the
// path that takes each top's count. Mutant ui-group-off-unchecked.
func fencedTwo(f *ui.Frame[int], s St) {
	ui.Fieldset(f, s.A%2 == 1, "") {
		checks(f, 7)
	}
}

// boundShown is a boundary whose body writes shown(f, 1) and, when more,
// a text after it, then stops when stop: a cut that takes back a kept
// group, written again in that pass, whose counts are deferred (and taken
// by the text when more). Two calls of one boundary share their counts.
func boundShown(f *ui.Frame[int], stop bool, more bool) {
	ui.Boundary(f) {
		shown(f, 1)
		if more {
			ui.Text(f, "more")
		}
		try stopIf(stop, 0)
	} catch {
	case Stop:
		ui.Text(f, "stopped")
	}
}

// cutKept is two columns of two boundShown calls; the first of each stops
// when a is 1. In that pass its shown is written again and cut, so the
// second's shown takes the counts its nodes take when nothing is kept.
// Mutants ui-pending-cut-kept and ui-pending-cut-counts-kept.
func cutKept(f *ui.Frame[int], s St) {
	ui.Column(f, ui.Plain()) {
		boundShown(f, s.A == 1, false)
		boundShown(f, false, false)
	}
	ui.Column(f, ui.Plain()) {
		boundShown(f, s.A == 1, true)
		boundShown(f, false, true)
	}
}

// pointers renders the counter at each of ns, through one pointer: a
// component whose argument is a pointer is no group, since an equal
// pointer may reach a changed value.
func pointers(ns []int) needs Console {
	let mut f = ui.NewFrame[int]()
	let mut c = Counter{N: 0}
	for n in ns {
		c.N = n
		let ops = ui.Render(&f, counterView, &c)
		Console.Println("pointer ops \(show(ops))")
		Console.Println(shot(ui.FrameShape(&f), ""))
	}
}

// growth renders n cards, then the same n with one changed, then one:
// the runs of each pass, the nodes Commit compared and the Sames skipped
// after the change (none: the list has no Same), and the groups kept
// after the last.
func growth(n int) needs Console {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, list, listed(n, -1))
	let first = ui.GroupRuns(&f)
	let _ = ui.Render(&f, list, listed(n, n/2))
	let changed = ui.GroupRuns(&f)
	let compared = ui.Compared(&f)
	let skips = ui.Skips(&f)
	let _ = ui.Render(&f, list, listed(1, -1))
	Console.Println("growth \(n) \(first) \(changed) \(compared) \(skips) \(ui.GroupsKept(&f))")
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
	kind := g.r.IntN(16)
	if depth >= 2 && (kind >= 3 && kind <= 5 || kind >= 12 && kind != 15) {
		kind = 0
	}
	switch kind {
	case 15:
		// A record with a slice (NS2b): wideA reads no slice of it, so it
		// is kept on equal It.Text and N; wideB reads the slice, so runs.
		w("wideA(f, Wide{It: it, Tags: rows(a), N: a%%3, M: a})")
		w("wideB(f, Wide{It: it, Tags: rows(a), N: 0, M: 0})")
		w("bagged(f, Bag{Name: it.Text, Tags: rows(a)})")
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
	case 12:
		// A disabled state around what may be kept.
		block("ui.Fieldset(f, a%2 == 1, \"\")")
	case 13:
		// A Same in a component, skipped while a%2 stays: a group that
		// skipped it may hold state its arguments do not say.
		block("ui.Same(f, ui.ByInt(a%2))")
	case 14:
		// A boundary whose body stops on some states, after writing:
		// the groups it wrote are cut with their nodes.
		w("ui.Boundary(f) {")
		g.stmts(i, depth+1, indent+"\t")
		w("\ttry stopIf(a%%3 == %d, a)", g.r.IntN(3))
		w("} catch {")
		w("case Stop:")
		w("\tui.Text(f, \"stopped\")")
		w("}")
	default:
		// Names, selections and constants, and computed arguments (NS2a):
		// arithmetic, a pure call, a record built with an interpolation.
		args := []string{"a, it", "it.Id, it", "3, it", "a+1, it", "a*2%5, it", "len(rows(a)), it", "a%3, Item{Id: it.Id, Text: \"\\(it.Text)\\(a%2)\", Done: it.Done}"}[g.r.IntN(7)]
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
	cards(f, s.Items)
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
// never kept; a press may flip one. Some sit under a Fieldset whose
// disabled state follows the state, in a Same, or in a Boundary whose
// body stops on some states after writing. Two fixed views hold the
// cases a draw seldom reaches: a component whose Same holds a toggle,
// pressed after a pass that skipped the Same, and a component under a
// Fieldset that disables it while its arguments stay, pressed. A component that takes a pointer
// renders a counter the program changes through it, at drawn values: an
// equal pointer must not skip it. A failing seed is replayed with
// -uiskip.seed=N, and the program's text is in the failure.
//
// It also holds that skipping happens, as counted work: a pass that
// returns the same arguments runs fewer components than the first pass,
// and one changed card of n runs one component and Commit compares its
// three nodes alone, at n 8 and 64; the groups kept follow the cards when
// they drop to one (growth); a pass that changes only a header above n
// cards runs none of them and compares no node of theirs, at n 50 and
// 200 (elsewhere, NS1); a card inside a component written again is kept
// with it (nested). After each seed a pass that calls no component holds
// no argument slot (held 0, as the reference holds none). A component
// argument that calls a function counting its calls is evaluated once a
// pass (tallied), and rows whose label is computed are kept when it is
// equal and run once when every label changes, comparing the nodes the
// reference compares (allChange, at 50 and 200). A component whose
// record argument holds a slice it does not read compares the fields it
// reads, through a nested call too (NS2b): generated components read
// subsets of their Item, and subset counts a record kept while its slice
// and an unread field change, and run when a field read through label
// changes. A list holder, cards(f, s.Items), and a record with a nested
// slice, Bag, are groups compared by their structural equality against
// a copy the group keeps (NS2c): each seed's view calls cards over its
// edited, grown, shrunk and shuffled items; cardsGuard counts a header
// change above n cards (no run, the header alone compared, at 50 and 200)
// and one edited card (one run); aliased writes a card in place after a
// pass, which the kept copy must not see.
//
// It builds Go because the subject is the lowered program's behaviour
// over passes and events, which only runs.
//
// Mutants (testdata/mutants.json): ui-group-args-ignored,
// ui-group-impure-kept, ui-group-count-unchecked, ui-group-again-compared,
// ui-group-slots-leak, ui-skip-never, ui-skip-pointer-arg,
// ui-group-off-unchecked, ui-group-same-skip-pure, ui-group-cut-kept,
// ui-group-again-skips, ui-group-fresh-impure-leak, ui-group-cut-slot-leak, ui-group-unsee-short, ui-prior-place-unchecked,
// ui-group-inner-unstamped, ui-group-inner-unlisted, ui-ns2a-arg-twice,
// ui-ns2a-arg-unvalued, ui-ns2a-computed-refused, ui-ns2b-nested-unjoined,
// ui-ns2b-whole-record, ui-ns2b-field-unread, ui-ns2c-off,
// ui-ns2c-same-length, ui-ns2c-keep-aliases, ui-ns2c-fields-unkept,
// ui-prior-place-uncounted, ui-cut-nkids-kept, ui-group-seen-unpassed,
// ui-keep-sweep-always, ui-keep-sweep-never, ui-keep-nheld-drift,
// ui-pending-untaken, ui-pending-each-unchecked, ui-pending-not-alone,
// ui-pending-cut-kept, ui-pending-cut-counts-kept, ui-block-one-node,
// ui-block-unplaced, ui-block-first-top, ui-block-kept-first,
// ui-block-committed-specs, ui-keep-through-never, ui-keep-through-stale,
// ui-keep-inner-uncounted (and dropped: ui-group-inner-unstamped; trailed: ui-block-one-node).
func TestUISkipAgrees(t *testing.T) {
	needsBuild(t)
	var seeds []uint64
	for s := range uint64(32) {
		seeds = append(seeds, s+1)
	}
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
		// A last pass that calls no component frees every slot.
		fmt.Fprintf(&main, "\tlet _ = ui.Render(&f%d, empty, St{A: 0, It: Item{Id: 0, Text: \"\", Done: false}, Items: []Item{}})\n\tConsole.Println(\"held \\(ui.GroupsKept(&f%d))\")\n", seed, seed)
		var ns []string
		for range 4 {
			ns = append(ns, fmt.Sprint(g.r.IntN(3)))
		}
		fmt.Fprintf(&main, "\tpointers([]int{%s})\n", strings.Join(ns, ", "))
	}
	main.WriteString("\tfixed()\n\tgrowth(8)\n\tgrowth(64)\n\telsewhere(50)\n\telsewhere(200)\n\tnested()\n\ttallied()\n\tallChange(50)\n\tallChange(200)\n\tsubset()\n\tcardsGuard(50)\n\tcardsGuard(200)\n\taliased()\n\tloopedTwice()\n\tcutTwice()\n\tshrunk(50)\n\tdropped(50)\n\ttrailed(50)\n}\n")
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
			if !strings.HasPrefix(l, "runs ") && !strings.HasPrefix(l, "growth ") && !strings.HasPrefix(l, "elsewhere ") && !strings.HasPrefix(l, "nested ") && !strings.HasPrefix(l, "allcount ") && !strings.HasPrefix(l, "subset ") && !strings.HasPrefix(l, "cards ") && !strings.HasPrefix(l, "looped ") && !strings.HasPrefix(l, "cut ") && !strings.HasPrefix(l, "shrunk ") && !strings.HasPrefix(l, "dropped ") && !strings.HasPrefix(l, "trailed ") {
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
	var growth, elsewhere []string
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
		case strings.HasPrefix(l, "elsewhere "), strings.HasPrefix(l, "nested "), strings.HasPrefix(l, "allcount "), strings.HasPrefix(l, "subset "), strings.HasPrefix(l, "cards "), strings.HasPrefix(l, "looped "), strings.HasPrefix(l, "cut "), strings.HasPrefix(l, "shrunk "), strings.HasPrefix(l, "dropped "), strings.HasPrefix(l, "trailed "):
			elsewhere = append(elsewhere, l)
		}
	}
	if first == 0 || later >= first*9 {
		t.Errorf("the groups build ran %d components in first passes and %d in the 9 later ones of each seed: no component was skipped", first, later)
	}
	if want := []string{"growth 8 8 1 3 0 1", "growth 64 64 1 3 0 1"}; strings.Join(growth, "|") != strings.Join(want, "|") {
		t.Errorf("growth: %q, want %q (n, first runs, runs after one change, nodes compared then, Sames skipped then, groups kept at one card)", growth, want)
	}
	// NS1: a change elsewhere than the rows runs no row and compares the
	// header and its sibling alone, at 50 rows as at 200.
	// A group written again keeps the groups inside it: the card in framed
	// is kept through it, so framed alone runs when its tag changes.
	// When every row's computed argument changes, each row runs once and
	// Commit compares what the reference compares (allchange, a behaviour
	// line, equal under both builds); a later pass that changes the header
	// alone runs none of them (allcount, NS1 and NS2a).
	// NS2b: a record argument with a slice is a group of the fields its
	// component reads: kept while only the slice or an unread field
	// changes, run when a field read through a nested call changes.
	if want := []string{"elsewhere 50 0 2", "elsewhere 200 0 2", "nested 1", "allcount 50 50 50", "allcount 50 header 0 2", "allcount 200 200 200", "allcount 200 header 0 2", "subset 0 1", "subset 0 1", "subset 1 1", "cards 50 tick 0 1 0 0", "cards 50 edit 1 0", "cards 200 tick 0 1 0 0", "cards 200 edit 1 0", "looped 0", "cut 1", "shrunk 50 50 0", "dropped 50 52 1", "trailed 50 0"}; strings.Join(elsewhere, "|") != strings.Join(want, "|") {
		t.Errorf("counted work: %q, want %q (n, components run, nodes compared; components run in nested; components run and groups kept when every row changed, then after a header change; components run and groups kept in subset)", elsewhere, want)
	}
}
