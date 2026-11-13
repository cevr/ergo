package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdUILoadProbe restates each law of std ui (ledger runner TestUILoadModel) as a
// property test of its own name (stdLawsTestName): the laws' helpers over
// the package's exported operations, each law a function the test asserts.
// It is generated from internal/sketch/std/ui/laws_state.ergo (with the helpers of laws.ergo); a law changed in
// source fails TestUILoadModel until this restatement changes with it.
const stdUILoadProbe = `import ui

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



type row { a string, b string, on bool }

func cellRows(f *ui.Frame[int], keys []int) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, keys, func(k int) ui.Key { ui.ByInt(k) }) func(k int) {
			let a = ui.NewDraft(f, "")
			ui.Field(f, a, "a")
			ui.Text(f, a.Value)
			ui.Row(f, ui.Plain()) {
				let b = ui.NewDraft(f, "b\(k)")
				ui.Field(f, b, "b")
			}
			let on = ui.NewFlag(f, false)
			ui.Toggle(f, on, "t")
			ui.Submit(f, a, "send", k) {
				ui.Text(f, "send")
			}
		}
	}
}

func cellShape(keys []int, rows Map[int, row]) []ui.Shape[int] {
	let mut kids = []ui.Shape[int]{}
	for k in keys {
		let r = rowOf(rows, k)
		kids = kids.Append(leaf(ui.InputNode{Value: r.a, Placeholder: "a"}))
		kids = kids.Append(leaf(ui.TextNode{Value: r.a, Style: ui.Plain()}))
		kids = kids.Append(ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.InputNode{Value: r.b, Placeholder: "b"})}})
		kids = kids.Append(leaf(ui.ToggleNode{Label: "t", On: r.on}))
		kids = kids.Append(ui.Shape[int]{Node: ui.PressableNode{Label: "send", On: k, Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: "send", Style: ui.Plain()})}})
	}
	[]ui.Shape[int]{ui.Shape[int]{Node: ui.ColumnNode{Style: ui.Plain()}, Kids: kids}}
}

func rowOf(rows Map[int, row], k int) row {
	match rows.Get(k) {
	case Some[row] as r:
		r.Value
	case None:
		row{a: "", b: "b\(k)", on: false}
	}
}

func part(f *ui.Frame[int], at int, i int) ui.Id[int] {
	let col = ui.Kids(f, ui.Root(f))[0]
	let id = ui.Kids(f, col)[5*at+i]
	if i == 2 {
		return ui.Kids(f, id)[0]
	}
	id
}

func pick(r *rng, keepAll bool) []int {
	let mut keys = []int{}
	if !keepAll && draw(r, 4) == 0 {
		return keys
	}
	for k in 0..6 {
		if keepAll || draw(r, 3) != 0 {
			keys = keys.Append(k)
		}
	}
	shuffled(keys, r)
}

func cellsFollowModel(seed int, steps int, unmount bool) bool {
	let mut r = seeded(seed)
	let mut f = ui.NewFrame[int]()
	let mut keys = pick(&r, true)
	let mut rows = Map[int, row]{}
	let _ = ui.Render(&f, cellRows, keys)
	for i in 0..(steps%24+24)%24+1 {
		let what = draw(&r, 5)
		if what == 0 || len(keys) == 0 {
			keys = pick(&r, !unmount)
			let mut kept = Map[int, row]{}
			for k in keys {
				match rows.Get(k) {
				case Some[row] as x:
					kept[k] = x.Value
				case None:
				}
			}
			rows = kept
		} else {
			let at = draw(&r, len(keys))
			let k = keys[at]
			let old = rowOf(rows, k)
			let mut now = old
			let mut done = ui.Handled[int]{Signal: None, Changed: false}
			match what {
			case 1:
				now.a = "s\(i % 3)"
				done = ui.Handle(&f, ui.Type[int]{Id: part(&f, at, 0), Text: now.a})
			case 2:
				now.b = "u\(i % 3)"
				done = ui.Handle(&f, ui.Type[int]{Id: part(&f, at, 2), Text: now.b})
			case 3:
				now.on = !old.on
				done = ui.Handle(&f, ui.Press[int]{Id: part(&f, at, 3)})
			case _:
				now.a = ""
				done = ui.Handle(&f, ui.Press[int]{Id: part(&f, at, 4)})
				if sent(done.Signal) != k {
					return false
				}
			}
			if what != 4 && sent(done.Signal) != -1 {
				return false
			}
			if done.Changed != (now != old) {
				return false
			}
			rows[k] = now
		}
		let _ = ui.Render(&f, cellRows, keys)
		if !ui.SameShapes(ui.FrameShape(&f), cellShape(keys, rows)) || !ui.SameShapes(ui.TreeShape(&f), cellShape(keys, rows)) {
			return false
		}
	}
	true
}

func lawStateKeeps(seed int, steps int) bool {
	cellsFollowModel(seed, steps, false)
}

func lawFreshMountInits(seed int, steps int) bool {
	cellsFollowModel(seed, steps, true)
}

type place = Nowhere | Top | Boxed

type Nowhere {}

type Top {}

type Boxed {}

type want { at place, key int }

func loadView(f *ui.Frame[int], w want) {
	let k = w.key
	match w.at {
	case Nowhere:
		ui.Text(f, "none")
	case Top:
		ui.Load(f, ui.ByInt(k), func() int { k })
	case Boxed:
		ui.Box(f, ui.Plain()) {
			ui.Load(f, ui.ByInt(k), func() int { k })
		}
	}
}

type loaded { at place, key int, epoch int }

type fetching { epoch int, run func() int }

func lawLoadFollowsModel(seed int, steps int) bool {
	let mut r = seeded(seed)
	let mut f = ui.NewFrame[int]()
	let mut cur Option[loaded] = None
	let mut flight = []fetching{}
	let mut used = Map[int, bool]{}
	let mut ready = -1
	for _ in 0..(steps%24+24)%24+2 {
		if draw(&r, 2) == 0 || len(flight) == 0 {
			let mut w = want{at: Nowhere, key: draw(&r, 3)}
			match draw(&r, 3) {
			case 0:
			case 1:
				w.at = Top
			case _:
				w.at = Boxed
			}
			let _ = ui.Render(&f, loadView, w)
			let mut fetches = []ui.Fetch[int]{}
			let mut cancels = []int{}
			for j in ui.Jobs(&f) {
				match j {
				case ui.Fetch[int] as x:
					fetches = fetches.Append(x)
				case ui.Cancel as c:
					cancels = cancels.Append(c.Epoch)
				}
			}
			let mut wantCancel = -1
			let mut wantFetch = false
			match cur {
			case Some[loaded] as c:
				if c.Value.at != w.at || c.Value.key != w.key {
					wantCancel = c.Value.epoch
					wantFetch = w.at != Nowhere
				}
			case None:
				wantFetch = w.at != Nowhere
			}
			if wantCancel >= 0 {
				if len(cancels) != 1 || cancels[0] != wantCancel {
					return false
				}
				cur = None
				ready = -1
			} else if len(cancels) != 0 {
				return false
			}
			if wantFetch {
				if len(fetches) != 1 || used.Get(fetches[0].Epoch) != None {
					return false
				}
				used[fetches[0].Epoch] = true
				cur = Some{Value: loaded{at: w.at, key: w.key, epoch: fetches[0].Epoch}}
				flight = flight.Append(fetching{epoch: fetches[0].Epoch, run: fetches[0].Run})
			} else if len(fetches) != 0 {
				return false
			}
		} else {
			let i = draw(&r, len(flight))
			let x = flight[i]
			let mut rest = []fetching{}
			for j, y in flight {
				if j != i {
					rest = rest.Append(y)
				}
			}
			flight = rest
			let mut wanted = false
			match cur {
			case Some[loaded] as c:
				wanted = c.Value.epoch == x.epoch
			case None:
			}
			let got = sent(ui.Handle(&f, ui.Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal)
			if ui.Arrive(&f, x.epoch) != wanted || (got != -1) != wanted {
				return false
			}
			if wanted {
				ready = got
			}
		}
	}
	for x in flight {
		let got = sent(ui.Handle(&f, ui.Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal)
		if got != -1 {
			ready = got
		}
	}
	match cur {
	case Some[loaded] as c:
		ready == c.Value.key
	case None:
		ready == -1
	}
}


type bstate { key int, keys []int, fail int, gone bool }

let innerKey = 100

func failIf(b bool) fails ui.Pending {
	if b {
		return ui.Pending
	}
}

func failMissing(b bool) fails Missing {
	if b {
		return Missing{V: 0}
	}
}

func labelled(f *ui.Frame[int], name string) {
	ui.Row(f, ui.Plain()) {
		let d = ui.NewDraft(f, name)
		ui.Field(f, d, name)
	}
}

func labelShape(vals Map[string, string], n string) ui.Shape[int] {
	ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: []ui.Shape[int]{input(vals, n, n)}}
}

func bview(f *ui.Frame[int], s bstate) {
	ui.Column(f, ui.Plain()) {
		let side = ui.NewDraft(f, "side")
		ui.Field(f, side, "side")
		let key = s.key
		ui.Load(f, ui.ByInt(key), func() int { key })
		if !s.gone {
			ui.Box(f, ui.Plain()) {
				ui.Boundary(f) {
					if s.fail != 6 {
						try failIf(s.fail == 1)
						let loose = ui.NewDraft(f, "loose")
						ui.Field(f, loose, "loose")
						labelled(f, "mid")
						try failIf(s.fail == 2)
						try failMissing(s.fail == 5)
						try ui.Row(f, ui.Plain()) {
							let inner = ui.NewDraft(f, "inner")
							ui.Field(f, inner, "inner")
							ui.Load(f, ui.ByInt(innerKey), func() int { innerKey })
							ui.Each(f, s.keys, func(k int) ui.Key { ui.ByInt(k) }) func(k int) {
								let d = ui.NewDraft(f, "r\(k)")
								ui.Field(f, d, "r")
							}
							try failIf(s.fail == 3)
						}
						try failIf(s.fail == 4)
					}
				} catch {
				case ui.Pending:
					ui.Text(f, "wait")
					labelled(f, "arm")
				case Missing:
				}
			}
		}
		// A container whose catch is right after it, outside the boundary
		// and with no namespace of its own: its arm writes, at the spec index
		// the body had, the helper the body wrote, so the arm's identity holds
		// only when the cut gives back the counts the body took.
		ui.Row(f, ui.Plain()) {
			try failIf(s.fail == 1)
			labelled(f, "deep")
			try failIf(s.fail == 2 || s.fail == 3)
		} catch {
		case ui.Pending:
			ui.Row(f, ui.Plain()) {
				labelled(f, "deeparm")
			}
		}
		ui.Row(f, ui.Plain()) {
			let tail = ui.NewDraft(f, "tail")
			ui.Field(f, tail, "tail")
		}
		let after = ui.NewDraft(f, "after")
		ui.Field(f, after, "after")
	}
}

func draft(vals Map[string, string], n string) string {
	match vals.Get(n) {
	case Some[string] as v:
		v.Value
	case None:
		n
	}
}

func input(vals Map[string, string], n string, placeholder string) ui.Shape[int] {
	leaf(ui.InputNode{Value: draft(vals, n), Placeholder: placeholder})
}

func bshape(s bstate, vals Map[string, string]) []ui.Shape[int] {
	let mut kids = []ui.Shape[int]{input(vals, "side", "side")}
	if !s.gone {
		let mut box = []ui.Shape[int]{}
		if s.fail == 0 {
			box = box.Append(input(vals, "loose", "loose"))
			box = box.Append(labelShape(vals, "mid"))
			let mut row = []ui.Shape[int]{input(vals, "inner", "inner")}
			for k in s.keys {
				row = row.Append(input(vals, "r\(k)", "r"))
			}
			box = box.Append(ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: row})
		} else if s.fail <= 4 {
			box = box.Append(leaf(ui.TextNode{Value: "wait", Style: ui.Plain()}))
			box = box.Append(labelShape(vals, "arm"))
		}
		kids = kids.Append(ui.Shape[int]{Node: ui.BoxNode{Style: ui.Plain()}, Kids: box})
	}
	let mut deep = "deep"
	if deepStops(s) {
		deep = "deeparm"
	}
	kids = kids.Append(ui.Shape[int]{Node: ui.RowNode{Style: ui.Plain()}, Kids: []ui.Shape[int]{labelShape(vals, deep)}})
	kids = kids.Append(labelShape(vals, "tail"))
	kids = kids.Append(input(vals, "after", "after"))
	[]ui.Shape[int]{ui.Shape[int]{Node: ui.ColumnNode{Style: ui.Plain()}, Kids: kids}}
}

func deepStops(s bstate) bool { s.fail >= 1 && s.fail <= 3 }

func dropGroup(vals Map[string, string]) Map[string, string] {
	let mut out = vals.Delete("loose").Delete("inner").Delete("mid").Delete("arm")
	for k in 0..6 {
		out = out.Delete("r\(k)")
	}
	out
}

func lawBoundaryFollowsModel(seed int, steps int) bool {
	let mut r = seeded(seed)
	let mut f = ui.NewFrame[int]()
	let mut vals = Map[string, string]{}
	let mut s = bstate{key: 0, keys: []int{}, fail: 0, gone: true}
	let mut outer = -1
	let mut okey = -1
	let mut rowMade = false
	let mut inner = -1
	let mut used = Map[int, bool]{}
	for i in 0..(steps%24+24)%24+2 {
		if draw(&r, 2) == 0 || i == 0 {
			let mut next = bstate{key: draw(&r, 3), keys: pick(&r, false), fail: draw(&r, 7), gone: draw(&r, 6) == 0}
			if draw(&r, 3) == 0 {
				next.fail = 0
			}
			if s.fail == 5 && draw(&r, 2) == 0 {
				// A stopped pass that writes nothing, then a completed one that
				// writes nothing: the parent's children are the same, and
				// what it parked is released.
				next.fail = 6
			}
			let _ = ui.Render(&f, bview, next)
			s = next
			let mut fetchOuter = []int{}
			let mut fetchInner = []int{}
			let mut cancels = []int{}
			for j in ui.Jobs(&f) {
				match j {
				case ui.Fetch[int] as x:
					if used.Get(x.Epoch) != None {
						return false
					}
					used[x.Epoch] = true
					if x.Run() == innerKey {
						fetchInner = fetchInner.Append(x.Epoch)
					} else if x.Run() == s.key {
						fetchOuter = fetchOuter.Append(x.Epoch)
					} else {
						return false
					}
				case ui.Cancel as c:
					cancels = cancels.Append(c.Epoch)
				}
			}
			for c in cancels {
				if ui.Arrive(&f, c) {
					return false
				}
			}
			if s.key != okey {
				if len(fetchOuter) != 1 || (outer >= 0 && ui.Arrive(&f, outer)) {
					return false
				}
				outer = fetchOuter[0]
				okey = s.key
			} else if len(fetchOuter) != 0 {
				return false
			}
			if !ui.Arrive(&f, outer) {
				return false
			}
			let releases = (s.gone || s.fail == 6) && !deepStops(s)
			if releases {
				if len(fetchInner) != 0 || (inner >= 0 && ui.Arrive(&f, inner)) {
					return false
				}
				inner = -1
				rowMade = false
				vals = dropGroup(vals)
			} else if inner >= 0 && (rowMade || s.fail != 0 || s.gone) {
				if len(fetchInner) != 0 || !ui.Arrive(&f, inner) {
					return false
				}
			} else if !s.gone && (s.fail == 0 || s.fail == 3 || s.fail == 4) {
				// The first pass that completes the ui.Row after a stopped one
				// that wrote it (with no ui.Row node to park the load in) may
				// fetch again (a known gap: the load waited in the group's
				// parent); the one before is then cancelled.
				if inner >= 0 && ui.Arrive(&f, inner) && len(fetchInner) != 0 {
					return false
				}
				let mut live = 0
				for e in fetchInner {
					if ui.Arrive(&f, e) {
						inner = e
						live = live + 1
					}
				}
				if live != 1 {
					return false
				}
			} else if len(fetchInner) != 0 {
				return false
			}
			if !deepStops(s) {
				vals = vals.Delete("deeparm")
			}
			if !s.gone && s.fail == 0 {
				rowMade = true
				vals = vals.Delete("arm")
				for k in 0..6 {
					let mut kept = false
					for x in s.keys {
						kept = kept || x == k
					}
					if !kept {
						vals = vals.Delete("r\(k)")
					}
				}
			}
			if (releases || s.fail == 0) && ui.Retained(&f) != ui.Len(&f) {
				return false
			}
		} else {
			let col = ui.Kids(&f, ui.Root(&f))[0]
			let mut names = []string{"side"}
			let mut ids = []ui.Id[int]{ui.Kids(&f, col)[0]}
			let mut next = 1
			if !s.gone {
				let box = ui.Kids(&f, col)[1]
				next = 2
				if s.fail == 0 {
					names = names.Append("loose")
					ids = ids.Append(ui.Kids(&f, box)[0])
					names = names.Append("mid")
					ids = ids.Append(ui.Kids(&f, ui.Kids(&f, box)[1])[0])
					let row = ui.Kids(&f, box)[2]
					names = names.Append("inner")
					ids = ids.Append(ui.Kids(&f, row)[0])
					for j, k in s.keys {
						names = names.Append("r\(k)")
						ids = ids.Append(ui.Kids(&f, row)[j+1])
					}
				} else if s.fail <= 4 {
					names = names.Append("arm")
					ids = ids.Append(ui.Kids(&f, ui.Kids(&f, box)[1])[0])
				}
			}
			if deepStops(s) {
				names = names.Append("deeparm")
			} else {
				names = names.Append("deep")
			}
			ids = ids.Append(ui.Kids(&f, ui.Kids(&f, ui.Kids(&f, col)[next])[0])[0])
			names = names.Append("tail")
			ids = ids.Append(ui.Kids(&f, ui.Kids(&f, col)[next+1])[0])
			names = names.Append("after")
			ids = ids.Append(ui.Kids(&f, col)[next+2])
			let at = draw(&r, len(names))
			let old = draft(vals, names[at])
			let now = "x\(i % 3)"
			let done = ui.Handle(&f, ui.Type[int]{Id: ids[at], Text: now})
			if done.Changed != (now != old) {
				return false
			}
			vals[names[at]] = now
			let _ = ui.Render(&f, bview, s)
			if len(ui.Jobs(&f)) != 0 {
				return false
			}
		}
		if !ui.SameShapes(ui.FrameShape(&f), bshape(s, vals)) || !ui.SameShapes(ui.TreeShape(&f), bshape(s, vals)) {
			return false
		}
	}
	true
}

func lawAwaitIsTheMatch(seed int) bool {
	let mut r = seeded(seed)
	let v = draw(&r, 64)
	let mut a ui.Async[int, Missing] = ui.Loading
	let pick = draw(&r, 3)
	if pick == 1 {
		a = ui.Ready{Value: v}
	} else if pick == 2 {
		a = ui.Failed{Error: Missing{V: v}}
	}
	let want = match a {
	case ui.Loading:
		-1
	case ui.Ready[int] as x:
		x.Value
	case ui.Failed[Missing] as x:
		-2 - x.Error.V
	}
	let got = ui.Await(a) catch {
	case ui.Pending:
		-1
	case Missing as m:
		-2 - m.V
	}
	got == want
}

test UiStateKeeps(seed int, steps int) {
	assert lawStateKeeps(seed, steps)
}

test UiFreshMountInits(seed int, steps int) {
	assert lawFreshMountInits(seed, steps)
}

test UiLoadFollowsModel(seed int, steps int) {
	assert lawLoadFollowsModel(seed, steps)
}

test UiBoundaryFollowsModel(seed int, steps int) {
	assert lawBoundaryFollowsModel(seed, steps)
}

test UiAwaitIsTheMatch(seed int) {
	assert lawAwaitIsTheMatch(seed)
}
`

// stdUILoadStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUILoadStatements = map[string]string{
	"ui StateKeeps":           `StateKeeps(seed int, steps int) { cellsFollowModel(seed, steps, false) }`,
	"ui FreshMountInits":      `FreshMountInits(seed int, steps int) { cellsFollowModel(seed, steps, true) }`,
	"ui LoadFollowsModel":     `LoadFollowsModel(seed int, steps int) { let mut r = seeded(seed) let mut f = NewFrame[int]() let mut cur Option[loaded] = None let mut flight = []fetching{} let mut used = Map[int, bool]{} let mut ready = -1 for _ in 0..(steps%24+24)%24+2 { if draw(&r, 2) == 0 || len(flight) == 0 { let mut w = want{at: Nowhere, key: draw(&r, 3)} match draw(&r, 3) { case 0: case 1: w.at = Top case _: w.at = Boxed } let _ = Render(&f, loadView, w) let mut fetches = []Fetch[int]{} let mut cancels = []int{} for j in Jobs(&f) { match j { case Fetch[int] as x: fetches = fetches.Append(x) case Cancel as c: cancels = cancels.Append(c.Epoch) } } let mut wantCancel = -1 let mut wantFetch = false match cur { case Some[loaded] as c: if c.Value.at != w.at || c.Value.key != w.key { wantCancel = c.Value.epoch wantFetch = w.at != Nowhere } case None: wantFetch = w.at != Nowhere } if wantCancel >= 0 { if len(cancels) != 1 || cancels[0] != wantCancel { return false } cur = None ready = -1 } else if len(cancels) != 0 { return false } if wantFetch { if len(fetches) != 1 || used.Get(fetches[0].Epoch) != None { return false } used[fetches[0].Epoch] = true cur = Some{Value: loaded{at: w.at, key: w.key, epoch: fetches[0].Epoch}} flight = flight.Append(fetching{epoch: fetches[0].Epoch, run: fetches[0].Run}) } else if len(fetches) != 0 { return false } } else { let i = draw(&r, len(flight)) let x = flight[i] let mut rest = []fetching{} for j, y in flight { if j != i { rest = rest.Append(y) } } flight = rest let mut wanted = false match cur { case Some[loaded] as c: wanted = c.Value.epoch == x.epoch case None: } let got = sent(Handle(&f, Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal) if Arrive(&f, x.epoch) != wanted || (got != -1) != wanted { return false } if wanted { ready = got } } } for x in flight { let got = sent(Handle(&f, Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal) if got != -1 { ready = got } } match cur { case Some[loaded] as c: ready == c.Value.key case None: ready == -1 } }`,
	"ui BoundaryFollowsModel": `BoundaryFollowsModel(seed int, steps int) { let mut r = seeded(seed) let mut f = NewFrame[int]() let mut vals = Map[string, string]{} let mut s = bstate{key: 0, keys: []int{}, fail: 0, gone: true} let mut outer = -1 let mut okey = -1 let mut rowMade = false let mut inner = -1 let mut used = Map[int, bool]{} for i in 0..(steps%24+24)%24+2 { if draw(&r, 2) == 0 || i == 0 { let mut next = bstate{key: draw(&r, 3), keys: pick(&r, false), fail: draw(&r, 7), gone: draw(&r, 6) == 0} if draw(&r, 3) == 0 { next.fail = 0 } if s.fail == 5 && draw(&r, 2) == 0 { // A stopped pass that writes nothing, then a completed one that // writes nothing: the parent's children are the same, and // what it parked is released. next.fail = 6 } let _ = Render(&f, bview, next) s = next let mut fetchOuter = []int{} let mut fetchInner = []int{} let mut cancels = []int{} for j in Jobs(&f) { match j { case Fetch[int] as x: if used.Get(x.Epoch) != None { return false } used[x.Epoch] = true if x.Run() == innerKey { fetchInner = fetchInner.Append(x.Epoch) } else if x.Run() == s.key { fetchOuter = fetchOuter.Append(x.Epoch) } else { return false } case Cancel as c: cancels = cancels.Append(c.Epoch) } } for c in cancels { if Arrive(&f, c) { return false } } if s.key != okey { if len(fetchOuter) != 1 || (outer >= 0 && Arrive(&f, outer)) { return false } outer = fetchOuter[0] okey = s.key } else if len(fetchOuter) != 0 { return false } if !Arrive(&f, outer) { return false } let releases = (s.gone || s.fail == 6) && !deepStops(s) if releases { if len(fetchInner) != 0 || (inner >= 0 && Arrive(&f, inner)) { return false } inner = -1 rowMade = false vals = dropGroup(vals) } else if inner >= 0 && (rowMade || s.fail != 0 || s.gone) { if len(fetchInner) != 0 || !Arrive(&f, inner) { return false } } else if !s.gone && (s.fail == 0 || s.fail == 3 || s.fail == 4) { // The first pass that completes the Row after a stopped one // that wrote it (with no Row node to park the load in) may // fetch again (a known gap: the load waited in the group's // parent); the one before is then cancelled. if inner >= 0 && Arrive(&f, inner) && len(fetchInner) != 0 { return false } let mut live = 0 for e in fetchInner { if Arrive(&f, e) { inner = e live = live + 1 } } if live != 1 { return false } } else if len(fetchInner) != 0 { return false } if !deepStops(s) { vals = vals.Delete("deeparm") } if !s.gone && s.fail == 0 { rowMade = true vals = vals.Delete("arm") for k in 0..6 { let mut kept = false for x in s.keys { kept = kept || x == k } if !kept { vals = vals.Delete("r\(k)") } } } if (releases || s.fail == 0) && Retained(&f) != Len(&f) { return false } } else { let col = Kids(&f, Root(&f))[0] let mut names = []string{"side"} let mut ids = []Id[int]{Kids(&f, col)[0]} let mut next = 1 if !s.gone { let box = Kids(&f, col)[1] next = 2 if s.fail == 0 { names = names.Append("loose") ids = ids.Append(Kids(&f, box)[0]) names = names.Append("mid") ids = ids.Append(Kids(&f, Kids(&f, box)[1])[0]) let row = Kids(&f, box)[2] names = names.Append("inner") ids = ids.Append(Kids(&f, row)[0]) for j, k in s.keys { names = names.Append("r\(k)") ids = ids.Append(Kids(&f, row)[j+1]) } } else if s.fail <= 4 { names = names.Append("arm") ids = ids.Append(Kids(&f, Kids(&f, box)[1])[0]) } } if deepStops(s) { names = names.Append("deeparm") } else { names = names.Append("deep") } ids = ids.Append(Kids(&f, Kids(&f, Kids(&f, col)[next])[0])[0]) names = names.Append("tail") ids = ids.Append(Kids(&f, Kids(&f, col)[next+1])[0]) names = names.Append("after") ids = ids.Append(Kids(&f, col)[next+2]) let at = draw(&r, len(names)) let old = draft(vals, names[at]) let now = "x\(i % 3)" let done = Handle(&f, Type[int]{Id: ids[at], Text: now}) if done.Changed != (now != old) { return false } vals[names[at]] = now let _ = Render(&f, bview, s) if len(Jobs(&f)) != 0 { return false } } if !SameShapes(FrameShape(&f), bshape(s, vals)) || !SameShapes(TreeShape(&f), bshape(s, vals)) { return false } } true }`,
	"ui AwaitIsTheMatch":      `AwaitIsTheMatch(seed int) { let mut r = seeded(seed) let v = draw(&r, 64) let mut a Async[int, Missing] = Loading let pick = draw(&r, 3) if pick == 1 { a = Ready{Value: v} } else if pick == 2 { a = Failed{Error: Missing{V: v}} } let want = match a { case Loading: -1 case Ready[int] as x: x.Value case Failed[Missing] as x: -2 - x.Error.V } let got = Await(a) catch { case Pending: -1 case Missing as m: -2 - m.V } got == want }`,
}

// TestUILoadModel runs the probe's tests with --seed 1; lawledger.Run holds that
// the probe restates exactly the ledger's ui laws of this runner, each
// with the row's statement, and that each passed. The mutants of
// internal/sketch/std/ui in testdata/mutants.json are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestUILoadModel(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stduiload", stdUILoadProbe, stdUILoadStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
