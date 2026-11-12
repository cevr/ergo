package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdUITreeProbe restates each law of std ui (ledger runner TestUITreeModel) as a
// property test of its own name (stdLawsTestName): the laws' helpers over
// the package's exported operations, each law a function the test asserts.
// It is generated from internal/sketch/std/ui/laws.ergo; a law changed in
// source fails TestUITreeModel until this restatement changes with it.
const stdUITreeProbe = `import ui

type prog = pText{slot int} | pInput{slot int} | pCheck{slot int} | pSwap{slot int} | pBox{slot int, kids []prog} | pRow{kids []prog} | pPress{slot int, kids []prog} | pEach{slot int, kids []prog}

type rng { x int }

func draw(r *rng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

func seeded(seed int) rng { rng{x: (seed%2147483648 + 2147483648) % 2147483648} }

let slots = 6

func genProg(r *rng, depth int) prog {
	let kind = draw(r, 8)
	if depth == 0 || kind < 4 {
		match kind % 4 {
		case 0:
			pText{slot: draw(r, slots)}
		case 1:
			pInput{slot: draw(r, slots)}
		case 2:
			pCheck{slot: draw(r, slots)}
		case _:
			pSwap{slot: draw(r, slots)}
		}
	} else {
		let mut kids = []prog{}
		for _ in 0..draw(r, 4) {
			kids = kids.Append(genProg(r, depth-1))
		}
		match kind {
		case 4:
			pBox{slot: draw(r, slots), kids: kids}
		case 5:
			pRow{kids: kids}
		case 6:
			pPress{slot: draw(r, slots), kids: kids}
		case _:
			pEach{slot: draw(r, slots), kids: kids}
		}
	}
}

func genState(r *rng) []int {
	let mut s = []int{}
	for _ in 0..slots {
		s = s.Append(draw(r, 64))
	}
	s
}

func items(v int) []int {
	let mut out = []int{}
	for i in 0..6 {
		let k = (i + v) % 6
		if (k + v/6) % 3 != 0 {
			out = out.Append(k)
		}
	}
	out
}

func gapped(v int) ui.Style {
	let mut st = ui.Plain()
	st.Gap = ui.Length(float64(v % 3))
	st
}

func swap(f *ui.Frame[int], v int, at CallSite) {
	if v%2 == 0 {
		ui.Text(f, "even \(v)", at)
	} else {
		ui.Check(f, "odd", v%3 == 0, v, at)
	}
}

func render(f *ui.Frame[int], p prog, s []int, row int) {
	match p {
	case pText as x:
		ui.Text(f, "t\(s[x.slot]+row)")
	case pInput as x:
		let v = s[x.slot] + row
		ui.Input(f, "v\(v)", "type", func(t string) int { len(t) + v })
	case pCheck as x:
		ui.Check(f, "c", s[x.slot]%2 == 0, s[x.slot]+row)
	case pSwap as x:
		swap(f, s[x.slot]+row)
	case pBox as x:
		ui.Box(f, gapped(s[x.slot])) {
			for c in x.kids {
				render(f, c, s, row)
			}
		}
	case pRow as x:
		ui.Row(f, ui.Plain()) {
			for c in x.kids {
				render(f, c, s, row)
			}
		}
		ui.Text(f, "after")
	case pPress as x:
		ui.Pressable(f, "press", s[x.slot]%4+row) {
			for c in x.kids {
				render(f, c, s, row)
			}
		}
	case pEach as x:
		ui.Each(f, items(s[x.slot]), func(k int) ui.Key { ui.ByInt(k) }) func(k int) {
			for c in x.kids {
				render(f, c, s, k)
			}
		}
		ui.Text(f, "after")
	}
}

func model(p prog, s []int, row int) []ui.Shape[int] {
	match p {
	case pText as x:
		[]ui.Shape[int]{leaf(ui.TextNode{Value: "t\(s[x.slot]+row)", Style: ui.Plain()})}
	case pInput as x:
		[]ui.Shape[int]{leaf(ui.InputNode{Value: "v\(s[x.slot]+row)", Placeholder: "type"})}
	case pCheck as x:
		[]ui.Shape[int]{leaf(ui.CheckNode{Label: "c", Checked: s[x.slot]%2 == 0, On: s[x.slot] + row})}
	case pSwap as x:
		let v = s[x.slot] + row
		if v%2 == 0 {
			return []ui.Shape[int]{leaf(ui.TextNode{Value: "even \(v)", Style: ui.Plain()})}
		}
		[]ui.Shape[int]{leaf(ui.CheckNode{Label: "odd", Checked: v%3 == 0, On: v})}
	case pBox as x:
		[]ui.Shape[int]{ui.Shape[int]{Node: ui.BoxNode{Style: gapped(s[x.slot])}, Kids: models(x.kids, s, row)}}
	case pRow as x:
		[]ui.Shape[int]{ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: models(x.kids, s, row)}, leaf(ui.TextNode{Value: "after", Style: ui.Plain()})}
	case pPress as x:
		[]ui.Shape[int]{ui.Shape[int]{Node: ui.PressableNode{Label: "press", On: s[x.slot]%4 + row, Style: ui.Plain()}, Kids: models(x.kids, s, row)}}
	case pEach as x:
		let mut out = []ui.Shape[int]{}
		for k in items(s[x.slot]) {
			for c in models(x.kids, s, k) {
				out = out.Append(c)
			}
		}
		out.Append(leaf(ui.TextNode{Value: "after", Style: ui.Plain()}))
	}
}

func models(ps []prog, s []int, row int) []ui.Shape[int] {
	let mut out = []ui.Shape[int]{}
	for p in ps {
		for x in model(p, s, row) {
			out = out.Append(x)
		}
	}
	out
}

func leaf(n ui.Node[int]) ui.Shape[int] { ui.Shape[int]{Node: n, Kids: []ui.Shape[int]{}} }

func pass(f *ui.Frame[int], ps []prog, s []int) []ui.Op[int] {
	ui.Render(f, func(g *ui.Frame[int], st []int) {
		for p in ps {
			render(g, p, st, 0)
		}
	}, s)
}

func genRoot(r *rng) []prog {
	let mut ps = []prog{}
	for _ in 0..draw(r, 4)+1 {
		ps = ps.Append(genProg(r, 3))
	}
	ps
}

func nodes(shapes []ui.Shape[int]) int {
	let mut n = 0
	for x in shapes {
		n = n + 1 + nodes(x.Kids)
	}
	n
}

func lawIncrementalEqualsFresh(seed int, steps int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	for _ in 0..(steps%8+8)%8+1 {
		let s = genState(&r)
		ui.Apply(&m, pass(&f, ps, s)) catch {
		case ui.BadPatch:
			return false
		}
		let mut fresh = ui.NewFrame[int]()
		let mut fm = ui.NewMirror[int](ui.Root(&fresh))
		ui.Apply(&fm, pass(&fresh, ps, s)) catch {
		case ui.BadPatch:
			return false
		}
		let want = models(ps, s, 0)
		if !ui.SameShapes(ui.FrameShape(&f), want) || !ui.SameShapes(ui.TreeShape(&f), want) || !ui.SameShapes(ui.MirrorShape(&m), want) || !ui.SameShapes(ui.MirrorShape(&fm), want) {
			return false
		}
		if ui.Len(&f) != nodes(want)+1 || ui.Held(&m) != ui.Len(&f) {
			return false
		}
	}
	true
}

func lawIdempotentCommit(seed int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let s0 = genState(&r)
	let s1 = genState(&r)
	let _ = pass(&f, ps, s0)
	let _ = pass(&f, ps, s1)
	len(pass(&f, ps, s1)) == 0
}

func shuffled(xs []int, r *rng) []int {
	let mut out = xs
	for i in 0..len(out) {
		let j = i + draw(r, len(out)-i)
		let x = out[i]
		out[i] = out[j]
		out[j] = x
	}
	out
}

func keyedRows(f *ui.Frame[int], xs []int) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, xs, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
			ui.Text(f, "row \(x)")
			ui.Pressable(f, "go", x) {
				ui.Text(f, "go")
			}
		}
		ui.Text(f, "end")
	}
}

func rowIds(f *ui.Frame[int], xs []int) Map[int, (ui.Id[int], ui.Id[int])] {
	let mut out = Map[int, (ui.Id[int], ui.Id[int])]{}
	let col = ui.Kids(f, ui.Root(f))
	if len(col) != 1 {
		return out
	}
	let kids = ui.Kids(f, col[0])
	if len(kids) != 2*len(xs)+1 {
		return out
	}
	for i, x in xs {
		out[x] = (kids[2*i], kids[2*i+1])
	}
	out[-1] = (kids[2*len(xs)], kids[2*len(xs)])
	out
}

func lawKeyedIdentity(seed int, count int) bool {
	let mut r = seeded(seed)
	let mut xs = []int{}
	for i in 0..(count%12+12)%12+1 {
		xs = xs.Append(i * 7)
	}
	let ys = shuffled(xs, &r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	ui.Apply(&m, ui.Render(&f, keyedRows, xs)) catch {
	case ui.BadPatch:
		return false
	}
	let before = rowIds(&f, xs)
	let ops = ui.Render(&f, keyedRows, ys)
	ui.Apply(&m, ops) catch {
	case ui.BadPatch:
		return false
	}
	for op in ops {
		match op {
		case ui.Move[int]:
		case _:
			return false
		}
	}
	let after = rowIds(&f, ys)
	if len(before) != len(xs)+1 || len(after) != len(xs)+1 || !ui.SameShapes(ui.MirrorShape(&m), ui.FrameShape(&f)) {
		return false
	}
	for x, ids in before {
		if after.Get(x) != (Some{Value: ids}) {
			return false
		}
	}
	true
}

func sent(o Option[int]) int {
	match o {
	case Some[int] as v:
		v.Value
	case None:
		-1
	}
}

func signalsAgree(f *ui.Frame[int], id ui.Id[int]) bool {
	let mut ok = true
	let pressed = sent(ui.Handle(f, ui.Press[int]{Id: id}).Signal)
	let typed = sent(ui.Handle(f, ui.Type[int]{Id: id, Text: "abc"}).Signal)
	match ui.NodeOf(f, id) {
	case Some[ui.Node[int]] as n:
		match n.Value {
		case ui.PressableNode[int] as p:
			ok = pressed == p.On && typed == -1
		case ui.CheckNode[int] as c:
			ok = pressed == c.On && typed == -1
		case ui.InputNode as i:
			ok = i.Value == "v\(typed-3)" && pressed == -1
		case _:
			ok = pressed == -1 && typed == -1
		}
	case None:
		return false
	}
	for k in ui.Kids(f, id) {
		ok = ok && signalsAgree(f, k)
	}
	ok
}

func lawPressGivesSignal(seed int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let mut ok = true
	for _ in 0..3 {
		let _ = pass(&f, ps, genState(&r))
		for k in ui.Kids(&f, ui.Root(&f)) {
			ok = ok && signalsAgree(&f, k)
		}
	}
	ok
}

test UiIncrementalEqualsFresh(seed int, steps int) {
	assert lawIncrementalEqualsFresh(seed, steps)
}

test UiIdempotentCommit(seed int) {
	assert lawIdempotentCommit(seed)
}

test UiKeyedIdentity(seed int, count int) {
	assert lawKeyedIdentity(seed, count)
}

test UiPressGivesSignal(seed int) {
	assert lawPressGivesSignal(seed)
}
`

// stdUITreeStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUITreeStatements = map[string]string{
	"ui IncrementalEqualsFresh": `IncrementalEqualsFresh(seed int, steps int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..(steps%8+8)%8+1 { let s = genState(&r) Apply(&m, pass(&f, ps, s)) catch { case BadPatch: return false } let mut fresh = NewFrame[int]() let mut fm = NewMirror[int](Root(&fresh)) Apply(&fm, pass(&fresh, ps, s)) catch { case BadPatch: return false } let want = models(ps, s, 0) if !SameShapes(FrameShape(&f), want) || !SameShapes(TreeShape(&f), want) || !SameShapes(MirrorShape(&m), want) || !SameShapes(MirrorShape(&fm), want) { return false } if Len(&f) != nodes(want)+1 || Held(&m) != Len(&f) { return false } } true }`,
	"ui IdempotentCommit":       `IdempotentCommit(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let s0 = genState(&r) let s1 = genState(&r) let _ = pass(&f, ps, s0) let _ = pass(&f, ps, s1) len(pass(&f, ps, s1)) == 0 }`,
	"ui KeyedIdentity":          `KeyedIdentity(seed int, count int) { let mut r = seeded(seed) let mut xs = []int{} for i in 0..(count%12+12)%12+1 { xs = xs.Append(i * 7) } let ys = shuffled(xs, &r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) Apply(&m, Render(&f, keyedRows, xs)) catch { case BadPatch: return false } let before = rowIds(&f, xs) let ops = Render(&f, keyedRows, ys) Apply(&m, ops) catch { case BadPatch: return false } for op in ops { match op { case Move[int]: case _: return false } } let after = rowIds(&f, ys) if len(before) != len(xs)+1 || len(after) != len(xs)+1 || !SameShapes(MirrorShape(&m), FrameShape(&f)) { return false } for x, ids in before { if after.Get(x) != (Some{Value: ids}) { return false } } true }`,
	"ui PressGivesSignal":       `PressGivesSignal(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut ok = true for _ in 0..3 { let _ = pass(&f, ps, genState(&r)) for k in Kids(&f, Root(&f)) { ok = ok && signalsAgree(&f, k) } } ok }`,
}

// TestUITreeModel runs the probe's tests with --seed 1; lawledger.Run holds that
// the probe restates exactly the ledger's ui laws of this runner, each
// with the row's statement, and that each passed. The mutants of
// internal/sketch/std/ui in testdata/mutants.json are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestUITreeModel(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stduitree", stdUITreeProbe, stdUITreeStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
