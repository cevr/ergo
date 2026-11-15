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

type prog = pText{slot int} | pInput{slot int} | pCheck{slot int} | pSwap{slot int} | pBox{slot int, kids []prog} | pRow{kids []prog} | pPress{slot int, kids []prog} | pEach{slot int, kids []prog} | pBound{form int, slot int, kids []prog} | pRead{slot int}

type Missing { V int }

type rng { x int }

func draw(r *rng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

func seeded(seed int) rng { rng{x: (seed%2147483648 + 2147483648) % 2147483648} }

let slots = 6

func genProg(r *rng, depth int, bounded bool) prog {
	let kind = draw(r, 10)
	if kind == 9 && bounded {
		return pRead{slot: draw(r, slots)}
	}
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
		let inner = bounded || kind >= 8
		let mut kids = []prog{}
		for _ in 0..draw(r, 4) {
			kids = kids.Append(genProg(r, depth-1, inner))
		}
		match kind {
		case 4:
			pBox{slot: draw(r, slots), kids: kids}
		case 5:
			pRow{kids: kids}
		case 6:
			pPress{slot: draw(r, slots), kids: kids}
		case 7:
			pEach{slot: draw(r, slots), kids: kids}
		case _:
			pBound{form: draw(r, 3), slot: draw(r, slots), kids: kids.Append(pRead{slot: draw(r, slots)})}
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
		if (k+v/6)%3 != 0 {
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

func render(f *ui.Frame[int], p prog, s []int, row int) fails Missing | ui.Pending {
	match p {
	case pText as x:
		ui.Text(f, "t\(s[x.slot] + row)")
	case pInput as x:
		let v = s[x.slot] + row
		ui.Input(f, "v\(v)", "type", func(t string) int { len(t) + v })
	case pCheck as x:
		ui.Check(f, "c", s[x.slot]%2 == 0, s[x.slot]+row)
	case pSwap as x:
		swap(f, s[x.slot]+row)
	case pBox as x:
		try ui.Box(f, gapped(s[x.slot])) {
			for c in x.kids {
				try render(f, c, s, row)
			}
		}
	case pRow as x:
		try ui.Row(f, ui.Plain()) {
			for c in x.kids {
				try render(f, c, s, row)
			}
		}
		ui.Text(f, "after")
	case pPress as x:
		try ui.Pressable(f, "press", s[x.slot]%4+row) {
			for c in x.kids {
				try render(f, c, s, row)
			}
		}
	case pEach as x:
		try ui.Each(f, items(s[x.slot]), func(k int) ui.Key { ui.ByInt(k) }) func(k int) fails Missing | ui.Pending {
			for c in x.kids {
				try render(f, c, s, k)
			}
		}
		ui.Text(f, "after")
	case pBound as x:
		if x.form == 0 {
			ui.Boundary(f) {
				for c in x.kids {
					try render(f, c, s, row)
				}
			} catch {
			case ui.Pending:
				ui.Text(f, "pending")
			case Missing as e:
				if e.V%2 == 0 {
					ui.Text(f, "missing \(e.V)")
				}
			}
		} else if x.form == 1 {
			ui.Box(f, ui.Plain()) {
				for c in x.kids {
					try render(f, c, s, row)
				}
			} catch {
			case ui.Pending:
				ui.Text(f, "pending")
			case Missing as e:
				if e.V%2 == 0 {
					ui.Text(f, "missing \(e.V)")
				}
			}
		} else {
			ui.Each(f, items(s[x.slot]), func(k int) ui.Key { ui.ByInt(k) }) func(k int) fails Missing | ui.Pending {
				for c in x.kids {
					try render(f, c, s, k)
				}
			} catch {
			case ui.Pending:
				ui.Text(f, "pending")
			case Missing as e:
				if e.V%2 == 0 {
					ui.Text(f, "missing \(e.V)")
				}
			}
		}
	case pRead as x:
		let v = s[x.slot] + row
		if v%3 == 1 {
			return ui.Pending
		}
		if v%3 == 2 {
			return Missing{V: v}
		}
	}
}

type outcome { shapes []ui.Shape[int], fail int, v int }

func done(shapes []ui.Shape[int]) outcome { outcome{shapes: shapes, fail: 0, v: 0} }

func model(p prog, s []int, row int) outcome {
	match p {
	case pText as x:
		done([]ui.Shape[int]{leaf(ui.TextNode{Value: "t\(s[x.slot] + row)", Style: ui.Plain()})})
	case pInput as x:
		done([]ui.Shape[int]{leaf(ui.InputNode{Value: "v\(s[x.slot] + row)", Placeholder: "type"})})
	case pCheck as x:
		done([]ui.Shape[int]{leaf(ui.CheckNode{Label: "c", Checked: s[x.slot]%2 == 0, On: s[x.slot] + row})})
	case pSwap as x:
		let v = s[x.slot] + row
		if v%2 == 0 {
			return done([]ui.Shape[int]{leaf(ui.TextNode{Value: "even \(v)", Style: ui.Plain()})})
		}
		done([]ui.Shape[int]{leaf(ui.CheckNode{Label: "odd", Checked: v%3 == 0, On: v})})
	case pBox as x:
		let o = outcomes(x.kids, s, row)
		if o.fail != 0 {
			return o
		}
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.BoxNode{Style: gapped(s[x.slot])}, Kids: o.shapes}})
	case pRow as x:
		let o = outcomes(x.kids, s, row)
		if o.fail != 0 {
			return o
		}
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: o.shapes}, leaf(ui.TextNode{Value: "after", Style: ui.Plain()})})
	case pPress as x:
		let o = outcomes(x.kids, s, row)
		if o.fail != 0 {
			return o
		}
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.PressableNode{Label: "press", On: s[x.slot]%4 + row, Style: ui.Plain()}, Kids: o.shapes}})
	case pEach as x:
		let mut out = []ui.Shape[int]{}
		for k in items(s[x.slot]) {
			let o = outcomes(x.kids, s, k)
			if o.fail != 0 {
				return o
			}
			for c in o.shapes {
				out = out.Append(c)
			}
		}
		done(out.Append(leaf(ui.TextNode{Value: "after", Style: ui.Plain()})))
	case pBound as x:
		let mut out = []ui.Shape[int]{}
		for k in boundRows(x, s, row) {
			let o = outcomes(x.kids, s, k)
			if o.fail == 1 {
				return done([]ui.Shape[int]{leaf(ui.TextNode{Value: "pending", Style: ui.Plain()})})
			}
			if o.fail == 2 && o.v%2 == 0 {
				return done([]ui.Shape[int]{leaf(ui.TextNode{Value: "missing \(o.v)", Style: ui.Plain()})})
			}
			if o.fail == 2 {
				return done([]ui.Shape[int]{})
			}
			for c in o.shapes {
				out = out.Append(c)
			}
		}
		if x.form == 1 {
			return done([]ui.Shape[int]{ui.Shape[int]{Node: ui.BoxNode{Style: ui.Plain()}, Kids: out}})
		}
		done(out)
	case pRead as x:
		let v = s[x.slot] + row
		if v%3 == 0 {
			return done([]ui.Shape[int]{})
		}
		outcome{shapes: []ui.Shape[int]{}, fail: v % 3, v: v}
	}
}

func outcomes(ps []prog, s []int, row int) outcome {
	let mut out = []ui.Shape[int]{}
	for p in ps {
		let o = model(p, s, row)
		if o.fail != 0 {
			return o
		}
		for x in o.shapes {
			out = out.Append(x)
		}
	}
	done(out)
}

func models(ps []prog, s []int, row int) []ui.Shape[int] { outcomes(ps, s, row).shapes }

func stopsAt(ps []prog, s []int, row int) bool {
	for p in ps {
		if stopIn(p, s, row) {
			return true
		}
	}
	false
}

func stopIn(p prog, s []int, row int) bool {
	match p {
	case pBox as x:
		stopsAt(x.kids, s, row)
	case pRow as x:
		stopsAt(x.kids, s, row)
	case pPress as x:
		stopsAt(x.kids, s, row)
	case pEach as x:
		for k in items(s[x.slot]) {
			if stopsAt(x.kids, s, k) {
				return true
			}
		}
		false
	case pBound as x:
		for k in boundRows(x, s, row) {
			if outcomes(x.kids, s, k).fail != 0 || stopsAt(x.kids, s, k) {
				return true
			}
		}
		false
	case _:
		false
	}
}

func boundRows(x pBound, s []int, row int) []int {
	if x.form == 2 {
		return items(s[x.slot])
	}
	[]int{row}
}

func leaf(n ui.Node[int]) ui.Shape[int] { ui.Shape[int]{Node: n, Kids: []ui.Shape[int]{}} }

func pass(f *ui.Frame[int], ps []prog, s []int) []ui.Op[int] {
	ui.Render(f, func(g *ui.Frame[int], st []int) {
		for p in ps {
			render(g, p, st, 0) catch {
			case Missing:
				Unreachable("ui laws: a read is drawn only under a boundary")
			case ui.Pending:
				Unreachable("ui laws: a read is drawn only under a boundary")
			}
		}
	}, s)
}

func genRoot(r *rng) []prog {
	let mut ps = []prog{}
	for _ in 0..draw(r, 4)+1 {
		ps = ps.Append(genProg(r, 3, false))
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
		if !stopsAt(ps, s, 0) && ui.Retained(&f) != ui.Len(&f) {
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
			ok = i.Value == "v\(typed - 3)" && pressed == -1
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
	"ui IncrementalEqualsFresh": `IncrementalEqualsFresh(seed int, steps int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..(steps%8+8)%8+1 { let s = genState(&r) Apply(&m, pass(&f, ps, s)) catch { case BadPatch: return false } let mut fresh = NewFrame[int]() let mut fm = NewMirror[int](Root(&fresh)) Apply(&fm, pass(&fresh, ps, s)) catch { case BadPatch: return false } let want = models(ps, s, 0) if !SameShapes(FrameShape(&f), want) || !SameShapes(TreeShape(&f), want) || !SameShapes(MirrorShape(&m), want) || !SameShapes(MirrorShape(&fm), want) { return false } if Len(&f) != nodes(want)+1 || Held(&m) != Len(&f) { return false } if !stopsAt(ps, s, 0) && Retained(&f) != Len(&f) { return false } } true }`,
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
