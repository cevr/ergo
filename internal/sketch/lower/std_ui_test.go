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

type prog = pText{slot int} | pInput{slot int} | pCheck{slot int} | pSwap{slot int} | pBox{slot int, kids []prog} | pRow{kids []prog} | pPress{slot int, kids []prog} | pEach{slot int, kids []prog} | pBound{form int, slot int, kids []prog} | pRead{slot int} | pFieldset{slot int, kids []prog} | pSame{kids []prog, memo bool}

type Missing { V int }

type rng { x int }

func draw(r *rng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

func seeded(seed int) rng { rng{x: (seed%2147483648 + 2147483648) % 2147483648} }

let slots = 6

func genProg(r *rng, depth int, bounded bool) prog {
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

func described(v int) string {
	if v%2 == 0 {
		return "d\(v)"
	}
	""
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

func sameBody(f *ui.Frame[int], kids []prog, s []int, row int, dig int) fails Missing | ui.Pending {
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

func signalsAgree(f *ui.Frame[int], id ui.Id[int], off bool) bool {
	let mut ok = true
	let mut under = off
	let pressed = sent(ui.Handle(f, ui.Press[int]{Id: id}).Signal)
	let typed = sent(ui.Handle(f, ui.Type[int]{Id: id, Text: "abc"}).Signal)
	match ui.NodeOf(f, id) {
	case Some[ui.Node[int]] as n:
		match n.Value {
		case ui.FieldsetNode as d:
			under = off || d.Disabled
			ok = pressed == -1 && typed == -1
		case ui.PressableNode[int] as p:
			ok = pressed == p.On && typed == -1
		case ui.CheckNode[int] as c:
			ok = pressed == c.On && typed == -1
		case ui.InputNode as i:
			ok = i.Value == "v\(typed - 3)" && pressed == -1
		case _:
			ok = pressed == -1 && typed == -1
		}
		if off {
			ok = pressed == -1 && typed == -1
		}
	case None:
		return false
	}
	for k in ui.Kids(f, id) {
		ok = ok && signalsAgree(f, k, under)
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
			ok = ok && signalsAgree(&f, k, false)
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
	let base = ui.Access{Role: ui.Generic, Name: "", Disabled: false, Description: desc, Checked: None, Value: None}
	match n {
	case ui.PressableNode[int] as x:
		ui.Access{Role: ui.Button, Name: x.Label, Disabled: off, Description: desc, Checked: None, Value: None}
	case ui.CheckNode[int] as x:
		ui.Access{Role: ui.Checkbox, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.Checked}, Value: None}
	case ui.ToggleNode as x:
		ui.Access{Role: ui.Switch, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.On}, Value: None}
	case ui.InputNode as x:
		ui.Access{Role: ui.TextBox, Name: x.Placeholder, Disabled: off, Description: desc, Checked: None, Value: Some{Value: x.Value}}
	case ui.TextNode as x:
		ui.Access{Role: ui.Static, Name: x.Value, Disabled: false, Description: desc, Checked: None, Value: None}
	case ui.FieldsetNode:
		ui.Access{Role: ui.Group, Name: "", Disabled: false, Description: desc, Checked: None, Value: None}
	case ui.AreaNode as x:
		ui.Access{Role: ui.Region, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None}
	case ui.CanvasNode as x:
		ui.Access{Role: ui.Image, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None}
	case ui.RowsNode as x:
		ui.Access{Role: ui.List, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None}
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
		labelled && x.a.Name == l.Label
	case ui.WithPlaceholder as l:
		x.a.Role == ui.TextBox && x.a.Name == l.Text
	case ui.WithText as l:
		x.a.Role == ui.Static && x.a.Name == l.Text
	}
}

func matches(m *ui.Mirror[int], loc ui.Locator, is ui.Matcher) bool {
	ui.ExpectIn(m, loc, is) catch {
	case ui.Mismatch:
		return false
	}
	true
}

func extras(f *ui.Frame[int], v int) {
	ui.Fieldset(f, v%2 == 1, "x\(v)") {
		let on = ui.NewFlag(f, true)
		ui.Toggle(f, on, "tg\(v % 3)")
		ui.Area(f, "area\(v % 2)", ui.Plain(), func(p ui.PointerEvent) Option[int] { None }) {
			ui.Text(f, "in area")
		}
		ui.Canvas(f, "c\(v % 4)", 4, 4) func(p *ui.Paint) {}
		ui.Rows(f, "rows", 0, 10, func(i int) ui.Key { ui.ByInt(i) }) func(i int) {}
	}
}

func extraShapes(v int) []ui.Shape[int] {
	let kids = []ui.Shape[int]{leaf(ui.ToggleNode{Label: "tg\(v % 3)", On: true}), ui.Shape[int]{Node: ui.AreaNode{Label: "area\(v % 2)", Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: "in area", Style: ui.Plain()})}}, leaf(ui.CanvasNode{Label: "c\(v % 4)", Width: 4, Height: 4, Paint: []ui.PaintOp{}}), leaf(ui.RowsNode{Label: "rows", Count: 0, RowHeight: 10, First: 0, Offset: 0})}
	[]ui.Shape[int]{ui.Shape[int]{Node: ui.FieldsetNode{Disabled: v%2 == 1, Description: "x\(v)"}, Kids: kids}}
}

func lawLocatorAgreesWithAccessTree(seed int) bool {
	let mut r = seeded(seed)
	let ps = genRoot(&r)
	let mut f = ui.NewFrame[int]()
	let mut m = ui.NewMirror[int](ui.Root(&f))
	for _ in 0..3 {
		let s = genState(&r)
		let ops = ui.Render(&f, func(g *ui.Frame[int], st []int) {
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
		for role in []ui.Role{ui.Button, ui.Checkbox, ui.Switch, ui.TextBox, ui.Static, ui.Group, ui.Region, ui.Image, ui.List, ui.Generic} {
			locs = locs.Append(ui.WithRole{Role: role, Name: ""})
		}
		let w = want[draw(&r, len(want))]
		locs = locs.Append(ui.WithRole{Role: w.a.Role, Name: w.a.Name})
		locs = locs.Append(ui.WithLabel{Label: w.a.Name})
		locs = locs.Append(ui.WithPlaceholder{Text: w.a.Name})
		locs = locs.Append(ui.WithText{Text: w.a.Name})
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
				if matches(&m, loc, ui.ToBeDisabled) != x.a.Disabled || matches(&m, loc, ui.ToBeEnabled) == x.a.Disabled || matches(&m, loc, ui.ToBeChecked) != checked || matches(&m, loc, ui.ToBeUnchecked) != unchecked {
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

func signalsMatch(f *ui.Frame[int], g *ui.Frame[int], a ui.Id[int], b ui.Id[int]) bool {
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

func toggles(f *ui.Frame[int], id ui.Id[int], out []ui.Id[int]) []ui.Id[int] {
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

test UiLocatorAgreesWithAccessTree(seed int) {
	assert lawLocatorAgreesWithAccessTree(seed)
}

test UiSameEqualsRecompute(seed int, steps int) {
	assert lawSameEqualsRecompute(seed, steps)
}
`

// stdUITreeStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUITreeStatements = map[string]string{
	"ui IncrementalEqualsFresh":      `IncrementalEqualsFresh(seed int, steps int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..(steps%8+8)%8+1 { let s = genState(&r) Apply(&m, pass(&f, ps, s)) catch { case BadPatch: return false } let mut fresh = NewFrame[int]() let mut fm = NewMirror[int](Root(&fresh)) Apply(&fm, pass(&fresh, ps, s)) catch { case BadPatch: return false } let want = models(ps, s, 0) if !SameShapes(FrameShape(&f), want) || !SameShapes(TreeShape(&f), want) || !SameShapes(MirrorShape(&m), want) || !SameShapes(MirrorShape(&fm), want) { return false } if Len(&f) != nodes(want)+1 || Held(&m) != Len(&f) { return false } if !stopsAt(ps, s, 0) && Retained(&f) != Len(&f) { return false } } true }`,
	"ui IdempotentCommit":            `IdempotentCommit(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let s0 = genState(&r) let s1 = genState(&r) let _ = pass(&f, ps, s0) let _ = pass(&f, ps, s1) len(pass(&f, ps, s1)) == 0 }`,
	"ui KeyedIdentity":               `KeyedIdentity(seed int, count int) { let mut r = seeded(seed) let mut xs = []int{} for i in 0..(count%12+12)%12+1 { xs = xs.Append(i * 7) } let ys = shuffled(xs, &r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) Apply(&m, Render(&f, keyedRows, xs)) catch { case BadPatch: return false } let before = rowIds(&f, xs) let ops = Render(&f, keyedRows, ys) Apply(&m, ops) catch { case BadPatch: return false } for op in ops { match op { case Move[int]: case _: return false } } let after = rowIds(&f, ys) if len(before) != len(xs)+1 || len(after) != len(xs)+1 || !SameShapes(MirrorShape(&m), FrameShape(&f)) { return false } for x, ids in before { if after.Get(x) != (Some{Value: ids}) { return false } } true }`,
	"ui PressGivesSignal":            `PressGivesSignal(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut ok = true for _ in 0..3 { let _ = pass(&f, ps, genState(&r)) for k in Kids(&f, Root(&f)) { ok = ok && signalsAgree(&f, k, false) } } ok }`,
	"ui LocatorAgreesWithAccessTree": `LocatorAgreesWithAccessTree(seed int) { let mut r = seeded(seed) let ps = genRoot(&r) let mut f = NewFrame[int]() let mut m = NewMirror[int](Root(&f)) for _ in 0..3 { let s = genState(&r) let ops = Render(&f, func(g *Frame[int], st []int) { for p in ps { render(g, p, st, 0) catch { case Missing: Unreachable("ui laws: a read is drawn only under a boundary") case Pending: Unreachable("ui laws: a read is drawn only under a boundary") } } extras(g, st[0]) }, s) Apply(&m, ops) catch { case BadPatch: return false } let mut shapes = models(ps, s, 0) for x in extraShapes(s[0]) { shapes = shapes.Append(x) } let want = accessModel(shapes, false, "") let mut locs = []Locator{} for role in []Role{Button, Checkbox, Switch, TextBox, Static, Group, Region, Image, List, Generic} { locs = locs.Append(WithRole{Role: role, Name: ""}) } let w = want[draw(&r, len(want))] locs = locs.Append(WithRole{Role: w.a.Role, Name: w.a.Name}) locs = locs.Append(WithLabel{Label: w.a.Name}) locs = locs.Append(WithPlaceholder{Text: w.a.Name}) locs = locs.Append(WithText{Text: w.a.Name}) for loc in locs { let mut model = []seen{} for x in want { if wants(loc, x) { model = model.Append(x) } } let got = LocateIn(&m, loc) if len(got) != len(model) { return false } for i, id in got { if AccessOf(&m, id) != model[i].a { return false } } if matches(&m, loc, ToHaveCount{Count: len(model)}) != true || matches(&m, loc, ToHaveCount{Count: len(model) + 1}) || matches(&m, loc, ToHaveCount{Count: len(model) - 1}) || matches(&m, loc, ToExist) != (len(model) > 0) || matches(&m, loc, ToBeAbsent) != (len(model) == 0) { return false } if len(model) > 0 { let x = model[0] let checked = x.a.Checked == (Some{Value: true}) let unchecked = x.a.Checked == (Some{Value: false}) if matches(&m, loc, ToBeDisabled) != x.a.Disabled || matches(&m, loc, ToBeEnabled) == x.a.Disabled || matches(&m, loc, ToBeChecked) != checked || matches(&m, loc, ToBeUnchecked) != unchecked { return false } if !matches(&m, loc, ToHaveDescription{Text: x.a.Description}) || matches(&m, loc, ToHaveDescription{Text: x.a.Description + "!"}) { return false } if !matches(&m, loc, ToContainText{Text: x.text}) || !matches(&m, loc, ToContainText{Text: ""}) || matches(&m, loc, ToContainText{Text: x.text + "!"}) { return false } match x.a.Value { case Some[string] as v: if !matches(&m, loc, ToHaveValue{Value: v.Value}) || matches(&m, loc, ToHaveValue{Value: v.Value + "!"}) { return false } case None: if matches(&m, loc, ToHaveValue{Value: ""}) { return false } } } } } true }`,
	"ui SameEqualsRecompute":         `SameEqualsRecompute(seed int, steps int) { let mut r = seeded(seed) // revived: a Same in a Box in a boundary whose read, of a slot the // Same's key does not hold, fails and passes in turn, so the Box is // parked and comes back while the Same's key stays. let revived = pBound{form: 0, slot: 0, kids: []prog{pBox{slot: 1, kids: []prog{pSame{kids: []prog{pText{slot: 2}}, memo: true}}}, pRead{slot: 3}}} let ps = genRoot(&r).Append(revived) let qs = unmemo(ps) let mut f = NewFrame[int]() let mut g = NewFrame[int]() let mut s = genState(&r) let mut again = false let key = KeyDown{Press: KeyPress{Name: "k", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}} for _ in 0..(steps%8+8)%8+2 { let a = pass(&f, ps, s) let b = pass(&g, qs, s) if !opsEqual(a, b) || !SameShapes(FrameShape(&f), FrameShape(&g)) || !SameShapes(TreeShape(&f), TreeShape(&g)) { return false } if again && Skips(&f) != sameCount(ps, s, 0) { return false } if sent(Handle(&f, key).Signal) != sent(Handle(&g, key).Signal) { return false } let ka = Kids(&f, Root(&f)) let kb = Kids(&g, Root(&g)) if len(ka) != len(kb) { return false } for i in 0..len(ka) { if !signalsMatch(&f, &g, ka[i], kb[i]) { return false } } again = false let pick = draw(&r, 4) if pick == 0 { again = true } else if pick == 1 { let ta = toggles(&f, Root(&f), []Id[int]{}) let tb = toggles(&g, Root(&g), []Id[int]{}) if len(ta) != len(tb) { return false } if len(ta) > 0 { let i = draw(&r, len(ta)) if Handle(&f, Press[int]{Id: ta[i]}) != Handle(&g, Press[int]{Id: tb[i]}) { return false } } } else if pick == 2 { s[draw(&r, slots)] = draw(&r, 64) } else { s[3] = draw(&r, 3) } } true }`,
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
