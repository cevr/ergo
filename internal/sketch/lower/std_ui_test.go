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
import json
import strings

type prog = pText{slot int} | pInput{slot int} | pCheck{slot int} | pSwap{slot int} | pBox{slot int, kids []prog} | pRow{kids []prog} | pPress{slot int, kids []prog} | pEach{slot int, kids []prog} | pBound{form int, slot int, kids []prog} | pRead{slot int} | pFieldset{slot int, kids []prog} | pSame{kids []prog, memo bool}

type Missing { V int }

type rng { x int }

func draw(r mut rng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

func seeded(seed int) rng { rng{x: (seed%2147483648 + 2147483648) % 2147483648} }

let slots = 6

func genProg(r mut rng, depth int, bounded bool) prog {
	let kind = draw(r, 12)
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
		let inner = bounded || kind == 8 || kind == 9
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
		case 10:
			pFieldset{slot: draw(r, slots), kids: kids}
		case 11:
			pSame{kids: kids, memo: true}
		case _:
			pBound{form: draw(r, 3), slot: draw(r, slots), kids: kids.Append(pRead{slot: draw(r, slots)})}
		}
	}
}

func genState(r mut rng) []int {
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

func described(v int) string {
	if v%2 == 0 {
		return "d\(v)"
	}
	""
}

func swap(f mut ui.Frame[int], v int, at CallSite) {
	if v%2 == 0 {
		ui.Text(f, "even \(v)", at)
	} else {
		ui.Check(f, "odd", v%3 == 0, v, at)
	}
}

func render(f mut ui.Frame[int], p prog, s []int, row int) fails Missing | ui.Pending {
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
	case pFieldset as x:
		try ui.Fieldset(f, s[x.slot]%3 == 0, described(s[x.slot])) {
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
	case pSame as x:
		let dig = digest(x.kids, s, row)
		if x.memo {
			try ui.Same(f, ui.ByInt(dig)) {
				try sameBody(f, x.kids, s, row, dig)
			}
		} else {
			try ui.Box(f, ui.Plain()) {
				try sameBody(f, x.kids, s, row, dig)
			}
		}
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

func sameBody(f mut ui.Frame[int], kids []prog, s []int, row int, dig int) fails Missing | ui.Pending {
	ui.OnKey(f, func(k ui.KeyPress) Option[int] { Some{Value: dig} })
	let fl = ui.NewFlag(f, false)
	ui.Toggle(f, fl, "same")
	ui.Box(f, ui.Plain()) {
		let inner = ui.NewFlag(f, false)
		ui.Toggle(f, inner, "inner")
	}
	for c in kids {
		try render(f, c, s, row)
	}
}

func digest(kids []prog, s []int, row int) int {
	let mut used = []bool{}
	for _ in 0..slots {
		used = used.Append(false)
	}
	for c in kids {
		used = reads(c, used)
	}
	let mut d = row
	for i in 0..slots {
		d = d * 64
		if used[i] {
			d = d + s[i]
		}
	}
	d
}

func reads(p prog, used []bool) []bool {
	let mut out = used
	let mut kids = []prog{}
	match p {
	case pText as x:
		out[x.slot] = true
	case pInput as x:
		out[x.slot] = true
	case pCheck as x:
		out[x.slot] = true
	case pSwap as x:
		out[x.slot] = true
	case pRead as x:
		out[x.slot] = true
	case pBox as x:
		out[x.slot] = true
		kids = x.kids
	case pRow as x:
		kids = x.kids
	case pPress as x:
		out[x.slot] = true
		kids = x.kids
	case pEach as x:
		out[x.slot] = true
		kids = x.kids
	case pBound as x:
		out[x.slot] = true
		kids = x.kids
	case pFieldset as x:
		out[x.slot] = true
		kids = x.kids
	case pSame as x:
		kids = x.kids
	}
	for c in kids {
		out = reads(c, out)
	}
	out
}

func unmemo(ps []prog) []prog {
	let mut out = []prog{}
	for p in ps {
		let q = match p {
		case pBox as x:
			pBox{slot: x.slot, kids: unmemo(x.kids)}
		case pRow as x:
			pRow{kids: unmemo(x.kids)}
		case pPress as x:
			pPress{slot: x.slot, kids: unmemo(x.kids)}
		case pEach as x:
			pEach{slot: x.slot, kids: unmemo(x.kids)}
		case pBound as x:
			pBound{form: x.form, slot: x.slot, kids: unmemo(x.kids)}
		case pFieldset as x:
			pFieldset{slot: x.slot, kids: unmemo(x.kids)}
		case pSame as x:
			pSame{kids: unmemo(x.kids), memo: false}
		case _:
			p
		}
		out = out.Append(q)
	}
	out
}

func sameCount(ps []prog, s []int, row int) int {
	let mut n = 0
	for p in ps {
		n = n + match p {
		case pSame:
			1
		case pBox as x:
			sameCount(x.kids, s, row)
		case pRow as x:
			sameCount(x.kids, s, row)
		case pPress as x:
			sameCount(x.kids, s, row)
		case pFieldset as x:
			sameCount(x.kids, s, row)
		case pEach as x:
			let mut c = 0
			for k in items(s[x.slot]) {
				c = c + sameCount(x.kids, s, k)
			}
			c
		case pBound as x:
			boundCount(x, s, row)
		case _:
			0
		}
	}
	n
}

func boundCount(x pBound, s []int, row int) int {
	let mut c = 0
	for k in boundRows(x, s, row) {
		if outcomes(x.kids, s, k).fail != 0 {
			return 0
		}
		c = c + sameCount(x.kids, s, k)
	}
	c
}

type outcome { shapes []ui.Shape[int], fail int, v int }

func done(shapes []ui.Shape[int]) outcome { outcome{shapes: shapes, fail: 0, v: 0} }

func model(p prog, s []int, row int) outcome {
	match p {
	case pText as x:
		done([]ui.Shape[int]{leaf(ui.TextNode{Value: "t\(s[x.slot] + row)", Style: ui.Plain()})})
	case pInput as x:
		done([]ui.Shape[int]{leaf(ui.InputNode{Value: "v\(s[x.slot] + row)", Placeholder: "type", Label: "", Description: "", Invalid: false, Hints: ui.PlainHints, Lines: 1, Caret: len("v\(s[x.slot] + row)"), Anchor: len("v\(s[x.slot] + row)")})})
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
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.PressableNode{Label: "press", On: s[x.slot]%4 + row, Style: ui.PressStyle()}, Kids: o.shapes}})
	case pFieldset as x:
		let o = outcomes(x.kids, s, row)
		if o.fail != 0 {
			return o
		}
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.FieldsetNode{Disabled: s[x.slot]%3 == 0, Description: described(s[x.slot])}, Kids: o.shapes}})
	case pSame as x:
		let o = outcomes(x.kids, s, row)
		if o.fail != 0 {
			return o
		}
		let mut kids = []ui.Shape[int]{leaf(ui.ToggleNode{Label: "same", On: false}), ui.Shape[int]{Node: ui.BoxNode{Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.ToggleNode{Label: "inner", On: false})}}}
		for k in o.shapes {
			kids = kids.Append(k)
		}
		done([]ui.Shape[int]{ui.Shape[int]{Node: ui.BoxNode{Style: ui.Plain()}, Kids: kids}})
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
	case pFieldset as x:
		stopsAt(x.kids, s, row)
	case pSame as x:
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

func pass(f mut ui.Frame[int], ps []prog, s []int) []ui.Op[int] {
	ui.Render(f, func(g mut ui.Frame[int], st []int) {
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

func genRoot(r mut rng) []prog {
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

func shuffled(xs []int, r mut rng) []int {
	let mut out = xs
	for i in 0..len(out) {
		let j = i + draw(r, len(out)-i)
		let x = out[i]
		out[i] = out[j]
		out[j] = x
	}
	out
}

func keyedRows(f mut ui.Frame[int], xs []int) {
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

func rowIds(f mut ui.Frame[int], xs []int) Map[int, (ui.Id[int], ui.Id[int])] {
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

func keyedLines(f mut ui.Frame[int], xs []int) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, xs, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
			ui.Text(f, "line \(x)")
		}
	}
}

func longestRise(xs []int) int {
	let mut best = []int{}
	let mut most = 0
	for i, x in xs {
		let mut b = 1
		for j in 0..i {
			if xs[j] < x && best[j]+1 > b {
				b = best[j] + 1
			}
		}
		best = best.Append(b)
		if b > most {
			most = b
		}
	}
	most
}

func lawKeyedMovesFewest(seed int, count int) bool {
	let mut r = seeded(seed)
	let mut xs = []int{}
	for i in 0..(count%12+12)%12+1 {
		xs = xs.Append(i)
	}
	let ys = shuffled(xs, &r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	ui.Apply(&m, ui.Render(&f, keyedLines, xs)) catch {
	case ui.BadPatch:
		return false
	}
	let ops = ui.Render(&f, keyedLines, ys)
	ui.Apply(&m, ops) catch {
	case ui.BadPatch:
		return false
	}
	let mut moves = 0
	for op in ops {
		match op {
		case ui.Move[int]:
			moves = moves + 1
		case _:
			return false
		}
	}
	ui.SameShapes(ui.MirrorShape(&m), ui.FrameShape(&f)) && moves == len(ys)-longestRise(ys)
}

func lawKeyedIdentity(seed int, count int) bool {
	let mut r = seeded(seed)
	let mut xs = []int{}
	for i in 0..(count%12+12)%12+1 {
		xs = xs.Append(i * 7)
	}
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	ui.Apply(&m, ui.Render(&f, keyedRows, xs)) catch {
	case ui.BadPatch:
		return false
	}
	let before = rowIds(&f, xs)
	for _ in 0..2 {
		let ys = shuffled(xs, &r)
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

func signalsAgree(f mut ui.Frame[int], id ui.Id[int], off bool, ctl int) bool {
	let mut ok = true
	let mut under = off
	let mut below = ctl
	let pressed = sent(ui.Handle(f, ui.Press[int]{Id: id}).Signal)
	let typed = sent(ui.Handle(f, ui.Type[int]{Id: id, Text: "abc"}).Signal)
	match ui.NodeOf(f, id) {
	case Some[ui.Node[int]] as n:
		match n.Value {
		case ui.FieldsetNode as d:
			under = off || d.Disabled
			ok = pressed == ctl && typed == -1
		case ui.PressableNode[int] as p:
			below = if off { -1 } else { p.On }
			ok = pressed == below && typed == -1
		case ui.CheckNode[int] as c:
			below = if off { -1 } else { c.On }
			ok = pressed == below && typed == -1
		case ui.ToggleNode:
			below = -1
			ok = pressed == -1 && typed == -1
		case ui.SemanticNode[int] as x:
			match x.On {
			case Some[int] as o:
				below = if off || x.Access.Disabled { -1 } else { o.Value }
				ok = pressed == below && typed == -1
			case None:
				ok = pressed == ctl && typed == -1
			}
		case ui.InputNode as i:
			if off {
				ok = pressed == ctl && typed == -1
			} else {
				ok = pressed == ctl && i.Value == "v\(typed - 3)"
			}
		case _:
			ok = pressed == ctl && typed == -1
		}
	case None:
		return false
	}
	for k in ui.Kids(f, id) {
		ok = ok && signalsAgree(f, k, under, below)
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
			ok = ok && signalsAgree(&f, k, false, -1)
		}
	}
	ok
}

type seen { node ui.Node[int], a ui.Access, text string }

func accessModel(shapes []ui.Shape[int], off bool, desc string) []seen {
	let mut out = []seen{}
	for sh in shapes {
		let mut o = off
		let mut d = desc
		match sh.Node {
		case ui.FieldsetNode as x:
			o = off || x.Disabled
			if x.Description != "" {
				d = x.Description
			}
		case _:
		}
		out = out.Append(seen{node: sh.Node, a: readAs(sh.Node, o, d), text: textOf(sh)})
		for x in accessModel(sh.Kids, o, d) {
			out = out.Append(x)
		}
	}
	out
}

func readAs(n ui.Node[int], off bool, desc string) ui.Access {
	let base = ui.Access{Role: ui.Generic, Name: "", Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	match n {
	case ui.PressableNode[int] as x:
		ui.Access{Role: ui.Button, Name: x.Label, Disabled: off, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.CheckNode[int] as x:
		ui.Access{Role: ui.Checkbox, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.Checked}, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.ToggleNode as x:
		ui.Access{Role: ui.Switch, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.On}, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.InputNode as x:
		// A label names the field, a placeholder only an unlabelled one;
		// its own description wins over the ui.Fieldset's.
		let mut name = x.Label
		if name == "" {
			name = x.Placeholder
		}
		let mut d = desc
		if x.Description != "" {
			d = x.Description
		}
		ui.Access{Role: ui.TextBox, Name: name, Disabled: off, Description: d, Checked: None, Value: Some{Value: x.Value}, Selected: None, Expanded: None, Pressed: None, Invalid: x.Invalid, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.TextNode as x:
		ui.Access{Role: ui.Static, Name: x.Value, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.FieldsetNode:
		ui.Access{Role: ui.Group, Name: "", Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.AreaNode as x:
		ui.Access{Role: ui.Region, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.CanvasNode as x:
		ui.Access{Role: ui.Image, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case ui.SemanticNode[int] as x:
		let mut a = x.Access
		a.Disabled = x.Access.Disabled || off
		if x.Access.Description == "" {
			a.Description = desc
		}
		a
	case ui.RowsNode as x:
		ui.Access{Role: ui.List, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None, Mixed: false, Current: None, Modal: false, MultiSelectable: false, Range: None, Key: ui.NoKey, Rels: []ui.Rel{}}
	case _:
		base
	}
}

func textOf(sh ui.Shape[int]) string {
	let mut out = ""
	match sh.Node {
	case ui.TextNode as t:
		out = t.Value
	case _:
	}
	for k in sh.Kids {
		out = out + textOf(k)
	}
	out
}

func wants(loc ui.Locator, x seen) bool {
	match loc {
	case ui.WithRole as l:
		x.a.Role == l.Role && (l.Name == "" || x.a.Name == l.Name)
	case ui.WithLabel as l:
		let labelled = x.a.Role == ui.Button || x.a.Role == ui.Checkbox || x.a.Role == ui.Switch || x.a.Role == ui.Region || x.a.Role == ui.Image || x.a.Role == ui.List
		match x.node {
		case ui.InputNode as i:
			i.Label != "" && i.Label == l.Label
		case ui.SemanticNode[int] as s:
			s.Access.Name == l.Label
		case _:
			labelled && x.a.Name == l.Label
		}
	case ui.WithPlaceholder as l:
		match x.node {
		case ui.InputNode as i:
			i.Placeholder == l.Text
		case _:
			false
		}
	case ui.WithText as l:
		x.a.Role == ui.Static && x.a.Name == l.Text
	case ui.WithZone:
		// The generated programs write no zone.
		false
	case ui.First as l:
		// ui.LocateIn takes the first of what l.At finds.
		wants(l.At, x)
	case ui.Within:
		// The generated programs write no ui.Within.
		false
	}
}

func matches(m mut ui.Mirror[int], loc ui.Locator, is ui.Matcher) bool {
	ui.ExpectIn(m, loc, is) catch {
	case ui.Mismatch:
		return false
	}
	true
}

func extras(f mut ui.Frame[int], v int) {
	ui.Fieldset(f, v%2 == 1, "x\(v)") {
		let on = ui.NewFlag(f, true)
		ui.Toggle(f, on, "tg\(v % 3)")
		ui.LabelledInput(f, extraLabel(v), "val", "ph\(v % 2)", func(t string) int { len(t) })
		ui.Area(f, "area\(v % 2)", ui.Plain(), func(p ui.PointerEvent) Option[int] { None }) {
			ui.Text(f, "in area")
		}
		ui.Canvas(f, "c\(v % 4)", 4, 4) func(p mut ui.Paint) {}
		ui.Rows(f, "rows", 0, 10, func(i int) ui.Key { ui.ByInt(i) }) func(i int) {}
		ui.Semantic(f, extraSem(v), None, ui.Plain()) {
			ui.Text(f, "in sem")
		}
	}
}

func extraLabel(v int) ui.FieldLabel {
	let mut l = ui.FieldLabel{Text: "lab\(v % 3)", Description: "", Invalid: v%3 == 0}
	if v%4 == 0 {
		l.Text = ""
	}
	if v%5 < 2 {
		l.Description = "own\(v % 2)"
	}
	l
}

// extraRoles is every role (UG2).
func extraRoles() []ui.Role {
	[]ui.Role{ui.Button, ui.Checkbox, ui.Switch, ui.TextBox, ui.Static, ui.Group, ui.Region, ui.Image, ui.List, ui.Status, ui.Generic, ui.Tab, ui.TabList, ui.TabPanel, ui.Dialog, ui.Menu, ui.MenuItem, ui.Tree, ui.TreeItem, ui.Heading, ui.Alert, ui.Link, ui.Table, ui.TableRow, ui.TableCell, ui.RadioGroup, ui.Radio, ui.ListBox, ui.ListOption, ui.Slider, ui.SpinButton, ui.SearchBox, ui.MenuItemCheckbox, ui.ComboBox, ui.Grid, ui.GridCell, ui.ColumnHeader, ui.RowHeader, ui.Navigation, ui.ListItem, ui.Separator, ui.Tooltip}
}

// extraSem is extras' Semantic: a role, name, state, key and relation v
// draws (UG2).
func extraSem(v int) ui.Access {
	let roles = extraRoles()
	let mut a = ui.Named(roles[v%len(roles)], "sem\(v % 3)")
	a.Selected = Some{Value: v%2 == 0}
	a.Key = ui.ByInt(v % 3)
	a.Rels = []ui.Rel{ui.Controls{To: ui.ByInt(v % 2)}}
	a
}

func extraShapes(v int) []ui.Shape[int] {
	let l = extraLabel(v)
	let kids = []ui.Shape[int]{leaf(ui.ToggleNode{Label: "tg\(v % 3)", On: true}), leaf(ui.InputNode{Value: "val", Placeholder: "ph\(v % 2)", Label: l.Text, Description: l.Description, Invalid: l.Invalid, Hints: ui.PlainHints, Lines: 1, Caret: len("val"), Anchor: len("val")}), ui.Shape[int]{Node: ui.AreaNode{Label: "area\(v % 2)", Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: "in area", Style: ui.Plain()})}}, leaf(ui.CanvasNode{Label: "c\(v % 4)", Width: 4, Height: 4, Paint: []ui.PaintOp{}}), leaf(ui.RowsNode{Label: "rows", Count: 0, RowHeight: 10, First: 0, Offset: 0}), ui.Shape[int]{Node: ui.SemanticNode{Access: extraSem(v), On: None, Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: "in sem", Style: ui.Plain()})}}}
	[]ui.Shape[int]{ui.Shape[int]{Node: ui.FieldsetNode{Disabled: v%2 == 1, Description: "x\(v)"}, Kids: kids}}
}

// TREE-JSON: laws_tree.ergo restated.
// ActsReadAsWritten: a drawn list of acts (taps, typing, keys, settles,
// advances in milliseconds and scrolls, with labels that need escaping)
// written as the JSON act list ParseActs documents reads back as the
// same acts, in order.
func lawActsReadAsWritten(seed int) bool {
	let mut r = seeded(seed)
	let labels = []string{"inc", "by 5", "say \"hi\"", "a\\b", "名前", ""}
	let mut want = []string{}
	let t = json.NewText()
	let n = draw(&r, 8)
	t.BeginList(n)
	for _ in 0..n {
		let label = labels[draw(&r, len(labels))]
		let k = draw(&r, 6)
		t.BeginObject(1)
		if k == 0 {
			t.Key("tap")
			t.String(label)
			want = want.Append(actWhole(ui.TapOn{Label: label}))
		} else if k == 1 {
			let text = labels[draw(&r, len(labels))]
			t.Key("type")
			t.BeginList(2)
			t.String(label)
			t.String(text)
			t.EndList()
			want = want.Append(actWhole(ui.TypeInto{Label: label, Text: text}))
		} else if k == 2 {
			t.Key("key")
			t.String(label)
			want = want.Append(actWhole(ui.KeyIn{Press: ui.KeyPress{Name: label, Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}))
		} else if k == 3 {
			t.Key("settle")
			t.Bool(true)
			want = want.Append(actWhole(ui.SettleAll))
		} else if k == 4 {
			let ms = draw(&r, 5000)
			t.Key("advance")
			t.Int(int64(ms))
			want = want.Append(actWhole(ui.AdvanceBy{By: duration(int64(ms) * int64(1000000))}))
		} else {
			let offset = float64(draw(&r, 400)) / 4.0
			let height = float64(draw(&r, 400) + 1)
			t.Key("scroll")
			t.BeginList(3)
			t.String(label)
			t.Float(offset)
			t.Float(height)
			t.EndList()
			want = want.Append(actWhole(ui.ScrollOn{Label: label, Offset: ui.Length(offset), Height: ui.Length(height)}))
		}
		t.EndObject()
	}
	t.EndList()
	let got = ui.ParseActs(t.Written()) catch {
	case ui.BadScript:
		return false
	}
	if len(got) != len(want) {
		return false
	}
	for i, a in got {
		if actWhole(a) != want[i] {
			return false
		}
	}
	true
}

// actWhole is an act a script can hold, written out whole.
func actWhole(a ui.Act) string {
	match a {
	case ui.TapOn as x:
		"tap \(x.Label)"
	case ui.TypeInto as x:
		"type \(x.Label)|\(x.Text)"
	case ui.KeyIn as x:
		"key \(x.Press.Name) \(x.Press.Mods.Shift) \(x.Press.Mods.Ctrl) \(x.Press.Mods.Alt) \(x.Press.Mods.Meta)"
	case ui.SettleAll:
		"settle"
	case ui.AdvanceBy as x:
		"advance \(int64(x.By))"
	case ui.ScrollOn as x:
		"scroll \(x.Label) \(float64(x.Offset)) \(float64(x.Height))"
	case _:
		"other"
	}
}

// TreeAgreesWithLocate: over drawn screens (the generator of
// LocatorAgreesWithAccessTree), TreeOf is the screen's nodes that are not
// Generic, in tree order, each the node WithRole finds there with its role,
// name and states as AccessOf reads them (disabled, invalid, checked or
// unchecked, value=V as the model's access has them), and the box the host
// gave that node; TreeLine writes each as a JSON object of those fields.
func lawTreeAgreesWithLocate(seed int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	for _ in 0..3 {
		let s = genState(&r)
		let ops = ui.Render(&f, func(g mut ui.Frame[int], st []int) {
			for p in ps {
				render(g, p, st, 0) catch {
				case Missing:
					Unreachable("ui laws: a read is drawn only under a boundary")
				case ui.Pending:
					Unreachable("ui laws: a read is drawn only under a boundary")
				}
			}
			extras(g, st[0])
		}, s)
		ui.Apply(&m, ops) catch {
		case ui.BadPatch:
			return false
		}
		let mut shapes = models(ps, s, 0)
		for x in extraShapes(s[0]) {
			shapes = shapes.Append(x)
		}
		let mut want = []seen{}
		for x in accessModel(shapes, false, "") {
			match x.a.Role {
			case ui.Generic:
			case _:
				want = want.Append(x)
			}
		}
		// The host's boxes: each node a role finds, the k-th of its role at
		// x = k, y = the role's name's length.
		let mut boxes = Map[ui.Id[int], ui.Rect]{}
		let mut ids = []ui.Id[int]{}
		let mut taken = Map[string, int]{}
		for x in want {
			let role = ui.RoleName(x.a.Role)
			let k = match taken.Get(role) {
			case Some[int] as n:
				n.Value
			case None:
				0
			}
			taken[role] = k + 1
			let found = ui.LocateIn(&m, ui.WithRole{Role: x.a.Role, Name: ""})
			if k >= len(found) {
				return false
			}
			ids = ids.Append(found[k])
			boxes[found[k]] = ui.Rect{X: ui.Length(float64(k)), Y: ui.Length(float64(len(role))), W: 1, H: 1}
		}
		let tree = ui.TreeOf(&m, boxes)
		if len(tree) != len(want) {
			return false
		}
		for i, e in tree {
			let a = ui.AccessOf(&m, ids[i])
			if e.Role != ui.RoleName(want[i].a.Role) || e.Name != a.Name || a != want[i].a || strings.Join(e.States, ",") != strings.Join(ui.StatesOf(a), ",") {
				return false
			}
			// The states the model screen's access has, as ARIA names them.
			let states = "," + strings.Join(e.States, ",") + ","
			let checked = match a.Checked {
			case Some[bool] as c:
				if c.Value { ",checked," } else { ",unchecked," }
			case None:
				""
			}
			let value = match a.Value {
			case Some[string] as v:
				",value=\(v.Value),"
			case None:
				""
			}
			if strings.Contains(states, ",disabled,") != a.Disabled || strings.Contains(states, ",invalid,") != a.Invalid || checked != "" && !strings.Contains(states, checked) || checked == "" && (strings.Contains(states, ",checked,") || strings.Contains(states, ",unchecked,")) || value != "" && !strings.Contains(states, value) {
				return false
			}
			match (e.Box, boxes.Get(ids[i])) {
			case (Some[ui.Rect] as b, Some[ui.Rect] as w):
				if b.Value != w.Value {
					return false
				}
			case _:
				return false
			}
			let line = ui.TreeLine(e)
			if !strings.HasPrefix(line, "{\"role\":\"\(e.Role)\",\"name\":") || !strings.Contains(line, ",\"states\":[") || !strings.Contains(line, ",\"box\":[") {
				return false
			}
		}
	}
	true
}

func lawLocatorAgreesWithAccessTree(seed int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	for _ in 0..3 {
		let s = genState(&r)
		let ops = ui.Render(&f, func(g mut ui.Frame[int], st []int) {
			for p in ps {
				render(g, p, st, 0) catch {
				case Missing:
					Unreachable("ui laws: a read is drawn only under a boundary")
				case ui.Pending:
					Unreachable("ui laws: a read is drawn only under a boundary")
				}
			}
			extras(g, st[0])
		}, s)
		ui.Apply(&m, ops) catch {
		case ui.BadPatch:
			return false
		}
		let mut shapes = models(ps, s, 0)
		for x in extraShapes(s[0]) {
			shapes = shapes.Append(x)
		}
		let want = accessModel(shapes, false, "")
		let mut locs = []ui.Locator{}
		for role in extraRoles() {
			locs = locs.Append(ui.WithRole{Role: role, Name: ""})
		}
		let w = want[draw(&r, len(want))]
		locs = locs.Append(ui.WithRole{Role: w.a.Role, Name: w.a.Name})
		locs = locs.Append(ui.WithLabel{Label: w.a.Name})
		locs = locs.Append(ui.WithPlaceholder{Text: w.a.Name})
		locs = locs.Append(ui.WithText{Text: w.a.Name})
		locs = locs.Append(ui.WithLabel{Label: extraLabel(s[0]).Text})
		locs = locs.Append(ui.WithPlaceholder{Text: "ph\(s[0] % 2)"})
		for loc in locs {
			let mut model = []seen{}
			for x in want {
				if wants(loc, x) {
					model = model.Append(x)
				}
			}
			let got = ui.LocateIn(&m, loc)
			if len(got) != len(model) {
				return false
			}
			for i, id in got {
				if ui.AccessOf(&m, id) != model[i].a {
					return false
				}
			}
			if matches(&m, loc, ui.ToHaveCount{Count: len(model)}) != true || matches(&m, loc, ui.ToHaveCount{Count: len(model) + 1}) || matches(&m, loc, ui.ToHaveCount{Count: len(model) - 1}) || matches(&m, loc, ui.ToExist) != (len(model) > 0) || matches(&m, loc, ui.ToBeAbsent) != (len(model) == 0) {
				return false
			}
			if len(model) > 0 {
				let x = model[0]
				let checked = x.a.Checked == (Some{Value: true})
				let unchecked = x.a.Checked == (Some{Value: false})
				if matches(&m, loc, ui.ToBeDisabled) != x.a.Disabled || matches(&m, loc, ui.ToBeEnabled) == x.a.Disabled || matches(&m, loc, ui.ToBeChecked) != checked || matches(&m, loc, ui.ToBeUnchecked) != unchecked || matches(&m, loc, ui.ToBeInvalid) != x.a.Invalid {
					return false
				}
				if !matches(&m, loc, ui.ToHaveDescription{Text: x.a.Description}) || matches(&m, loc, ui.ToHaveDescription{Text: x.a.Description + "!"}) {
					return false
				}
				if !matches(&m, loc, ui.ToContainText{Text: x.text}) || !matches(&m, loc, ui.ToContainText{Text: ""}) || matches(&m, loc, ui.ToContainText{Text: x.text + "!"}) {
					return false
				}
				match x.a.Value {
				case Some[string] as v:
					if !matches(&m, loc, ui.ToHaveValue{Value: v.Value}) || matches(&m, loc, ui.ToHaveValue{Value: v.Value + "!"}) {
						return false
					}
				case None:
					if matches(&m, loc, ui.ToHaveValue{Value: ""}) {
						return false
					}
				}
			}
		}
	}
	true
}

func opsEqual(a []ui.Op[int], b []ui.Op[int]) bool {
	if len(a) != len(b) {
		return false
	}
	for i in 0..len(a) {
		if a[i] != b[i] {
			return false
		}
	}
	true
}

func signalsMatch(f mut ui.Frame[int], g mut ui.Frame[int], a ui.Id[int], b ui.Id[int]) bool {
	if ui.NodeOf(f, a) != ui.NodeOf(g, b) {
		return false
	}
	match ui.NodeOf(f, a) {
	case Some[ui.Node[int]] as n:
		match n.Value {
		case ui.ToggleNode:
		case _:
			if sent(ui.Handle(f, ui.Press[int]{Id: a}).Signal) != sent(ui.Handle(g, ui.Press[int]{Id: b}).Signal) {
				return false
			}
		}
	case None:
		return false
	}
	if sent(ui.Handle(f, ui.Type[int]{Id: a, Text: "abc"}).Signal) != sent(ui.Handle(g, ui.Type[int]{Id: b, Text: "abc"}).Signal) {
		return false
	}
	let ka = ui.Kids(f, a)
	let kb = ui.Kids(g, b)
	if len(ka) != len(kb) {
		return false
	}
	for i in 0..len(ka) {
		if !signalsMatch(f, g, ka[i], kb[i]) {
			return false
		}
	}
	true
}

func toggles(f mut ui.Frame[int], id ui.Id[int], out []ui.Id[int]) []ui.Id[int] {
	let mut all = out
	match ui.NodeOf(f, id) {
	case Some[ui.Node[int]] as n:
		match n.Value {
		case ui.ToggleNode:
			all = all.Append(id)
		case _:
		}
	case None:
	}
	for k in ui.Kids(f, id) {
		all = toggles(f, k, all)
	}
	all
}

func lawSameEqualsRecompute(seed int, steps int) bool {
	let mut r = seeded(seed)
	// revived: a ui.Same in a ui.Box in a boundary whose read, of a slot the
	// ui.Same's key does not hold, fails and passes in turn, so the ui.Box is
	// parked and comes back while the ui.Same's key stays.
	let revived = pBound{form: 0, slot: 0, kids: []prog{pBox{slot: 1, kids: []prog{pSame{kids: []prog{pText{slot: 2}}, memo: true}}}, pRead{slot: 3}}}
	let ps = genRoot(&r).Append(revived)
	let qs = unmemo(ps)
	let mut f = ui.NewFrame[int]()
	let mut g = ui.NewFrame[int]()
	let mut s = genState(&r)
	let mut again = false
	let key = ui.KeyDown{Press: ui.KeyPress{Name: "k", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}
	for _ in 0..(steps%8+8)%8+2 {
		let a = pass(&f, ps, s)
		let b = pass(&g, qs, s)
		if !opsEqual(a, b) || !ui.SameShapes(ui.FrameShape(&f), ui.FrameShape(&g)) || !ui.SameShapes(ui.TreeShape(&f), ui.TreeShape(&g)) {
			return false
		}
		if again && ui.Skips(&f) != sameCount(ps, s, 0) {
			return false
		}
		if sent(ui.Handle(&f, key).Signal) != sent(ui.Handle(&g, key).Signal) {
			return false
		}
		let ka = ui.Kids(&f, ui.Root(&f))
		let kb = ui.Kids(&g, ui.Root(&g))
		if len(ka) != len(kb) {
			return false
		}
		for i in 0..len(ka) {
			if !signalsMatch(&f, &g, ka[i], kb[i]) {
				return false
			}
		}
		again = false
		let pick = draw(&r, 4)
		if pick == 0 {
			again = true
		} else if pick == 1 {
			let ta = toggles(&f, ui.Root(&f), []ui.Id[int]{})
			let tb = toggles(&g, ui.Root(&g), []ui.Id[int]{})
			if len(ta) != len(tb) {
				return false
			}
			if len(ta) > 0 {
				let i = draw(&r, len(ta))
				if ui.Handle(&f, ui.Press[int]{Id: ta[i]}) != ui.Handle(&g, ui.Press[int]{Id: tb[i]}) {
					return false
				}
			}
		} else if pick == 2 {
			s[draw(&r, slots)] = draw(&r, 64)
		} else {
			s[3] = draw(&r, 3)
		}
	}
	true
}

func lawMirrorWorkIsLinear(n int, seed int) bool {
	let k = (n%40+40)%40 + 1
	let mut reads = []int{}
	for size in []int{k, 2 * k, 4 * k} {
		let mut xs = []int{}
		for i in 0..size {
			xs = xs.Append(i)
		}
		let view = func(f mut ui.Frame[int], rows []int) {
			ui.Column(f, ui.Plain()) {
				ui.Each(f, rows, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
					ui.Text(f, "row \(x)")
				}
			}
		}
		let mut f = ui.NewFrame[int]()
		let mut m = ui.NewMirror[int](ui.Root(&f))
		ui.Apply(&m, ui.Render(&f, view, xs)) catch {
		case ui.BadPatch:
			return false
		}
		reads = reads.Append(ui.MirrorReads(&m))
		let before = ui.MirrorReads(&m)
		let found = ui.LocateIn(&m, ui.WithText{Text: "row \((seed%size + size) % size)"})
		if len(found) != 1 || ui.MirrorReads(&m)-before != ui.Held(&m) {
			return false
		}
	}
	reads[2]-reads[1] == 2*(reads[1]-reads[0])
}

func lawMirrorRejectsBadPatches(a int, b int) bool {
	let na = (a%5+5)%5 + 1
	let nb = (b%5+5)%5 + 1
	let mut xs = []int{}
	let mut ys = []int{}
	let mut more = []int{}
	for i in 0..na {
		xs = xs.Append(i)
		more = more.Append(i)
	}
	for i in 0..nb {
		ys = ys.Append(100 + i)
	}
	xs = xs.Append(99)
	more = more.Append(50)
	more = more.Append(51)
	let zs = []int{200, 201}
	let mut f = ui.NewFrame[int]()
	let root = ui.Root(&f)
	let first = ui.Render(&f, badPatchView, [][]int{xs, ys, zs})
	let second = ui.Render(&f, badPatchView, [][]int{more, ys, zs})
	let rows = insertsOf(first, "row 0")
	let others = insertsOf(first, "row 100")
	let gones = insertsOf(first, "row 99")
	let zrows = insertsOf(first, "row 200")
	let freshes = insertsOf(second, "row 50")
	let adds = insertsOf(second, "row 51")
	if len(rows) != 1 || len(others) != 1 || len(gones) != 1 || len(zrows) != 1 || len(freshes) != 1 || len(adds) != 1 {
		return false
	}
	let row = rows[0]
	let other = others[0]
	let fresh = freshes[0]
	let lc = row.Parent
	let rc = other.Parent
	if len(insertsUnder(first, lc)) != na+1 || len(insertsUnder(first, rc)) != nb || len(insertsUnder(first, root)) != 3 {
		return false
	}
	let text = ui.TextNode{Value: "x", Style: ui.Plain()}
	let box = ui.BoxNode{Style: ui.Plain()}
	// The valid ops before each bad one: a ui.Set, a ui.Move, an ui.Insert, and a
	// ui.Remove of a row and of a whole column.
	let good = []ui.Op[int]{
		ui.Set[int]{Id: other.Id, Node: ui.TextNode{Value: "changed", Style: ui.Plain()}},
		ui.Move[int]{Id: other.Id, Parent: rc, Before: None},
		ui.Insert[int]{Id: adds[0].Id, Parent: lc, Before: Some{Value: row.Id}, Node: adds[0].Node},
		ui.Remove[int]{Id: gones[0].Id},
		ui.Remove[int]{Id: zrows[0].Parent},
	}
	let bad = []ui.Op[int]{
		row,
		ui.Insert[int]{Id: fresh.Id, Parent: fresh.Id, Before: None, Node: text},
		ui.Insert[int]{Id: fresh.Id, Parent: lc, Before: Some{Value: fresh.Id}, Node: text},
		ui.Insert[int]{Id: fresh.Id, Parent: lc, Before: Some{Value: other.Id}, Node: text},
		ui.Move[int]{Id: row.Id, Parent: rc, Before: None},
		ui.Move[int]{Id: row.Id, Parent: lc, Before: Some{Value: row.Id}},
		ui.Move[int]{Id: row.Id, Parent: lc, Before: Some{Value: other.Id}},
		ui.Move[int]{Id: fresh.Id, Parent: lc, Before: None},
		ui.Remove[int]{Id: root},
		ui.Remove[int]{Id: fresh.Id},
		ui.Remove[int]{Id: gones[0].Id},
		ui.Set[int]{Id: fresh.Id, Node: text},
		ui.Set[int]{Id: row.Id, Node: box},
	}
	for op in bad {
		if !badPatchRejected(root, first, good, op) {
			return false
		}
	}
	// The second pass fits.
	let mut m = ui.NewMirror[int](root)
	ui.Apply(&m, first) catch {
	case ui.BadPatch:
		return false
	}
	ui.Apply(&m, second) catch {
	case ui.BadPatch:
		return false
	}
	true
}

func badPatchView(f mut ui.Frame[int], cols [][]int) {
	for c in cols {
		ui.Column(f, ui.Plain()) {
			ui.Each(f, c, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
				ui.Text(f, "row \(x)")
			}
		}
	}
}

func badPatchRejected(root ui.Id[int], base []ui.Op[int], good []ui.Op[int], bad ui.Op[int]) bool {
	let mut m = ui.NewMirror[int](root)
	ui.Apply(&m, base) catch {
	case ui.BadPatch:
		return false
	}
	let shape = ui.MirrorShape(&m)
	let held = ui.Held(&m)
	let mut failed = false
	ui.Apply(&m, good.Append(bad)) catch {
	case ui.BadPatch:
		failed = true
	}
	if !failed || !ui.SameShapes(ui.MirrorShape(&m), shape) || ui.Held(&m) != held {
		return false
	}
	let mut w = ui.NewMirror[int](root)
	ui.Apply(&w, base) catch {
	case ui.BadPatch:
		return false
	}
	ui.Apply(&w, good) catch {
	case ui.BadPatch:
		return false
	}
	ui.Apply(&m, good) catch {
	case ui.BadPatch:
		return false
	}
	ui.SameShapes(ui.MirrorShape(&m), ui.MirrorShape(&w)) && ui.Held(&m) == ui.Held(&w)
}

func insertsUnder(ops []ui.Op[int], p ui.Id[int]) []ui.Insert[int] {
	let mut out = []ui.Insert[int]{}
	for op in ops {
		match op {
		case ui.Insert[int] as o:
			if o.Parent == p {
				out = out.Append(o)
			}
		case _:
		}
	}
	out
}

func insertsOf(ops []ui.Op[int], text string) []ui.Insert[int] {
	let mut out = []ui.Insert[int]{}
	for op in ops {
		match op {
		case ui.Insert[int] as o:
			if o.Node == (ui.TextNode{Value: text, Style: ui.Plain()}) {
				out = out.Append(o)
			}
		case _:
		}
	}
	out
}

func lawCountsSumToPatch(seed int, steps int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r).Append(pBox{slot: 1, kids: []prog{pSame{kids: []prog{pText{slot: 2}, pText{slot: 3}}, memo: true}}})
	let mut f = ui.NewFrame[int]()
	let mut kept = 0
	let mut s = []int{}
	for i in 0..(steps%8+8)%8+2 {
		// The kept ui.Same reads slots 2 and 3, which stay; a repeated state keeps each component group.
		if i%2 == 0 {
			s = genState(&r)
			s[2] = 7
			s[3] = 9
		}
		let ops = pass(&f, ps, s)
		let c = ui.PassCounts(&f)
		let mut ins = 0
		let mut mov = 0
		let mut rem = 0
		let mut set = 0
		for op in ops {
			match op {
			case ui.Insert[int]:
				ins = ins + 1
			case ui.Move[int]:
				mov = mov + 1
			case ui.Remove[int]:
				rem = rem + 1
			case ui.Set[int]:
				set = set + 1
			case ui.Define:
			}
		}
		if c.Inserted != ins || c.Moved != mov || c.Removed != rem || c.Set != set || ins+mov+rem+set != len(ops) {
			return false
		}
		if c.Ran+c.Kept != ui.Len(&f)-1 || c.Visits != ui.Len(&f)-c.Kept-ui.Skips(&f) || c.GroupsKept < ui.Skips(&f) {
			return false
		}
		kept = kept + c.Kept
	}
	kept > 0
}

func lawCountsLineText() bool {
	let c = ui.StepCounts{Inserted: 0, Moved: 0, Removed: 0, Set: 1, Ran: 3, Kept: 801, GroupsRan: 0, GroupsKept: 200, Visits: 3}
	let d = ui.StepCounts{Inserted: 2, Moved: 0, Removed: 1, Set: 0, Ran: 2, Kept: 0, GroupsRan: 1, GroupsKept: 0, Visits: 4}
	ui.CountsLine(c) == "1 set; ran 3 nodes, kept 801; groups ran 0, kept 200; visits 3" &&
		ui.CountsLine(d) == "2 insert, 1 remove; ran 2 nodes, kept 0; groups ran 1, kept 0; visits 4" &&
		ui.CountsLine(ui.NoCounts()) == "no ops; ran 0 nodes, kept 0; groups ran 0, kept 0; visits 0"
}

func lawSlowNamesItsCounter(seed int) bool {
	let mut r = seeded(seed)
	let n = func() int { draw(&r, 9) }
	let s = ui.Stats{Signals: n(), Written: n(), Rows: n(), Skipped: n(), Stopped: n(), Committed: n(), Inserted: n(), Moved: n(), Removed: n(), Set: n(), Painted: n(), Fetches: n(), Cancels: n()}
	let b = ui.Budget{Signals: n(), Written: n(), Committed: n(), Ops: n() * 3, Painted: n(), Fetches: n()}
	let all = []ui.Counter{ui.SignalsCounter, ui.WrittenCounter, ui.CommittedCounter, ui.OpsCounter, ui.PaintedCounter, ui.FetchesCounter}
	let names = []string{"Signals", "Written", "Committed", "Ops", "Painted", "Fetches"}
	let mut want = []ui.Slow{}
	for i, c in all {
		if ui.CounterText(c) != names[i] {
			return false
		}
		let bound = slowBound(c, b)
		let count = slowCount(c, s)
		if bound > 0 && count > bound {
			want = want.Append(ui.Slow{Counter: c, Count: count, Bound: bound})
		}
	}
	ui.SlowsOf(s, b) == want
}

func slowBound(c ui.Counter, b ui.Budget) int {
	match c {
	case ui.SignalsCounter:
		b.Signals
	case ui.WrittenCounter:
		b.Written
	case ui.CommittedCounter:
		b.Committed
	case ui.OpsCounter:
		b.Ops
	case ui.PaintedCounter:
		b.Painted
	case ui.FetchesCounter:
		b.Fetches
	}
}

func slowCount(c ui.Counter, s ui.Stats) int {
	match c {
	case ui.SignalsCounter:
		s.Signals
	case ui.WrittenCounter:
		s.Written
	case ui.CommittedCounter:
		s.Committed
	case ui.OpsCounter:
		s.Inserted + s.Moved + s.Removed + s.Set
	case ui.PaintedCounter:
		s.Painted
	case ui.FetchesCounter:
		s.Fetches
	}
}

func lawKeyedReorderKeepsRows(seed int, steps int) bool {
	let orders = reorders(seed, steps)
	let mut r = seeded(steps)
	let view = func(f mut ui.Frame[int], k int) {
		reorderTable(f, seed, steps, k)
	}
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	for k in 0..len(orders) {
		let mut want = []string{}
		for x in orders[k] {
			want = want.Append("row \(x)")
		}
		for _ in 0..draw(&r, 2)+1 {
			ui.Apply(&m, ui.Render(&f, view, k)) catch {
			case ui.BadPatch:
				return false
			}
			if shownTexts(ui.MirrorShape(&m)) != want {
				return false
			}
		}
	}
	true
}

func shownTexts(shapes []ui.Shape[int]) []string {
	let mut out = []string{}
	for s in shapes {
		match s.Node {
		case ui.TextNode as x:
			out = out.Append(x.Value)
		case _:
		}
		for x in shownTexts(s.Kids) {
			out = out.Append(x)
		}
	}
	out
}

func reorders(seed int, steps int) [][]int {
	let mut r = seeded(seed)
	let mut order = []int{}
	for i in 0..draw(&r, 5)+2 {
		order = order.Append(i)
	}
	let mut out = [][]int{order}
	for _ in 0..(steps%6+6)%6+3 {
		let mut next = []int{}
		for x in out[len(out)-1] {
			next = next.Append(x)
		}
		for i in 0..len(next) {
			let j = i + draw(&r, len(next)-i)
			let x = next[i]
			next[i] = next[j]
			next[j] = x
		}
		out = out.Append(next)
	}
	out
}

func reorderTable(f mut ui.Frame[int], seed int, steps int, k int) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, reorders(seed, steps)[k], func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
			ui.Same(f, ui.ByInt(x)) {
				reorderRow(f, x)
			}
		}
	}
}

func reorderRow(f mut ui.Frame[int], x int) {
	ui.Text(f, "row \(x)")
}

test UiIncrementalEqualsFresh(seed int, steps int) {
	assert lawIncrementalEqualsFresh(seed, steps)
}

test UiIdempotentCommit(seed int) {
	assert lawIdempotentCommit(seed)
}

test UiKeyedMovesFewest(seed int, count int) {
	assert lawKeyedMovesFewest(seed, count)
}

test UiKeyedIdentity(seed int, count int) {
	assert lawKeyedIdentity(seed, count)
}

test UiPressGivesSignal(seed int) {
	assert lawPressGivesSignal(seed)
}

test UiLocatorAgreesWithAccessTree(seed int) {
	assert lawLocatorAgreesWithAccessTree(seed)
}

test UiActsReadAsWritten(seed int) {
	assert lawActsReadAsWritten(seed)
}

test UiTreeAgreesWithLocate(seed int) {
	assert lawTreeAgreesWithLocate(seed)
}

test UiSameEqualsRecompute(seed int, steps int) {
	assert lawSameEqualsRecompute(seed, steps)
}

test UiMirrorWorkIsLinear(n int, seed int) {
	assert lawMirrorWorkIsLinear(n, seed)
}

test UiMirrorRejectsBadPatches(a int, b int) {
	assert lawMirrorRejectsBadPatches(a, b)
}

test UiCountsSumToPatch(seed int, steps int) {
	assert lawCountsSumToPatch(seed, steps)
}

type gbArgs { n int, xs []int }

func gbSame(a gbArgs, b gbArgs) bool {
	if a.n != b.n || len(a.xs) != len(b.xs) {
		return false
	}
	for i, x in a.xs {
		if b.xs[i] != x {
			return false
		}
	}
	true
}

func gbCheap(a gbArgs, b gbArgs) bool { a.n == b.n }

func gbBody(f mut ui.Frame[int], a gbArgs) {
	ui.Text(f, "n \(a.n)")
	for x in a.xs {
		ui.Text(f, "x \(x)")
	}
}

// gbGrouped's call of gbBody is a component call: the compiler sends it
// through std ui's groupBy, with cheap comparing n (the ledger's
// gbGrouped calls groupBy itself).
func gbGrouped(f mut ui.Frame[int], a gbArgs) {
	gbBody(f, a)
}

func lawGroupByKeepsAfterCheapEqual(seed int, steps int) bool {
	let mut r = seeded(seed)
	let mut f = ui.NewFrame[int]()
	let mut g = ui.NewFrame[int]()
	let mut prev = gbArgs{n: 0, xs: []int{}}
	let mut deep = true
	for i in 0..(steps%10+10)%10+2 {
		let pick = draw(&r, 4)
		let a = if i == 0 || pick == 3 {
			gbArgs{n: prev.n + draw(&r, 2), xs: []int{draw(&r, 3), draw(&r, 3)}}
		} else if pick == 0 {
			prev
		} else if pick == 1 {
			gbArgs{n: prev.n + 1, xs: prev.xs}
		} else {
			gbArgs{n: prev.n, xs: prev.xs.Append(draw(&r, 3))}
		}
		if !opsEqual(ui.Render(&f, gbGrouped, a), ui.Render(&g, gbBody, a)) {
			return false
		}
		let ran = i == 0 || !deep || !gbSame(prev, a)
		if (ui.GroupRuns(&f) == 1) != ran {
			return false
		}
		if ran {
			deep = i == 0 || gbCheap(prev, a)
		}
		prev = a
	}
	true
}

test UiGroupByKeepsAfterCheapEqual(seed int, steps int) {
	assert lawGroupByKeepsAfterCheapEqual(seed, steps)
}

test UiCountsLineText() {
	assert lawCountsLineText()
}

test UiSlowNamesItsCounter(seed int) {
	assert lawSlowNamesItsCounter(seed)
}

test UiKeyedReorderKeepsRows(seed int, steps int) {
	assert lawKeyedReorderKeepsRows(seed, steps)
}

type grArgs { n int, on bool }

func grBody(f mut ui.Frame[int], a grArgs) {
	ui.Text(f, "n \(a.n)")
	if a.on {
		ui.Box(f, ui.Plain()) {
			ui.Text(f, "on")
		}
	}
}

// grGrouped's call of grBody is a component call: the compiler sends it
// through std ui's group (the ledger's grGrouped calls group itself).
func grGrouped(f mut ui.Frame[int], a grArgs) {
	grBody(f, a)
}

func lawGroupSkipsEqualArguments(seed int, steps int) bool {
	let mut r = seeded(seed)
	let mut f = ui.NewFrame[int]()
	let mut g = ui.NewFrame[int]()
	let mut prev = grArgs{n: 0, on: false}
	for i in 0..(steps%10+10)%10+2 {
		let a = if i > 0 && draw(&r, 2) == 0 {
			prev
		} else {
			grArgs{n: draw(&r, 3), on: draw(&r, 2) == 0}
		}
		if !opsEqual(ui.Render(&f, grGrouped, a), ui.Render(&g, grBody, a)) || !ui.SameShapes(ui.TreeShape(&f), ui.TreeShape(&g)) {
			return false
		}
		let ran = i == 0 || a.n != prev.n || a.on != prev.on
		if (ui.GroupRuns(&f) == 1) != ran {
			return false
		}
		prev = a
	}
	true
}

test UiGroupSkipsEqualArguments(seed int, steps int) {
	assert lawGroupSkipsEqualArguments(seed, steps)
}
`

// stdUITreeStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUITreeStatements = map[string]string{
	"ui IncrementalEqualsFresh":      `IncrementalEqualsFresh(seed int, steps int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..(steps%8+8)%8+1 { let s = genState(&r) Apply(&m, pass(&f, ps, s)) catch { case BadPatch: return false } let mut fresh = NewFrame[int]() let mut fm = NewMirror[int](Root(&fresh)) Apply(&fm, pass(&fresh, ps, s)) catch { case BadPatch: return false } let want = models(ps, s, 0) if !SameShapes(FrameShape(&f), want) || !SameShapes(TreeShape(&f), want) || !SameShapes(MirrorShape(&m), want) || !SameShapes(MirrorShape(&fm), want) { return false } if Len(&f) != nodes(want)+1 || Held(&m) != Len(&f) { return false } if !stopsAt(ps, s, 0) && Retained(&f) != Len(&f) { return false } } true }`,
	"ui IdempotentCommit":            `IdempotentCommit(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let s0 = genState(&r) let s1 = genState(&r) let _ = pass(&f, ps, s0) let _ = pass(&f, ps, s1) len(pass(&f, ps, s1)) == 0 }`,
	"ui KeyedIdentity":               `KeyedIdentity(seed int, count int) { let mut r = seeded(seed) let mut xs = []int{} for i in 0..(count%12+12)%12+1 { xs = xs.Append(i * 7) } let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) Apply(&m, Render(&f, keyedRows, xs)) catch { case BadPatch: return false } let before = rowIds(&f, xs) for _ in 0..2 { let ys = shuffled(xs, &r) let ops = Render(&f, keyedRows, ys) Apply(&m, ops) catch { case BadPatch: return false } for op in ops { match op { case Move[int]: case _: return false } } let after = rowIds(&f, ys) if len(before) != len(xs)+1 || len(after) != len(xs)+1 || !SameShapes(MirrorShape(&m), FrameShape(&f)) { return false } for x, ids in before { if after.Get(x) != (Some{Value: ids}) { return false } } } true }`,
	"ui KeyedMovesFewest":            `KeyedMovesFewest(seed int, count int) { let mut r = seeded(seed) let mut xs = []int{} for i in 0..(count%12+12)%12+1 { xs = xs.Append(i) } let ys = shuffled(xs, &r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) Apply(&m, Render(&f, keyedLines, xs)) catch { case BadPatch: return false } let ops = Render(&f, keyedLines, ys) Apply(&m, ops) catch { case BadPatch: return false } let mut moves = 0 for op in ops { match op { case Move[int]: moves = moves + 1 case _: return false } } SameShapes(MirrorShape(&m), FrameShape(&f)) && moves == len(ys)-longestRise(ys) }`,
	"ui PressGivesSignal":            `PressGivesSignal(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut ok = true for _ in 0..3 { let _ = pass(&f, ps, genState(&r)) for k in Kids(&f, Root(&f)) { ok = ok && signalsAgree(&f, k, false, -1) } } ok }`,
	"ui LocatorAgreesWithAccessTree": `LocatorAgreesWithAccessTree(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..3 { let s = genState(&r) let ops = Render(&f, func(g mut Frame[int], st []int) { for p in ps { render(g, p, st, 0) catch { case Missing: Unreachable("ui laws: a read is drawn only under a boundary") case Pending: Unreachable("ui laws: a read is drawn only under a boundary") } } extras(g, st[0]) }, s) Apply(&m, ops) catch { case BadPatch: return false } let mut shapes = models(ps, s, 0) for x in extraShapes(s[0]) { shapes = shapes.Append(x) } let want = accessModel(shapes, false, "") let mut locs = []Locator{} for role in extraRoles() { locs = locs.Append(WithRole{Role: role, Name: ""}) } let w = want[draw(&r, len(want))] locs = locs.Append(WithRole{Role: w.a.Role, Name: w.a.Name}) locs = locs.Append(WithLabel{Label: w.a.Name}) locs = locs.Append(WithPlaceholder{Text: w.a.Name}) locs = locs.Append(WithText{Text: w.a.Name}) locs = locs.Append(WithLabel{Label: extraLabel(s[0]).Text}) locs = locs.Append(WithPlaceholder{Text: "ph\(s[0] % 2)"}) for loc in locs { let mut model = []seen{} for x in want { if wants(loc, x) { model = model.Append(x) } } let got = LocateIn(&m, loc) if len(got) != len(model) { return false } for i, id in got { if AccessOf(&m, id) != model[i].a { return false } } if matches(&m, loc, ToHaveCount{Count: len(model)}) != true || matches(&m, loc, ToHaveCount{Count: len(model) + 1}) || matches(&m, loc, ToHaveCount{Count: len(model) - 1}) || matches(&m, loc, ToExist) != (len(model) > 0) || matches(&m, loc, ToBeAbsent) != (len(model) == 0) { return false } if len(model) > 0 { let x = model[0] let checked = x.a.Checked == (Some{Value: true}) let unchecked = x.a.Checked == (Some{Value: false}) if matches(&m, loc, ToBeDisabled) != x.a.Disabled || matches(&m, loc, ToBeEnabled) == x.a.Disabled || matches(&m, loc, ToBeChecked) != checked || matches(&m, loc, ToBeUnchecked) != unchecked || matches(&m, loc, ToBeInvalid) != x.a.Invalid { return false } if !matches(&m, loc, ToHaveDescription{Text: x.a.Description}) || matches(&m, loc, ToHaveDescription{Text: x.a.Description + "!"}) { return false } if !matches(&m, loc, ToContainText{Text: x.text}) || !matches(&m, loc, ToContainText{Text: ""}) || matches(&m, loc, ToContainText{Text: x.text + "!"}) { return false } match x.a.Value { case Some[string] as v: if !matches(&m, loc, ToHaveValue{Value: v.Value}) || matches(&m, loc, ToHaveValue{Value: v.Value + "!"}) { return false } case None: if matches(&m, loc, ToHaveValue{Value: ""}) { return false } } } } } true }`,
	"ui ActsReadAsWritten":           `ActsReadAsWritten(seed int) { let mut r = seeded(seed) let labels = []string{"inc", "by 5", "say \"hi\"", "a\\b", "名前", ""} let mut want = []string{} let t = json.NewText() let n = draw(&r, 8) t.BeginList(n) for _ in 0..n { let label = labels[draw(&r, len(labels))] let k = draw(&r, 6) t.BeginObject(1) if k == 0 { t.Key("tap") t.String(label) want = want.Append(actWhole(TapOn{Label: label})) } else if k == 1 { let text = labels[draw(&r, len(labels))] t.Key("type") t.BeginList(2) t.String(label) t.String(text) t.EndList() want = want.Append(actWhole(TypeInto{Label: label, Text: text})) } else if k == 2 { t.Key("key") t.String(label) want = want.Append(actWhole(KeyIn{Press: KeyPress{Name: label, Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}})) } else if k == 3 { t.Key("settle") t.Bool(true) want = want.Append(actWhole(SettleAll)) } else if k == 4 { let ms = draw(&r, 5000) t.Key("advance") t.Int(int64(ms)) want = want.Append(actWhole(AdvanceBy{By: duration(int64(ms) * int64(1000000))})) } else { let offset = float64(draw(&r, 400)) / 4.0 let height = float64(draw(&r, 400) + 1) t.Key("scroll") t.BeginList(3) t.String(label) t.Float(offset) t.Float(height) t.EndList() want = want.Append(actWhole(ScrollOn{Label: label, Offset: Length(offset), Height: Length(height)})) } t.EndObject() } t.EndList() let got = ParseActs(t.Written()) catch { case BadScript: return false } if len(got) != len(want) { return false } for i, a in got { if actWhole(a) != want[i] { return false } } true }`,
	"ui TreeAgreesWithLocate":        `TreeAgreesWithLocate(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..3 { let s = genState(&r) let ops = Render(&f, func(g mut Frame[int], st []int) { for p in ps { render(g, p, st, 0) catch { case Missing: Unreachable("ui laws: a read is drawn only under a boundary") case Pending: Unreachable("ui laws: a read is drawn only under a boundary") } } extras(g, st[0]) }, s) Apply(&m, ops) catch { case BadPatch: return false } let mut shapes = models(ps, s, 0) for x in extraShapes(s[0]) { shapes = shapes.Append(x) } let mut want = []seen{} for x in accessModel(shapes, false, "") { match x.a.Role { case Generic: case _: want = want.Append(x) } } // The host's boxes: each node a role finds, the k-th of its role at // x = k, y = the role's name's length. let mut boxes = Map[Id[int], Rect]{} let mut ids = []Id[int]{} let mut taken = Map[string, int]{} for x in want { let role = RoleName(x.a.Role) let k = match taken.Get(role) { case Some[int] as n: n.Value case None: 0 } taken[role] = k + 1 let found = LocateIn(&m, WithRole{Role: x.a.Role, Name: ""}) if k >= len(found) { return false } ids = ids.Append(found[k]) boxes[found[k]] = Rect{X: Length(float64(k)), Y: Length(float64(len(role))), W: 1, H: 1} } let tree = TreeOf(&m, boxes) if len(tree) != len(want) { return false } for i, e in tree { let a = AccessOf(&m, ids[i]) if e.Role != RoleName(want[i].a.Role) || e.Name != a.Name || a != want[i].a || strings.Join(e.States, ",") != strings.Join(StatesOf(a), ",") { return false } // The states the model screen's access has, as ARIA names them. let states = "," + strings.Join(e.States, ",") + "," let checked = match a.Checked { case Some[bool] as c: if c.Value { ",checked," } else { ",unchecked," } case None: "" } let value = match a.Value { case Some[string] as v: ",value=\(v.Value)," case None: "" } if strings.Contains(states, ",disabled,") != a.Disabled || strings.Contains(states, ",invalid,") != a.Invalid || checked != "" && !strings.Contains(states, checked) || checked == "" && (strings.Contains(states, ",checked,") || strings.Contains(states, ",unchecked,")) || value != "" && !strings.Contains(states, value) { return false } match (e.Box, boxes.Get(ids[i])) { case (Some[Rect] as b, Some[Rect] as w): if b.Value != w.Value { return false } case _: return false } let line = TreeLine(e) if !strings.HasPrefix(line, "{\"role\":\"\(e.Role)\",\"name\":") || !strings.Contains(line, ",\"states\":[") || !strings.Contains(line, ",\"box\":[") { return false } } } true }`,
	"ui SameEqualsRecompute":         `SameEqualsRecompute(seed int, steps int) { let mut r = seeded(seed) // revived: a Same in a Box in a boundary whose read, of a slot the // Same's key does not hold, fails and passes in turn, so the Box is // parked and comes back while the Same's key stays. let revived = pBound{form: 0, slot: 0, kids: []prog{pBox{slot: 1, kids: []prog{pSame{kids: []prog{pText{slot: 2}}, memo: true}}}, pRead{slot: 3}}} let ps = genRoot(&r).Append(revived) let qs = unmemo(ps) let mut f = NewFrame[int]() let mut g = NewFrame[int]() let mut s = genState(&r) let mut again = false let key = KeyDown{Press: KeyPress{Name: "k", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}} for _ in 0..(steps%8+8)%8+2 { let a = pass(&f, ps, s) let b = pass(&g, qs, s) if !opsEqual(a, b) || !SameShapes(FrameShape(&f), FrameShape(&g)) || !SameShapes(TreeShape(&f), TreeShape(&g)) { return false } if again && Skips(&f) != sameCount(ps, s, 0) { return false } if sent(Handle(&f, key).Signal) != sent(Handle(&g, key).Signal) { return false } let ka = Kids(&f, Root(&f)) let kb = Kids(&g, Root(&g)) if len(ka) != len(kb) { return false } for i in 0..len(ka) { if !signalsMatch(&f, &g, ka[i], kb[i]) { return false } } again = false let pick = draw(&r, 4) if pick == 0 { again = true } else if pick == 1 { let ta = toggles(&f, Root(&f), []Id[int]{}) let tb = toggles(&g, Root(&g), []Id[int]{}) if len(ta) != len(tb) { return false } if len(ta) > 0 { let i = draw(&r, len(ta)) if Handle(&f, Press[int]{Id: ta[i]}) != Handle(&g, Press[int]{Id: tb[i]}) { return false } } } else if pick == 2 { s[draw(&r, slots)] = draw(&r, 64) } else { s[3] = draw(&r, 3) } } true }`,
	"ui MirrorWorkIsLinear":          `MirrorWorkIsLinear(n int, seed int) { let k = (n%40+40)%40 + 1 let mut reads = []int{} for size in []int{k, 2 * k, 4 * k} { let mut xs = []int{} for i in 0..size { xs = xs.Append(i) } let view = func(f mut Frame[int], rows []int) { Column(f, Plain()) { Each(f, rows, func(x int) Key { ByInt(x) }) func(x int) { Text(f, "row \(x)") } } } let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) Apply(&m, Render(&f, view, xs)) catch { case BadPatch: return false } reads = reads.Append(MirrorReads(&m)) let before = MirrorReads(&m) let found = LocateIn(&m, WithText{Text: "row \((seed%size + size) % size)"}) if len(found) != 1 || MirrorReads(&m)-before != Held(&m) { return false } } reads[2]-reads[1] == 2*(reads[1]-reads[0]) }`,
	"ui MirrorRejectsBadPatches":     `MirrorRejectsBadPatches(a int, b int) { let na = (a%5+5)%5 + 1 let nb = (b%5+5)%5 + 1 let mut xs = []int{} let mut ys = []int{} let mut more = []int{} for i in 0..na { xs = xs.Append(i) more = more.Append(i) } for i in 0..nb { ys = ys.Append(100 + i) } xs = xs.Append(99) more = more.Append(50) more = more.Append(51) let zs = []int{200, 201} let mut f = NewFrame[int]() let root = Root(&f) let first = Render(&f, badPatchView, [][]int{xs, ys, zs}) let second = Render(&f, badPatchView, [][]int{more, ys, zs}) let rows = insertsOf(first, "row 0") let others = insertsOf(first, "row 100") let gones = insertsOf(first, "row 99") let zrows = insertsOf(first, "row 200") let freshes = insertsOf(second, "row 50") let adds = insertsOf(second, "row 51") if len(rows) != 1 || len(others) != 1 || len(gones) != 1 || len(zrows) != 1 || len(freshes) != 1 || len(adds) != 1 { return false } let row = rows[0] let other = others[0] let fresh = freshes[0] let lc = row.Parent let rc = other.Parent if len(insertsUnder(first, lc)) != na+1 || len(insertsUnder(first, rc)) != nb || len(insertsUnder(first, root)) != 3 { return false } let text = TextNode{Value: "x", Style: Plain()} let box = BoxNode{Style: Plain()} // The valid ops before each bad one: a Set, a Move, an Insert, and a // Remove of a row and of a whole column. let good = []Op[int]{ Set[int]{Id: other.Id, Node: TextNode{Value: "changed", Style: Plain()}}, Move[int]{Id: other.Id, Parent: rc, Before: None}, Insert[int]{Id: adds[0].Id, Parent: lc, Before: Some{Value: row.Id}, Node: adds[0].Node}, Remove[int]{Id: gones[0].Id}, Remove[int]{Id: zrows[0].Parent}, } let bad = []Op[int]{ row, Insert[int]{Id: fresh.Id, Parent: fresh.Id, Before: None, Node: text}, Insert[int]{Id: fresh.Id, Parent: lc, Before: Some{Value: fresh.Id}, Node: text}, Insert[int]{Id: fresh.Id, Parent: lc, Before: Some{Value: other.Id}, Node: text}, Move[int]{Id: row.Id, Parent: rc, Before: None}, Move[int]{Id: row.Id, Parent: lc, Before: Some{Value: row.Id}}, Move[int]{Id: row.Id, Parent: lc, Before: Some{Value: other.Id}}, Move[int]{Id: fresh.Id, Parent: lc, Before: None}, Remove[int]{Id: root}, Remove[int]{Id: fresh.Id}, Remove[int]{Id: gones[0].Id}, Set[int]{Id: fresh.Id, Node: text}, Set[int]{Id: row.Id, Node: box}, } for op in bad { if !badPatchRejected(root, first, good, op) { return false } } // The second pass fits. let mut m = NewMirror[int](root) Apply(&m, first) catch { case BadPatch: return false } Apply(&m, second) catch { case BadPatch: return false } true }`,
	"ui CountsSumToPatch":            `CountsSumToPatch(seed int, steps int) { let mut r = seeded(seed) let ps = genRoot(&r).Append(pBox{slot: 1, kids: []prog{pSame{kids: []prog{pText{slot: 2}, pText{slot: 3}}, memo: true}}}) let mut f = NewFrame[int]() let mut kept = 0 let mut s = []int{} for i in 0..(steps%8+8)%8+2 { // The kept Same reads slots 2 and 3, which stay; a repeated state keeps each component group. if i%2 == 0 { s = genState(&r) s[2] = 7 s[3] = 9 } let ops = pass(&f, ps, s) let c = PassCounts(&f) let mut ins = 0 let mut mov = 0 let mut rem = 0 let mut set = 0 for op in ops { match op { case Insert[int]: ins = ins + 1 case Move[int]: mov = mov + 1 case Remove[int]: rem = rem + 1 case Set[int]: set = set + 1 case Define: } } if c.Inserted != ins || c.Moved != mov || c.Removed != rem || c.Set != set || ins+mov+rem+set != len(ops) { return false } if c.Ran+c.Kept != Len(&f)-1 || c.Visits != Len(&f)-c.Kept-Skips(&f) || c.GroupsKept < Skips(&f) { return false } kept = kept + c.Kept } kept > 0 }`,
	"ui CountsLineText":              `CountsLineText() { let c = StepCounts{Inserted: 0, Moved: 0, Removed: 0, Set: 1, Ran: 3, Kept: 801, GroupsRan: 0, GroupsKept: 200, Visits: 3} let d = StepCounts{Inserted: 2, Moved: 0, Removed: 1, Set: 0, Ran: 2, Kept: 0, GroupsRan: 1, GroupsKept: 0, Visits: 4} CountsLine(c) == "1 set; ran 3 nodes, kept 801; groups ran 0, kept 200; visits 3" && CountsLine(d) == "2 insert, 1 remove; ran 2 nodes, kept 0; groups ran 1, kept 0; visits 4" && CountsLine(NoCounts()) == "no ops; ran 0 nodes, kept 0; groups ran 0, kept 0; visits 0" }`,
	"ui SlowNamesItsCounter":         `SlowNamesItsCounter(seed int) { let mut r = seeded(seed) let n = func() int { draw(&r, 9) } let s = Stats{Signals: n(), Written: n(), Rows: n(), Skipped: n(), Stopped: n(), Committed: n(), Inserted: n(), Moved: n(), Removed: n(), Set: n(), Painted: n(), Fetches: n(), Cancels: n()} let b = Budget{Signals: n(), Written: n(), Committed: n(), Ops: n() * 3, Painted: n(), Fetches: n()} let all = []Counter{SignalsCounter, WrittenCounter, CommittedCounter, OpsCounter, PaintedCounter, FetchesCounter} let names = []string{"Signals", "Written", "Committed", "Ops", "Painted", "Fetches"} let mut want = []Slow{} for i, c in all { if CounterText(c) != names[i] { return false } let bound = slowBound(c, b) let count = slowCount(c, s) if bound > 0 && count > bound { want = want.Append(Slow{Counter: c, Count: count, Bound: bound}) } } SlowsOf(s, b) == want }`,
	"ui GroupByKeepsAfterCheapEqual": `GroupByKeepsAfterCheapEqual(seed int, steps int) { let mut r = seeded(seed) let mut f = NewFrame[int]() let mut g = NewFrame[int]() let mut prev = gbArgs{n: 0, xs: []int{}} let mut deep = true for i in 0..(steps%10+10)%10+2 { let pick = draw(&r, 4) let a = if i == 0 || pick == 3 { gbArgs{n: prev.n + draw(&r, 2), xs: []int{draw(&r, 3), draw(&r, 3)}} } else if pick == 0 { prev } else if pick == 1 { gbArgs{n: prev.n + 1, xs: prev.xs} } else { gbArgs{n: prev.n, xs: prev.xs.Append(draw(&r, 3))} } if !opsEqual(Render(&f, gbGrouped, a), Render(&g, gbBody, a)) { return false } let ran = i == 0 || !deep || !gbSame(prev, a) if (GroupRuns(&f) == 1) != ran { return false } if ran { deep = i == 0 || gbCheap(prev, a) } prev = a } true }`,
	"ui KeyedReorderKeepsRows":       `KeyedReorderKeepsRows(seed int, steps int) { let orders = reorders(seed, steps) let mut r = seeded(steps) let view = func(f mut Frame[int], k int) { reorderTable(f, seed, steps, k) } let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for k in 0..len(orders) { let mut want = []string{} for x in orders[k] { want = want.Append("row \(x)") } for _ in 0..draw(&r, 2)+1 { Apply(&m, Render(&f, view, k)) catch { case BadPatch: return false } if shownTexts(MirrorShape(&m)) != want { return false } } } true }`,
	"ui GroupSkipsEqualArguments":    `GroupSkipsEqualArguments(seed int, steps int) { let mut r = seeded(seed) let mut f = NewFrame[int]() let mut g = NewFrame[int]() let mut prev = grArgs{n: 0, on: false} for i in 0..(steps%10+10)%10+2 { let a = if i > 0 && draw(&r, 2) == 0 { prev } else { grArgs{n: draw(&r, 3), on: draw(&r, 2) == 0} } if !opsEqual(Render(&f, grGrouped, a), Render(&g, grBody, a)) || !SameShapes(TreeShape(&f), TreeShape(&g)) { return false } let ran = i == 0 || a.n != prev.n || a.on != prev.on if (GroupRuns(&f) == 1) != ran { return false } prev = a } true }`,
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
