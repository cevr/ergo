package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdUITextProbe restates each law of std ui (ledger runner TestUITextModel) as a
// property test of its own name (stdLawsTestName): the laws' helpers over
// the package's exported operations, each law a function the test asserts.
// It is generated from internal/sketch/std/ui/laws_text.ergo (with the helpers of laws.ergo); a law changed in
// source fails TestUITextModel until this restatement changes with it.
const stdUITextProbe = `import ui
import utf8

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




let pieces = []string{"a", "bb", "ccc", "dddddddddddd", " ", "  ", "\n", "é", "日本", "x y"}

type textCase { m ui.Monospace, text string, width ui.Length }

func drawText(seed int) textCase {
	let mut r = seeded(seed)
	let m = ui.Monospace{Advance: ui.Length(float64(draw(&r, 3) + 1)), Line: ui.Length(float64(draw(&r, 3) + 2))}
	let mut text = ""
	for _ in 0..draw(&r, 14) {
		text = text + pieces[draw(&r, len(pieces))]
	}
	textCase{m: m, text: text, width: ui.Length(float64(draw(&r, 24)))}
}

let mono = ui.Font{Family: "mono", Size: 12.0}

func trimmed(s string) string {
	let mut e = len(s)
	for e > 0 && s[e-1] == ' ' {
		e = e - 1
	}
	s[:e]
}

func sizeAt(s string, at int) int { utf8.RuneLen(utf8.DecodeRuneInString(s[at:]).Rune) }

func newlineAt(s string, from int) int {
	let mut i = from
	for i < len(s) && s[i] != '\n' {
		i = i + 1
	}
	i
}

func boundaryList(s string) []int {
	let mut out = []int{0}
	let mut o = 0
	for o < len(s) {
		o = o + sizeAt(s, o)
		out = out.Append(o)
	}
	out
}

func overflows(m ui.Monospace, text string, ln ui.Line, width ui.Length) bool {
	if text[ln.End-1] == ' ' {
		let pe = newlineAt(text, ln.End)
		let mut k = ln.End + 1
		for k < pe && !(text[k-1] == ' ' && text[k] != ' ') {
			k = k + 1
		}
		return m.Width(trimmed(text[ln.Start:k]), mono) > width
	}
	m.Width(text[ln.Start:ln.End+sizeAt(text, ln.End)], mono) > width
}

func lawLinesCoverText(seed int) bool {
	let c = drawText(seed)
	let l = ui.LayoutText(c.m, c.text, mono, c.width)
	if len(l.Lines) == 0 {
		return false
	}
	let mut at = 0
	for i, ln in l.Lines {
		if ln.Start != at || ln.End < ln.Start || ln.End > len(c.text) {
			return false
		}
		for j in ln.Start..ln.End {
			if c.text[j] == '\n' {
				return false
			}
		}
		let shown = trimmed(c.text[ln.Start:ln.End])
		if ln.Width != c.m.Width(shown, mono) || ln.Width > c.width && utf8.RuneCount(shown) > 1 {
			return false
		}
		if ln.Wrapped {
			if ln.End == ln.Start || !overflows(c.m, c.text, ln, c.width) {
				return false
			}
			at = ln.End
		} else if i < len(l.Lines)-1 {
			if ln.End >= len(c.text) || c.text[ln.End] != '\n' {
				return false
			}
			at = ln.End + 1
		} else if ln.End != len(c.text) {
			return false
		}
	}
	true
}

func lawHitFindsTheCaret(seed int, a int, b int) bool {
	let c = drawText(seed)
	let l = ui.LayoutText(c.m, c.text, mono, c.width)
	let bounds = boundaryList(c.text)
	for o in bounds {
		let k = ui.CaretAt(c.m, l, o)
		if ui.OffsetAt(c.m, l, k.X, k.Top+k.Height/2.0) != o {
			return false
		}
	}
	let from = bounds[(a%len(bounds)+len(bounds))%len(bounds)]
	let to = bounds[(b%len(bounds)+len(bounds))%len(bounds)]
	let rects = ui.SelectionRects(c.m, l, from, to)
	for o in bounds {
		if o < len(c.text) && c.text[o] != '\n' {
			let k = ui.CaretAt(c.m, l, o)
			let x = k.X + c.m.Width(c.text[o:o+sizeAt(c.text, o)], mono)/2.0
			let y = k.Top + k.Height/2.0
			let mut inside = false
			for r in rects {
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					inside = true
				}
			}
			let want = from <= o && o < to || to <= o && o < from
			if inside != want {
				return false
			}
		}
	}
	true
}

test UiLinesCoverText(seed int) {
	assert lawLinesCoverText(seed)
}

test UiHitFindsTheCaret(seed int, a int, b int) {
	assert lawHitFindsTheCaret(seed, a, b)
}

test UiMeasureEmptyIsZero(advance float64, line float64, size float64) {
	let m = ui.Monospace{Advance: ui.Length(advance), Line: ui.Length(line)}
	let font = ui.Font{Family: "mono", Size: ui.Length(size)}
	assert m.Width("", font) == 0.0
}

test UiMeasureWidthGrows(advance float64, a string, b string) {
	let m = ui.Monospace{Advance: ui.Length(advance), Line: 1.0}
	let font = ui.Font{Family: "mono", Size: 12.0}
	assert m.Width(a+b, font) >= m.Width(a, font)
}

test UiClipboardWriteThenRead(first string, text string) {
	let c = ui.NewMemoryClipboard()
	let d = c
	c.Write(first)
	d.Write(text)
	assert c.Read() == text
}
`

// stdUITextStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUITextStatements = map[string]string{
	"ui LinesCoverText":          `LinesCoverText(seed int) { let c = drawText(seed) let l = LayoutText(c.m, c.text, mono, c.width) if len(l.Lines) == 0 { return false } let mut at = 0 for i, ln in l.Lines { if ln.Start != at || ln.End < ln.Start || ln.End > len(c.text) { return false } for j in ln.Start..ln.End { if c.text[j] == '\n' { return false } } let shown = trimmed(c.text[ln.Start:ln.End]) if ln.Width != c.m.Width(shown, mono) || ln.Width > c.width && utf8.RuneCount(shown) > 1 { return false } if ln.Wrapped { if ln.End == ln.Start || !overflows(c.m, c.text, ln, c.width) { return false } at = ln.End } else if i < len(l.Lines)-1 { if ln.End >= len(c.text) || c.text[ln.End] != '\n' { return false } at = ln.End + 1 } else if ln.End != len(c.text) { return false } } true }`,
	"ui HitFindsTheCaret":        `HitFindsTheCaret(seed int, a int, b int) { let c = drawText(seed) let l = LayoutText(c.m, c.text, mono, c.width) let bounds = boundaryList(c.text) for o in bounds { let k = CaretAt(c.m, l, o) if OffsetAt(c.m, l, k.X, k.Top+k.Height/2.0) != o { return false } } let from = bounds[(a%len(bounds)+len(bounds))%len(bounds)] let to = bounds[(b%len(bounds)+len(bounds))%len(bounds)] let rects = SelectionRects(c.m, l, from, to) for o in bounds { if o < len(c.text) && c.text[o] != '\n' { let k = CaretAt(c.m, l, o) let x = k.X + c.m.Width(c.text[o:o+sizeAt(c.text, o)], mono)/2.0 let y = k.Top + k.Height/2.0 let mut inside = false for r in rects { if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H { inside = true } } let want = from <= o && o < to || to <= o && o < from if inside != want { return false } } } true }`,
	"ui Measure.EmptyIsZero":     `EmptyIsZero(font Font) { Width("", font) == 0.0 }`,
	"ui Measure.WidthGrows":      `WidthGrows(a string, b string, font Font) { Width(a+b, font) >= Width(a, font) }`,
	"ui Clipboard.WriteThenRead": `WriteThenRead(text string) { Write(text) Read() == text }`,
}

// TestUITextModel runs the probe's tests with --seed 1; lawledger.Run holds that
// the probe restates exactly the ledger's ui laws of this runner, each
// with the row's statement, and that each passed. The mutants of
// internal/sketch/std/ui in testdata/mutants.json are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestUITextModel(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stduitext", stdUITextProbe, stdUITextStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
