package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdUIHostProbe restates each law of std ui (ledger runner TestUIHostModel) as a
// property test of its own name (stdLawsTestName): the laws' helpers over
// the package's exported operations, each law a function the test asserts.
// It is generated from internal/sketch/std/ui/laws_host.ergo (with the helpers of laws.ergo); a law changed in
// source fails TestUIHostModel until this restatement changes with it.
const stdUIHostProbe = `import ui
import actor
import machines
import queue
import scope
import strings
import time

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
	let kids = []ui.Shape[int]{leaf(ui.ToggleNode{Label: "tg\(v % 3)", On: true}), ui.Shape[int]{Node: ui.AreaNode{Label: "area\(v % 2)", Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: "in area", Style: ui.Plain()})}}, leaf(ui.CanvasNode{Label: "c\(v % 4)", Width: 4, Height: 4, Paint: []ui.PaintOp{}}), leaf(ui.RowsNode{Label: "rows", Count: 0, RowHeight: 10, First: 0})}
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




type shelf { rows []int, next int }

func shelfStep(s shelf, e int) Decision[shelf, int] {
	if e == 0 {
		return Enter[shelf]{Next: shelf{rows: s.rows.Append(s.next), next: s.next + 1}}
	}
	let mut rows = []int{}
	for k in s.rows {
		if k != e {
			rows = rows.Append(k)
		}
	}
	Enter[shelf]{Next: shelf{rows: rows, next: s.next}}
}

func shelfTimer(s shelf) Option[Timeout[int]] { None }

func shelfWork(s shelf) Option[func(send func(e int))] { None }

func shelfMachine() Machine[shelf, int, int] {
	Machine[shelf, int, int]{Initial: shelf{rows: []int{}, next: 1}, Step: shelfStep, After: shelfTimer, Work: shelfWork}
}

func shelfView(f *ui.Frame[int], s shelf) {
	ui.Column(f, ui.Plain()) {
		ui.Text(f, "\(len(s.rows)) rows")
		ui.Pressable(f, "add", 0) {
			ui.Text(f, "+")
		}
		ui.Each(f, s.rows, func(k int) ui.Key { ui.ByInt(k) }) func(k int) {
			ui.Row(f, ui.Plain()) {
				ui.Text(f, "row \(k)")
				ui.Pressable(f, "del", k) {
					ui.Text(f, "x")
				}
			}
		}
	}
}

func freshShape[S](view func(f *ui.Frame[int], s S), s S) []ui.Shape[int] {
	let mut g = ui.NewFrame[int]()
	let _ = ui.Render(&g, view, s)
	ui.FrameShape(&g)
}

func lawScriptShowsTheView(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		scriptShows(seed, steps, m)
	}
}

func scriptShows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let a = actor.StartMachine(shelfMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, shelfView, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let start = ui.Now(&h)
	let mut model = shelf{rows: []int{}, next: 1}
	for _ in 0..(steps%16+16)%16+1 {
		let mut label = "add"
		if len(model.rows) > 0 && draw(&r, 2) == 0 {
			label = "del"
			model = machines.Next(model, shelfStep(model, model.rows[0]))
		} else {
			model = machines.Next(model, shelfStep(model, 0))
		}
		// Half the presses go by locator, and each step expects the count
		// the model shows and the number of delete buttons (H4r).
		let mut tap ui.Act = ui.TapOn{Label: label}
		if draw(&r, 2) == 0 {
			tap = ui.ClickOn{At: ui.WithRole{Role: ui.Button, Name: label}}
		}
		let expect = []ui.Act{ui.ExpectOn{At: ui.WithText{Text: "\(len(model.rows)) rows"}, Is: ui.ToExist}, ui.ExpectOn{At: ui.WithLabel{Label: "del"}, Is: ui.ToHaveCount{Count: len(model.rows)}}}
		ui.Play(&h, []ui.Act{tap, expect[0], expect[1]}) catch {
		case _:
			return false
		}
		let mut refused = false
		ui.Play(&h, []ui.Act{ui.ExpectOn{At: ui.WithLabel{Label: "del"}, Is: ui.ToHaveCount{Count: len(model.rows) + 1}}}) catch {
		case ui.Mismatch:
			refused = true
		case _:
			return false
		}
		if !refused {
			return false
		}
		let s = ui.StateOf(&h)
		if s != model || !ui.SameShapes(ui.Screen(&h), freshShape(shelfView, s)) || ui.Now(&h) != start {
			return false
		}
	}
	true
}

type chosen { key int, ready int }

func chosenStep(s chosen, e int) Decision[chosen, int] {
	if e >= 0 {
		return Enter[chosen]{Next: chosen{key: e, ready: -1}}
	}
	Enter[chosen]{Next: chosen{key: s.key, ready: -1 - e}}
}

func chosenTimer(s chosen) Option[Timeout[int]] { None }

func chosenWork(s chosen) Option[func(send func(e int))] { None }

func chosenMachine() Machine[chosen, int, int] {
	Machine[chosen, int, int]{Initial: chosen{key: 0, ready: -1}, Step: chosenStep, After: chosenTimer, Work: chosenWork}
}

func valueOf(k int) int { k*10 + 7 }

func lawCancelledFetchStops(steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		cancelStops(steps, m)
	}
}

func cancelStops(steps int, m time.Manual) bool needs Scheduler {
	let started = queue.New[int](64)
	let ended = queue.New[int](64)
	let view = func(f *ui.Frame[int], s chosen) {
		let k = s.key
		ui.Text(f, "key \(k) ready \(s.ready)")
		ui.Pressable(f, "next", k+1+k%3) {
			ui.Text(f, "next")
		}
		ui.Load(f, ui.ByInt(k), func() int {
			started.Push(k) catch {
			case queue.Closed:
			}
			scope.OnExit() {
				ended.Push(k) catch {
				case queue.Closed:
				}
			}
			time.Sleep(m, 1h)
			-1 - valueOf(k)
		})
	}
	let a = actor.StartMachine(chosenMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let first = started.Pop() catch {
	case queue.Closed:
		return false
	}
	if first != 0 {
		return false
	}
	for _ in 0..(steps%8+8)%8+1 {
		let old = ui.StateOf(&h).key
		ui.Play(&h, []ui.Act{ui.TapOn{Label: "next"}}) catch {
		case _:
			return false
		}
		let s = ui.StateOf(&h)
		if ended.Poll() != (Some{Value: old}) || ui.Fetching(&h) != 1 || s.ready != -1 {
			return false
		}
		let now = started.Pop() catch {
		case queue.Closed:
			return false
		}
		if now != s.key {
			return false
		}
	}
	ui.Play(&h, []ui.Act{ui.AdvanceBy{By: 1h}, ui.SettleAll}) catch {
	case _:
		return false
	}
	let s = ui.StateOf(&h)
	s.ready == valueOf(s.key) && ui.Fetching(&h) == 0 && ui.SameShapes(ui.Screen(&h), freshShape(view, s))
}

type spin { left int, at int }

func spinStep(s spin, e int) Decision[spin, int] {
	if e >= 0 {
		return Enter[spin]{Next: spin{left: e, at: s.at}}
	}
	Enter[spin]{Next: spin{left: s.left - 1, at: -1 - e}}
}

func spinTimer(s spin) Option[Timeout[int]] { None }

func spinWork(s spin) Option[func(send func(e int))] { None }

func spinMachine() Machine[spin, int, int] {
	Machine[spin, int, int]{Initial: spin{left: 0, at: -1}, Step: spinStep, After: spinTimer, Work: spinWork}
}

func spinView(f *ui.Frame[int], s spin) {
	ui.Text(f, "left \(s.left) at \(s.at)")
	for n in 0..4 {
		ui.Pressable(f, "go\(n)", n) {
			ui.Text(f, "go")
		}
	}
	if s.left > 0 {
		ui.Animate(f, func(at duration) int { -1 - int(int64(at)) })
	}
}

type frameModel { since duration, owed duration, frames int, app spin }

func advanced(m frameModel, d duration, p duration) frameModel {
	let mut x = m
	let mut left = d
	for x.app.left > 0 && left >= p-x.owed {
		let due = p - x.owed
		x.since = x.since + due
		left = left - due
		x.owed = 0ns
		x.frames = x.frames + 1
		x.app = spin{left: x.app.left - 1, at: int(int64(x.since))}
	}
	x.since = x.since + left
	if x.app.left > 0 {
		x.owed = x.owed + left
	} else {
		x.owed = 0ns
	}
	x
}

func lawNoTickWhenSettled(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		ticksFollow(seed, steps, m)
	}
}

func ticksFollow(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let a = actor.StartMachine(spinMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, spinView, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let p = 16ms
	let start = ui.Now(&h)
	let mut model = frameModel{since: 0ns, owed: 0ns, frames: 0, app: spin{left: 0, at: -1}}
	let moves = []duration{0ns, 5ms, 11ms, 16ms, 40ms, 100ms}
	for _ in 0..(steps%24+24)%24+1 {
		let mut act ui.Act = ui.SettleAll
		if draw(&r, 2) == 0 {
			let n = draw(&r, 4)
			act = ui.TapOn{Label: "go\(n)"}
			model.app = machines.Next(model.app, spinStep(model.app, n))
		} else {
			let d = moves[draw(&r, len(moves))]
			act = ui.AdvanceBy{By: d}
			model = advanced(model, d, p)
		}
		ui.Play(&h, []ui.Act{act}) catch {
		case _:
			return false
		}
		let s = ui.StateOf(&h)
		if s != model.app || ui.Frames(&h) != model.frames || ui.Now(&h).Sub(start) != model.since || ui.WantsFrames(&h) != (model.app.left > 0) || !ui.SameShapes(ui.Screen(&h), freshShape(spinView, s)) {
			return false
		}
	}
	true
}

type pad { open bool, last int, n int }

func padStep(s pad, e int) Decision[pad, int] {
	if e == 1 {
		return Enter[pad]{Next: pad{open: false, last: s.last, n: s.n}}
	}
	if e == 2 {
		return Enter[pad]{Next: pad{open: true, last: s.last, n: s.n}}
	}
	Enter[pad]{Next: pad{open: s.open, last: e, n: s.n + 1}}
}

func padTimer(s pad) Option[Timeout[int]] { None }

func padWork(s pad) Option[func(send func(e int))] { None }

func padMachine() Machine[pad, int, int] {
	Machine[pad, int, int]{Initial: pad{open: false, last: 0, n: 0}, Step: padStep, After: padTimer, Work: padWork}
}

func onPad(s pad, p ui.PointerEvent) Option[int] {
	match p.Phase {
	case ui.Down:
		Some{Value: 1000 + int(p.X) + 100*(s.n%3)}
	case ui.Up:
		Some{Value: 2000 + int(p.Y)}
	case ui.Wheel as w:
		Some{Value: 3000 + int(w.DY)}
	case ui.Moved:
		None
	}
}

func padView(f *ui.Frame[int], s pad) {
	ui.Text(f, "last \(s.last) of \(s.n)")
	if s.open {
		ui.OnKey(f, func(k ui.KeyPress) Option[int] {
			if k.Name == "Escape" {
				return Some{Value: 1}
			}
			None
		})
	}
	ui.OnKey(f, func(k ui.KeyPress) Option[int] {
		if k.Mods.Ctrl {
			return Some{Value: 2}
		}
		if k.Name == "Enter" {
			return Some{Value: 4005}
		}
		None
	})
	ui.Area(f, "pad", ui.Plain(), func(p ui.PointerEvent) Option[int] { onPad(s, p) }) {
		ui.Text(f, "pad")
	}
}

func keySignal(s pad, k ui.KeyPress) Option[int] {
	if s.open && k.Name == "Escape" {
		return Some{Value: 1}
	}
	if k.Mods.Ctrl {
		return Some{Value: 2}
	}
	if k.Name == "Enter" {
		return Some{Value: 4005}
	}
	None
}

func lawInputGivesSignal(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		inputFollows(seed, steps, m)
	}
}

func inputFollows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let a = actor.StartMachine(padMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, padView, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut model = pad{open: false, last: 0, n: 0}
	let names = []string{"a", "Enter", "Escape"}
	for _ in 0..(steps%24+24)%24+1 {
		let mut act ui.Act = ui.SettleAll
		let mut sent Option[int] = None
		if draw(&r, 2) == 0 {
			let k = ui.KeyPress{Name: names[draw(&r, len(names))], Mods: ui.Mods{Shift: false, Ctrl: draw(&r, 2) == 0, Alt: false, Meta: false}}
			act = ui.KeyIn{Press: k}
			sent = keySignal(model, k)
		} else {
			let x = float64(draw(&r, 100)) + 0.5
			let y = float64(draw(&r, 100)) + 0.25
			let mut phase ui.Phase = ui.Moved
			match draw(&r, 4) {
			case 0:
				phase = ui.Down
			case 1:
				phase = ui.Up
			case 2:
				phase = ui.Wheel{DX: 0.0, DY: float64(draw(&r, 9))}
			case _:
			}
			let ev = ui.PointerEvent{Phase: phase, X: x, Y: y}
			act = ui.PointOn{Label: "pad", Pointer: ev}
			sent = onPad(model, ev)
		}
		match sent {
		case Some[int] as e:
			model = machines.Next(model, padStep(model, e.Value))
		case None:
		}
		ui.Play(&h, []ui.Act{act}) catch {
		case _:
			return false
		}
		let s = ui.StateOf(&h)
		if s != model || !ui.SameShapes(ui.Screen(&h), freshShape(padView, s)) {
			return false
		}
	}
	true
}

type pp = ppFill{k int} | ppStroke{k int} | ppRect{k int} | ppMove{k int, kids []pp} | ppClip{k int, kids []pp}

func genPaint(r *rng, depth int) []pp {
	let mut out = []pp{}
	for _ in 0..draw(r, 4) {
		let k = draw(r, 50)
		let kind = draw(r, 5)
		if depth == 0 || kind < 3 {
			match kind % 3 {
			case 0:
				out = out.Append(ppFill{k: k})
			case 1:
				out = out.Append(ppStroke{k: k})
			case _:
				out = out.Append(ppRect{k: k})
			}
		} else if kind == 3 {
			out = out.Append(ppMove{k: k, kids: genPaint(r, depth-1)})
		} else {
			out = out.Append(ppClip{k: k, kids: genPaint(r, depth-1)})
		}
	}
	out
}

func num(s int, k int, i int) float64 { float64((s*7+k*13+i*5)%60) + 0.5 }

func tone(s int, k int) ui.Color { ui.Color{R: uint8((s + k) % 256), G: uint8(k % 256), B: uint8((s * 3) % 256), A: 255} }

func tri(s int, k int) ui.Path {
	ui.Path{Segs: []ui.Seg{ui.MoveTo{X: num(s, k, 0), Y: num(s, k, 1)}, ui.LineTo{X: num(s, k, 2), Y: num(s, k, 3)}, ui.QuadTo{CX: num(s, k, 4), CY: num(s, k, 5), X: num(s, k, 6), Y: num(s, k, 7)}, ui.CubicTo{C1X: num(s, k, 8), C1Y: num(s, k, 9), C2X: num(s, k, 10), C2Y: num(s, k, 11), X: num(s, k, 12), Y: num(s, k, 13)}, ui.ClosePath}}
}

func paintProg(p *ui.Paint, prog []pp, s int) {
	for op in prog {
		match op {
		case ppFill as x:
			ui.Fill(p, tri(s, x.k), tone(s, x.k))
		case ppStroke as x:
			ui.Stroke(p, tri(s, x.k), tone(s, x.k), num(s, x.k, 14))
		case ppRect as x:
			ui.Fill(p, ui.RectPath(num(s, x.k, 0), num(s, x.k, 1), num(s, x.k, 2), num(s, x.k, 3)), tone(s, x.k))
		case ppMove as x:
			ui.Transformed(p, ui.Affine{A: num(s, x.k, 0), B: 0.0, C: 0.0, D: num(s, x.k, 1), E: num(s, x.k, 2), F: num(s, x.k, 3)}) {
				paintProg(p, x.kids, s)
			}
		case ppClip as x:
			ui.Clipped(p, num(s, x.k, 0), num(s, x.k, 1), num(s, x.k, 2), num(s, x.k, 3)) {
				paintProg(p, x.kids, s)
			}
		}
	}
}

func paintModel(prog []pp, s int) []ui.PaintOp {
	let mut out = []ui.PaintOp{}
	for op in prog {
		match op {
		case ppFill as x:
			out = out.Append(ui.FillOp{Path: tri(s, x.k), Color: tone(s, x.k)})
		case ppStroke as x:
			out = out.Append(ui.StrokeOp{Path: tri(s, x.k), Color: tone(s, x.k), Width: num(s, x.k, 14)})
		case ppRect as x:
			let rx = num(s, x.k, 0)
			let ry = num(s, x.k, 1)
			let rw = num(s, x.k, 2)
			let rh = num(s, x.k, 3)
			let corners = ui.Path{Segs: []ui.Seg{ui.MoveTo{X: rx, Y: ry}, ui.LineTo{X: rx + rw, Y: ry}, ui.LineTo{X: rx + rw, Y: ry + rh}, ui.LineTo{X: rx, Y: ry + rh}, ui.ClosePath}}
			out = out.Append(ui.FillOp{Path: corners, Color: tone(s, x.k)})
		case ppMove as x:
			out = out.Append(ui.PushTransform{M: ui.Affine{A: num(s, x.k, 0), B: 0.0, C: 0.0, D: num(s, x.k, 1), E: num(s, x.k, 2), F: num(s, x.k, 3)}})
			for o in paintModel(x.kids, s) {
				out = out.Append(o)
			}
			out = out.Append(ui.PopPaint)
		case ppClip as x:
			out = out.Append(ui.PushClip{X: num(s, x.k, 0), Y: num(s, x.k, 1), W: num(s, x.k, 2), H: num(s, x.k, 3)})
			for o in paintModel(x.kids, s) {
				out = out.Append(o)
			}
			out = out.Append(ui.PopPaint)
		}
	}
	out
}

func dialStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func dialTimer(s int) Option[Timeout[int]] { None }

func dialWork(s int) Option[func(send func(e int))] { None }

func dialMachine() Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: dialStep, After: dialTimer, Work: dialWork}
}

func canvasOf(shapes []ui.Shape[int]) ([]ui.PaintOp, bool) {
	for x in shapes {
		match x.Node {
		case ui.CanvasNode as c:
			return (c.Paint, true)
		case _:
		}
	}
	([]ui.PaintOp{}, false)
}

func lawPaintMatchesList(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		paintFollows(seed, steps, m)
	}
}

func paintFollows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let prog = genPaint(&r, 2)
	let view = func(f *ui.Frame[int], s int) {
		ui.Text(f, "at \(s)")
		ui.Pressable(f, "next", (s*5+3)%11) {
			ui.Text(f, "next")
		}
		ui.Canvas(f, "board", 120, 80) func(p *ui.Paint) {
			paintProg(p, prog, s)
		}
	}
	let a = actor.StartMachine(dialMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	for _ in 0..(steps%12+12)%12+1 {
		ui.Play(&h, []ui.Act{ui.TapOn{Label: "next"}}) catch {
		case _:
			return false
		}
		let s = ui.StateOf(&h)
		let (shown, ok) = canvasOf(ui.Screen(&h))
		let want = paintModel(prog, s)
		if !ok || shown != want || ui.Find(&h, "board") == None || strings.Count(ui.Screenshot(&h), "\n") != 4+len(want) {
			return false
		}
		let mut g = ui.NewFrame[int]()
		let _ = ui.Render(&g, view, s)
		if len(ui.Render(&g, view, s)) != 0 {
			return false
		}
	}
	true
}

type roster { items []int, next int }

func rosterStep(s roster, e int) Decision[roster, int] {
	if e == 0 {
		return Enter[roster]{Next: roster{items: s.items.Append(s.next), next: s.next + 1}}
	}
	if len(s.items) == 0 {
		return Enter[roster]{Next: s}
	}
	Enter[roster]{Next: roster{items: s.items[1:], next: s.next}}
}

func rosterTimer(s roster) Option[Timeout[int]] { None }

func rosterWork(s roster) Option[func(send func(e int))] { None }

func rosterMachine(n int) Machine[roster, int, int] {
	let mut items = []int{}
	for k in 0..n {
		items = items.Append(k)
	}
	Machine[roster, int, int]{Initial: roster{items: items, next: n}, Step: rosterStep, After: rosterTimer, Work: rosterWork}
}

func shownRows(count int, rh float64, off float64, height float64) []int {
	let mut out = []int{}
	for i in 0..count {
		let top = float64(i-ui.Overscan) * rh
		let bottom = float64(i+1+ui.Overscan) * rh
		if bottom > off && top < off+height {
			out = out.Append(i)
		}
	}
	out
}

func lawRowsAreTheCutEach(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		rowsFollow(seed, steps, m)
	}
}

func rowsFollow(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let sizes = []int{0, 3, 50, 1000}
	let n = sizes[draw(&r, len(sizes))]
	let rh = float64(draw(&r, 40) + 10)
	let view = func(f *ui.Frame[int], s roster) {
		ui.Pressable(f, "add", 0) {
			ui.Text(f, "+")
		}
		ui.Pressable(f, "drop", 1) {
			ui.Text(f, "-")
		}
		ui.Rows(f, "list", len(s.items), ui.Length(rh), func(i int) ui.Key { ui.ByInt(s.items[i]) }) func(i int) {
			let fl = ui.NewFlag(f, false)
			ui.Toggle(f, fl, "row \(s.items[i])")
		}
	}
	let a = actor.StartMachine(rosterMachine(n), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut model = ui.StateOf(&h)
	let mut off = 0.0
	let mut height = 0.0
	let mut on = Map[int, bool]{}
	for _ in 0..(steps%24+24)%24+1 {
		let shown = shownRows(len(model.items), rh, off, height)
		let mut act ui.Act = ui.SettleAll
		match draw(&r, 4) {
		case 0:
			off = float64(draw(&r, len(model.items)*int(rh)+1))
			height = float64(draw(&r, 300))
			act = ui.ScrollOn{Label: "list", Offset: ui.Length(off), Height: ui.Length(height)}
		case 1:
			act = ui.TapOn{Label: "add"}
			model = machines.Next(model, rosterStep(model, 0))
		case 2:
			act = ui.TapOn{Label: "drop"}
			model = machines.Next(model, rosterStep(model, 1))
		case _:
			if len(shown) > 0 {
				let k = model.items[shown[draw(&r, len(shown))]]
				act = ui.TapOn{Label: "row \(k)"}
				on[k] = !(on.Get(k) == (Some{Value: true}))
			}
		}
		ui.Play(&h, []ui.Act{act}) catch {
		case _:
			return false
		}
		let now = shownRows(len(model.items), rh, off, height)
		let mut kept = Map[int, bool]{}
		let mut rows = []ui.Shape[int]{}
		for i in now {
			let k = model.items[i]
			let lit = on.Get(k) == (Some{Value: true})
			if lit {
				kept[k] = true
			}
			rows = rows.Append(ui.Shape[int]{Node: ui.ToggleNode{Label: "row \(k)", On: lit}, Kids: []ui.Shape[int]{}})
		}
		on = kept
		let mut first = len(model.items)
		if len(now) > 0 {
			first = now[0]
		}
		let want = ui.Shape[int]{Node: ui.RowsNode{Label: "list", Count: len(model.items), RowHeight: ui.Length(rh), First: first}, Kids: rows}
		let screen = ui.Screen(&h)
		if ui.StateOf(&h) != model || len(screen) != 3 || !ui.SameShapes(screen[2:], []ui.Shape[int]{want}) {
			return false
		}
		if ui.RowRuns(&h) != len(now) || ui.RowRuns(&h) > int(height/rh)+2+2*ui.Overscan {
			return false
		}
	}
	true
}

let places = []string{"/", "/a", "/b?x=1", "/c#top", "/a/b?q=2#f"}

func placeOf(u ui.URL) Option[int] {
	for i, p in places {
		if ui.Href(u) == p {
			return Some{Value: i}
		}
	}
	None
}

func routeStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func routeTimer(s int) Option[Timeout[int]] { None }

func routeWork(s int) Option[func(send func(e int))] { None }

func routeMachine() Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: routeStep, After: routeTimer, Work: routeWork}
}

func home(u ui.URL) Option[int] { Some{Value: 0} }

func routeView(f *ui.Frame[int], s int) {
	ui.OnURL(f, placeOf)
	ui.OnURL(f, home)
	ui.Text(f, "at \(places[s])")
}

func lawHrefParses(seed int) bool {
	let mut r = seeded(seed)
	let parts = []string{"", "a", "b/c", "x=1", "q=2&y", "top"}
	let u = ui.URL{Path: "/" + parts[draw(&r, len(parts))], Query: parts[draw(&r, len(parts))], Fragment: parts[draw(&r, len(parts))]}
	ui.ParseURL(ui.Href(u)) == u
}

func lawNavigationFollowsModel(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		navigationFollows(seed, steps, m)
	}
}

func navigationFollows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let a = actor.StartMachine(routeMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, routeView, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut stack = []string{"/"}
	let mut shown = 0
	for _ in 0..(steps%16+16)%16+1 {
		let mut act ui.Act = ui.GoBack
		if draw(&r, 3) != 0 {
			let mut href = "/nowhere"
			if draw(&r, 4) != 0 {
				href = places[draw(&r, len(places))]
			}
			act = ui.NavigateTo{Href: href}
			stack = stack.Append(href)
		} else if len(stack) > 1 {
			let mut kept = []string{}
			for i in 0..len(stack)-1 {
				kept = kept.Append(stack[i])
			}
			stack = kept
		} else {
			act = ui.GoBack
		}
		ui.Play(&h, []ui.Act{act}) catch {
		case _:
			return false
		}
		let top = stack[len(stack)-1]
		match placeOf(ui.ParseURL(top)) {
		case Some[int] as p:
			shown = p.Value
		case None:
			shown = 0
		}
		if ui.Href(ui.Current(ui.History(&h))) != top || ui.Depth(ui.History(&h)) != len(stack)-1 || ui.StateOf(&h) != shown {
			return false
		}
		ui.Play(&h, []ui.Act{ui.ExpectOn{At: ui.WithText{Text: "at \(places[shown])"}, Is: ui.ToExist}}) catch {
		case _:
			return false
		}
	}
	true
}

test UiScriptShowsTheView(seed int, steps int) {
	assert lawScriptShowsTheView(seed, steps)
}

test UiCancelledFetchStops(steps int) {
	assert lawCancelledFetchStops(steps)
}

test UiNoTickWhenSettled(seed int, steps int) {
	assert lawNoTickWhenSettled(seed, steps)
}

test UiInputGivesSignal(seed int, steps int) {
	assert lawInputGivesSignal(seed, steps)
}

test UiPaintMatchesList(seed int, steps int) {
	assert lawPaintMatchesList(seed, steps)
}

test UiRowsAreTheCutEach(seed int, steps int) {
	assert lawRowsAreTheCutEach(seed, steps)
}

test UiHrefParses(seed int) {
	assert lawHrefParses(seed)
}

test UiNavigationFollowsModel(seed int, steps int) {
	assert lawNavigationFollowsModel(seed, steps)
}


test UiLocationPushThenBackRestores(path string) {
	let h = ui.NewMemoryHistory(ui.URL{Path: "/", Query: "", Fragment: ""})
	let u = ui.URL{Path: "/" + path, Query: "", Fragment: ""}
	let before = h.Current()
	h.Push(u)
	let there = h.Current()
	assert h.Back() && there == u && h.Current() == before
}

test UiLocationReplaceKeepsDepth(a string, b string) {
	let h = ui.NewMemoryHistory(ui.URL{Path: "/", Query: "", Fragment: ""})
	let u = ui.URL{Path: "/" + a, Query: "", Fragment: ""}
	let v = ui.URL{Path: "/" + b, Query: "q", Fragment: ""}
	let before = h.Current()
	h.Push(u)
	h.Replace(v)
	let there = h.Current()
	assert h.Back() && there == v && h.Current() == before
}

test UiLocationBackStopsAtTheStart(n int, path string) {
	let h = ui.NewMemoryHistory(ui.URL{Path: "/", Query: "", Fragment: ""})
	for i in 0..(n%4+4)%4 {
		h.Push(ui.URL{Path: "/\(i)", Query: "", Fragment: ""})
	}
	h.Replace(ui.URL{Path: "/" + path, Query: "", Fragment: ""})
	for h.Back() {
	}
	let first = h.Current()
	assert !h.Back() && h.Current() == first
}
`

// stdUIHostStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUIHostStatements = map[string]string{
	"ui ScriptShowsTheView":            `ScriptShowsTheView(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { scriptShows(seed, steps, m) } }`,
	"ui CancelledFetchStops":           `CancelledFetchStops(steps int) { let m = time.NewManual(0) with context { Scheduler: m } { cancelStops(steps, m) } }`,
	"ui NoTickWhenSettled":             `NoTickWhenSettled(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { ticksFollow(seed, steps, m) } }`,
	"ui InputGivesSignal":              `InputGivesSignal(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { inputFollows(seed, steps, m) } }`,
	"ui PaintMatchesList":              `PaintMatchesList(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { paintFollows(seed, steps, m) } }`,
	"ui RowsAreTheCutEach":             `RowsAreTheCutEach(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { rowsFollow(seed, steps, m) } }`,
	"ui HrefParses":                    `HrefParses(seed int) { let mut r = seeded(seed) let parts = []string{"", "a", "b/c", "x=1", "q=2&y", "top"} let u = URL{Path: "/" + parts[draw(&r, len(parts))], Query: parts[draw(&r, len(parts))], Fragment: parts[draw(&r, len(parts))]} ParseURL(Href(u)) == u }`,
	"ui NavigationFollowsModel":        `NavigationFollowsModel(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { navigationFollows(seed, steps, m) } }`,
	"ui Location.PushThenBackRestores": `PushThenBackRestores(u URL) { let before = Current() Push(u) let there = Current() Back() && there == u && Current() == before }`,
	"ui Location.ReplaceKeepsDepth":    `ReplaceKeepsDepth(u URL, v URL) { let before = Current() Push(u) Replace(v) let there = Current() Back() && there == v && Current() == before }`,
	"ui Location.BackStopsAtTheStart":  `BackStopsAtTheStart(u URL) { Replace(u) for Back() { } let first = Current() !Back() && Current() == first }`,
}

// TestUIHostModel runs the probe's tests with --seed 1; lawledger.Run holds that
// the probe restates exactly the ledger's ui laws of this runner, each
// with the row's statement, and that each passed. The mutants of
// internal/sketch/std/ui in testdata/mutants.json are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestUIHostModel(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stduihost", stdUIHostProbe, stdUIHostStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
