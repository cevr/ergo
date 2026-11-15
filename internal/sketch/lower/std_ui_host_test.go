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
import hex
import json
import machines
import queue
import scope
import strings
import sync
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

func keyedLines(f *ui.Frame[int], xs []int) {
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
	let base = ui.Access{Role: ui.Generic, Name: "", Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	match n {
	case ui.PressableNode[int] as x:
		ui.Access{Role: ui.Button, Name: x.Label, Disabled: off, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.CheckNode[int] as x:
		ui.Access{Role: ui.Checkbox, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.Checked}, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.ToggleNode as x:
		ui.Access{Role: ui.Switch, Name: x.Label, Disabled: off, Description: desc, Checked: Some{Value: x.On}, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.InputNode as x:
		ui.Access{Role: ui.TextBox, Name: x.Placeholder, Disabled: off, Description: desc, Checked: None, Value: Some{Value: x.Value}, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.TextNode as x:
		ui.Access{Role: ui.Static, Name: x.Value, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.FieldsetNode:
		ui.Access{Role: ui.Group, Name: "", Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.AreaNode as x:
		ui.Access{Role: ui.Region, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.CanvasNode as x:
		ui.Access{Role: ui.Image, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
	case ui.RowsNode as x:
		ui.Access{Role: ui.List, Name: x.Label, Disabled: false, Description: desc, Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: None}
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
		let byRole = draw(&r, 2) == 0
		if label == "del" && byRole {
			tap = ui.ClickOn{At: ui.First{At: ui.WithRole{Role: ui.Button, Name: label}}}
		} else if label == "del" {
			tap = ui.ClickOn{At: ui.First{At: ui.WithLabel{Label: label}}}
		} else if byRole {
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

func lawAdvanceTakesThePosted(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		advanceTakes(n, m)
	}
}

func advanceTakes(n int, m time.Manual) bool needs Scheduler {
	let frames = (n%8+8)%8 + 3
	let a = actor.StartMachine(spinMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, spinView, m, 2) catch {
	case ui.BadPatch:
		return false
	}
	a.Send(frames + 1) catch {
	case actor.Stopped:
		return false
	}
	ui.Settle(&h) catch {
	case _:
		return false
	}
	for _ in 0..frames {
		ui.Advance(&h, 16ms) catch {
		case _:
			return false
		}
	}
	ui.Posted(&h) <= 1 && ui.Frames(&h) == frames
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
			let ev = ui.PointerEvent{Phase: phase, X: x, Y: y, Zone: None}
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
		let want = ui.Shape[int]{Node: ui.RowsNode{Label: "list", Count: len(model.items), RowHeight: ui.Length(rh), First: first, Offset: ui.Length(off)}, Kids: rows}
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

type capA { tagA() int }

type capB { tagB() int }

type capC { tagC() int }

type aIs { v int }

type bIs { v int }

type cIs { v int }

func tagA(p aIs) int { p.v }

func tagB(p bIs) int { p.v }

func tagC(p cIs) int { p.v }

type boundLoad { set int, inner bool }

func fetchNone() int { 0 }

func fetchA() int needs capA { capA.tagA() }

func fetchB() int needs capB { 10 * capB.tagB() }

func fetchAB() int needs capA | capB { capA.tagA() + 10*capB.tagB() }

func fetchC() int needs capC { 100 * capC.tagC() }

func fetchAC() int needs capA | capC { capA.tagA() + 100*capC.tagC() }

func fetchBC() int needs capB | capC { 10*capB.tagB() + 100*capC.tagC() }

func fetchABC() int needs capA | capB | capC { capA.tagA() + 10*capB.tagB() + 100*capC.tagC() }

func loadFor(f *ui.Frame[int], i int, set int) needs capA | capB | capC {
	let base = 1000 * (i + 1)
	match set {
	case 1:
		ui.Load(f, ui.ByInt(i), func() int needs capA { base + fetchA() })
	case 2:
		ui.Load(f, ui.ByInt(i), func() int needs capB { base + fetchB() })
	case 3:
		ui.Load(f, ui.ByInt(i), func() int needs capA | capB { base + fetchAB() })
	case 4:
		ui.Load(f, ui.ByInt(i), func() int needs capC { base + fetchC() })
	case 5:
		ui.Load(f, ui.ByInt(i), func() int needs capA | capC { base + fetchAC() })
	case 6:
		ui.Load(f, ui.ByInt(i), func() int needs capB | capC { base + fetchBC() })
	case 7:
		ui.Load(f, ui.ByInt(i), func() int needs capA | capB | capC { base + fetchABC() })
	case _:
		ui.Load(f, ui.ByInt(i), func() int { base + fetchNone() })
	}
}

func boundView(f *ui.Frame[int], plan []boundLoad, outer []int) needs capA | capB | capC {
	for i, l in plan {
		ui.Box(f, ui.Plain()) {
			if l.inner {
				with context { capA: aIs{v: 3 - outer[0]}, capB: bIs{v: 3 - outer[1]}, capC: cIs{v: 3 - outer[2]} } {
					loadFor(f, i, l.set)
				}
			} else {
				loadFor(f, i, l.set)
			}
		}
	}
	ui.Text(f, "loads")
}

func logStep(s []int, e int) Decision[[]int, int] { Enter[[]int]{Next: s.Append(e)} }

func logTimer(s []int) Option[Timeout[int]] { None }

func logWork(s []int) Option[func(send func(e int))] { None }

func logMachine() Machine[[]int, int, int] {
	Machine[[]int, int, int]{Initial: []int{}, Step: logStep, After: logTimer, Work: logWork}
}

func lawLoadUsesTheBoundService(seed int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		loadsBound(seed, m)
	}
}

func loadsBound(seed int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let mut plan = []boundLoad{}
	for _ in 0..draw(&r, 3)+1 {
		plan = plan.Append(boundLoad{set: draw(&r, 8), inner: draw(&r, 2) == 0})
	}
	let outer = []int{draw(&r, 2) + 1, draw(&r, 2) + 1, draw(&r, 2) + 1}
	let mut want = []int{}
	for i, l in plan {
		let mut code = 1000 * (i + 1)
		for c in 0..3 {
			if l.set/[]int{1, 2, 4}[c]%2 == 1 {
				let mut tag = outer[c]
				if l.inner {
					tag = 3 - outer[c]
				}
				code = code + tag*[]int{1, 10, 100}[c]
			}
		}
		want = want.Append(code)
	}
	with context { capA: aIs{v: outer[0]}, capB: bIs{v: outer[1]}, capC: cIs{v: outer[2]} } {
		let a = actor.StartMachine(logMachine(), 4, func(o int) {})
		let mut h = ui.NewTestHost(a, func(f *ui.Frame[int], s []int) needs capA | capB | capC { boundView(f, plan, outer) }, m, 8) catch {
		case ui.BadPatch:
			return false
		}
		ui.Play(&h, []ui.Act{ui.SettleAll}) catch {
		case _:
			return false
		}
		let got = ui.StateOf(&h)
		if len(got) != len(plan) {
			return false
		}
		for g in got {
			let mut seen = false
			for w in want {
				seen = seen || w == g
			}
			if !seen {
				return false
			}
		}
	}
	true
}

func cursorStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func cursorTimer(s int) Option[Timeout[int]] { None }

func cursorWork(s int) Option[func(send func(e int))] { None }

func cursorMachine() Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: cursorStep, After: cursorTimer, Work: cursorWork}
}

func keptOffset(k int, rh float64, off float64, height float64) float64 {
	let top = float64(k) * rh
	let bottom = top + rh
	if height >= rh && top >= off && bottom <= off+height {
		return off
	}
	if height < rh {
		return top
	}
	let up = top
	let down = bottom - height
	if (off-up)*(off-up) <= (down-off)*(down-off) {
		return up
	}
	down
}

func lawRowsKeepTheCursor(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		cursorKept(seed, steps, m)
	}
}

func cursorKept(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let sizes = []int{0, 3, 50, 1000}
	let n = sizes[draw(&r, len(sizes))]
	let rh = float64(draw(&r, 40) + 10)
	let view = func(f *ui.Frame[int], s int) {
		ui.OnKey(f, func(k ui.KeyPress) Option[int] {
			for i in 0..n+1 {
				if k.Name == "\(i)" {
					return Some{Value: i}
				}
			}
			None
		})
		ui.RowsKeeping(f, "list", n, ui.Length(rh), s, func(i int) ui.Key { ui.ByInt(i) }) func(i int) {
			ui.Text(f, "row \(i)")
		}
	}
	let a = actor.StartMachine(cursorMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut off = 0.0
	let mut height = 0.0
	for _ in 0..(steps%24+24)%24+1 {
		let mut act ui.Act = ui.SettleAll
		if draw(&r, 2) == 0 {
			off = float64(draw(&r, n*int(rh)+1))
			height = float64(draw(&r, 300))
			act = ui.ScrollOn{Label: "list", Offset: ui.Length(off), Height: ui.Length(height)}
		} else {
			let k = draw(&r, n+1)
			act = ui.KeyIn{Press: ui.KeyPress{Name: "\(k)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}
			if k != ui.StateOf(&h) && k < n {
				off = keptOffset(k, rh, off, height)
			}
		}
		ui.Play(&h, []ui.Act{act}) catch {
		case _:
			return false
		}
		let now = shownRows(n, rh, off, height)
		let mut rows = []ui.Shape[int]{}
		for i in now {
			rows = rows.Append(leaf(ui.TextNode{Value: "row \(i)", Style: ui.Plain()}))
		}
		let mut first = n
		if len(now) > 0 {
			first = now[0]
		}
		let want = ui.Shape[int]{Node: ui.RowsNode{Label: "list", Count: n, RowHeight: ui.Length(rh), First: first, Offset: ui.Length(off)}, Kids: rows}
		if !ui.SameShapes(ui.Screen(&h), []ui.Shape[int]{want}) {
			return false
		}
	}
	true
}

func escapedForm(e string) bool {
	let mut i = 0
	for i < len(e) {
		let c = e[i]
		if c == '%' {
			if i+2 >= len(e) || !strings.Contains("0123456789ABCDEF", e[i+1:i+2]) || !strings.Contains("0123456789ABCDEF", e[i+2:i+3]) {
				return false
			}
			i = i + 3
		} else if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			i = i + 1
		} else {
			return false
		}
	}
	true
}

func lawEscapeRoundTrips(s string, t string) bool {
	let e = ui.Escape(s)
	let mut lower = ""
	for i in 0..len(s) {
		lower = lower + "%" + hex.Encode(s[i:i+1])
	}
	let plain = strings.ReplaceAll(t, "%", "")
	escapedForm(e) && ui.Unescape(e) == s && ui.Unescape(lower) == s && ui.Unescape(plain) == plain && ui.Unescape(s+"%4g"+plain) == ui.Unescape(s)+"%4g"+plain
}

func lawSegmentsReadPathOf(xs []string) bool {
	let path = ui.PathOf(xs)
	let mut want = []string{}
	for x in xs {
		if x != "" {
			want = want.Append(x)
		}
	}
	let got = ui.Segments(ui.URL{Path: path, Query: "", Fragment: ""})
	if len(got) != len(want) || ui.ParseURL(path).Path != path {
		return false
	}
	for i, x in want {
		if got[i] != x {
			return false
		}
	}
	true
}

type queryRecord {
	Name  string,
	Sort  string "sort by&",
	Page  int,
	Big   uint64,
	Ratio float64,
	On    bool,
	Note  json.Optional[string],
	Tags  []string,
}

type queryNested { Name string, Inner queryRecord }

func lawQueryRoundTrips(seed int, a string, b string, tags []string) bool {
	let mut r = seeded(seed)
	let mut note json.Optional[string] = json.Absent
	if draw(&r, 2) == 0 {
		note = json.Present{Value: b + a}
	}
	let mut ts = tags
	if len(ts) == 0 {
		ts = []string{a}
	}
	let v = queryRecord{Name: a, Sort: b, Page: draw(&r, 2001) - 1000, Big: uint64(draw(&r, 1000000)) * 18446744073709, Ratio: float64(draw(&r, 2001)-1000) / 8.0, On: draw(&r, 2) == 0, Note: note, Tags: ts}
	let u = ui.WithQuery(ui.URL{Path: "/list", Query: "", Fragment: "top"}, v) catch {
	case ui.QueryShape:
		return false
	}
	let back = ui.QueryOf[queryRecord](ui.ParseURL(ui.Href(u))) catch {
	case _:
		return false
	}
	let nested = ui.EncodeQuery(queryNested{Name: a, Inner: v}) catch {
	case ui.QueryShape as q:
		q.Key
	}
	let scalar = ui.EncodeQuery(draw(&r, 9)) catch {
	case ui.QueryShape as q:
		"none" + q.Key
	}
	back == v && u.Path == "/list" && u.Fragment == "top" && nested == "inner" && scalar == "none"
}

func replyWork(gate queue.Queue[int], last int) func(s int) Option[func(send func(e int))] {
	func(s int) Option[func(send func(e int))] {
		if s >= last {
			return None
		}
		Some{Value: func(send func(e int)) {
			let _ = gate.Pop() catch {
			case queue.Closed:
				return
			}
			send(s + 1)
		}}
	}
}

func replyStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func replyTimer(s int) Option[Timeout[int]] { None }

func replyMachine(gate queue.Queue[int], last int) Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: replyStep, After: replyTimer, Work: replyWork(gate, last)}
}

func replyView(f *ui.Frame[int], s int) { ui.Text(f, "at \(s)") }

func lawWorkReplyIsShown(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		replyShown(n, m)
	}
}

func replyShown(n int, m time.Manual) bool needs Scheduler {
	let last = (n%3+3)%3 + 1
	let gate = queue.New[int](last)
	let a = actor.StartMachine(replyMachine(gate, last), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, replyView, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	if !ui.SameShapes(ui.Screen(&h), []ui.Shape[int]{leaf(ui.TextNode{Value: "at 0", Style: ui.Plain()})}) {
		return false
	}
	for i in 0..last {
		gate.Push(i) catch {
		case queue.Closed:
			return false
		}
		let _ = actor.WaitFor(a, func(s int) bool { s == i+1 }) catch {
		case actor.Stopped:
			return false
		}
		ui.Settle(&h) catch {
		case _:
			return false
		}
		if !ui.SameShapes(ui.Screen(&h), []ui.Shape[int]{leaf(ui.TextNode{Value: "at \(i + 1)", Style: ui.Plain()})}) {
			return false
		}
	}
	true
}

func lawWatcherHoldsOneState(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		watcherHolds(n, m)
	}
}

func watcherHolds(n int, m time.Manual) bool needs Scheduler {
	let commits = (n%8+8)%8 + 3
	let gate = queue.New[int](1)
	let a = actor.StartMachine(spinMachine(), 1, func(o int) {})
	let (s, _) = ui.Start(a, spinView, func(ev ui.Event[int]) {
		let _ = gate.Pop() catch {
		case queue.Closed:
			return
		}
	})
	let _ = s
	// The watcher has subscribed and is blocked in its first post.
	m.Advance(0ns)
	for i in 0..commits {
		a.Send(i) catch {
		case actor.Stopped:
			return false
		}
	}
	m.Advance(0ns)
	let held = a.Held()
	gate.End()
	a.Stop()
	held <= 1
}

func lawWatcherShowsWorkReplies(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		watchReplies(n)
	}
}

func watchReplies(n int) bool needs Scheduler {
	let last = (n%3+3)%3 + 1
	let gate = queue.New[int](last)
	let posted = queue.New[ui.Event[int]](8)
	let a = actor.StartMachine(replyMachine(gate, last), 1, func(o int) {})
	let (s, first) = ui.Start(a, replyView, func(ev ui.Event[int]) {
		posted.Push(ev) catch {
		case queue.Closed:
		}
	})
	let mut session = s
	if !writesText(first, "at 0") {
		return false
	}
	for i in 1..last+1 {
		gate.Push(i) catch {
		case queue.Closed:
			return false
		}
		let mut shown = false
		for !shown {
			let ev = posted.Pop() catch {
			case queue.Closed:
				return false
			}
			match ev {
			case ui.Committed:
			case _:
				return false
			}
			let ops = ui.Step(&session, ev) catch {
			case actor.Stopped:
				return false
			}
			shown = writesText(ops, "at \(i)")
		}
	}
	true
}

func writesText(ops []ui.Op[int], v string) bool {
	for op in ops {
		let mut node Option[ui.Node[int]] = None
		match op {
		case ui.Insert[int] as x:
			node = Some{Value: x.Node}
		case ui.Set[int] as x:
			node = Some{Value: x.Node}
		case _:
		}
		match node {
		case Some[ui.Node[int]] as x:
			match x.Value {
			case ui.TextNode as t:
				if t.Value == v {
					return true
				}
			case _:
			}
		case None:
		}
	}
	false
}

type Boom { N int }

func crashStep(s int, e int) Decision[int, int] fails Boom {
	if e == 1 {
		Unreachable("crash app: a defect, on purpose")
	}
	if e == 2 {
		return Boom{N: s}
	}
	if e == 0 {
		return Enter[int]{Next: s + 1}
	}
	Enter[int]{Next: s}
}

func crashTimer(s int) Option[Timeout[int]] { None }

func crashWork(s int) Option[func(send func(e int))] { None }

func crashMachine() Machine[int, int, int, Boom] {
	Machine[int, int, int, Boom]{Initial: 0, Step: crashStep, After: crashTimer, Work: crashWork}
}

func crashAppView(f *ui.Frame[int], s int) {
	ui.Text(f, "count \(s)")
	ui.Pressable(f, "add", 0) {
		ui.Text(f, "+")
	}
	ui.Pressable(f, "defect", 1) {
		ui.Text(f, "!")
	}
	ui.Pressable(f, "fail", 2) {
		ui.Text(f, "x")
	}
	let fl = ui.NewFlag(f, false)
	ui.Toggle(f, fl, "keep")
}

func crashText(c ui.Crash[int, int, Boom]) string {
	match c.Cause {
	case ui.DefectCrash:
		"crashed: defect"
	case ui.FailureCrash[Boom] as x:
		match x.Failure {
		case Boom as b:
			"crashed: failure \(b.N)"
		}
	}
}

func crashOwnView(f *ui.Frame[int], c ui.Crash[int, int, Boom]) {
	ui.Text(f, crashText(c))
	ui.Pressable(f, "reload", 3) {
		ui.Text(f, "reload")
	}
}

func pressShape(label string, on int, text string) ui.Shape[int] {
	ui.Shape[int]{Node: ui.PressableNode{Label: label, On: on, Style: ui.Plain()}, Kids: []ui.Shape[int]{leaf(ui.TextNode{Value: text, Style: ui.Plain()})}}
}

func crashAppShapes(n int, on bool) []ui.Shape[int] {
	[]ui.Shape[int]{leaf(ui.TextNode{Value: "count \(n)", Style: ui.Plain()}), pressShape("add", 0, "+"), pressShape("defect", 1, "!"), pressShape("fail", 2, "x"), leaf(ui.ToggleNode{Label: "keep", On: on})}
}

func lawCrashShowsTheCrashView(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		crashShown(seed, steps, m)
	}
}

func crashShown(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let reports = sync.NewRef(0)
	let crashes = ui.Crashes[int, int, Boom]{View: crashOwnView, Report: func(c ui.Crash[int, int, Boom]) {
		reports.Update(func(n int) int { n + 1 })
	}}
	let mut h = ui.NewAppTestHost(crashMachine(), 1, crashAppView, crashes, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut count = 0
	let mut on = false
	let mut crash Option[ui.Crash[int, int, Boom]] = None
	let mut crashed = 0
	for _ in 0..(steps%24+24)%24+1 {
		let pick = draw(&r, 4)
		let mut label = "reload"
		match crash {
		case Some[ui.Crash[int, int, Boom]]:
			crash = None
			count = 0
			on = false
		case None:
			if pick == 0 {
				label = "add"
				count = count + 1
			} else if pick == 1 {
				label = "defect"
				crash = Some{Value: ui.Crash[int, int, Boom]{Cause: ui.DefectCrash{Reason: "crash app: a defect, on purpose"}, State: count, Signal: Some{Value: 1}}}
				crashed = crashed + 1
			} else if pick == 2 {
				label = "fail"
				crash = Some{Value: ui.Crash[int, int, Boom]{Cause: ui.FailureCrash[Boom]{Failure: Boom{N: count}}, State: count, Signal: Some{Value: 2}}}
				crashed = crashed + 1
			} else {
				label = "keep"
				on = !on
			}
		}
		ui.Play(&h, []ui.Act{ui.TapOn{Label: label}}) catch {
		case _:
			return false
		}
		let want = match crash {
		case Some[ui.Crash[int, int, Boom]] as c:
			[]ui.Shape[int]{leaf(ui.TextNode{Value: crashText(c.Value), Style: ui.Plain()}), pressShape("reload", 3, "reload")}
		case None:
			crashAppShapes(count, on)
		}
		if !ui.SameShapes(ui.Screen(&h), want) || reports.Get() != crashed || !sameCrash(ui.CrashOf(&h), crash) {
			return false
		}
	}
	true
}

func lawPlainCrashSaysWhatItKnows(reason string, e int) bool {
	let mut f = ui.NewFrame[int]()
	let sent = ui.Render(&f, ui.PlainCrash[int, int, Boom], ui.Crash[int, int, Boom]{Cause: ui.DefectCrash{Reason: "at \(e): " + reason}, State: 0, Signal: Some{Value: e}})
	let mut g = ui.NewFrame[int]()
	let none = ui.Render(&g, ui.PlainCrash[int, int, Boom], ui.Crash[int, int, Boom]{Cause: ui.FailureCrash[Boom]{Failure: Boom{N: e}}, State: 0, Signal: None})
	writesText(sent, "The program stopped: a defect ended it: at \(e): \(reason)") && writesText(sent, "signal: the last one the window sent (or a timer or work around it)") && writesText(none, "signal: none (timer or work)")
}

func sameCrash(got Option[ui.Crash[int, int, Boom]], want Option[ui.Crash[int, int, Boom]]) bool {
	match (got, want) {
	case (Some[ui.Crash[int, int, Boom]] as g, Some[ui.Crash[int, int, Boom]] as w):
		let same = g.Value.State == w.Value.State && g.Value.Signal == w.Value.Signal
		match (g.Value.Cause, w.Value.Cause) {
		case (ui.DefectCrash as x, ui.DefectCrash as y):
			same && strings.HasPrefix(x.Reason, "unreachable at ") && strings.HasSuffix(x.Reason, ": "+y.Reason)
		case (ui.FailureCrash[Boom] as x, ui.FailureCrash[Boom] as y):
			same && x.Failure == y.Failure
		case _:
			false
		}
	case (None, None):
		true
	case _:
		false
	}
}

func lawReloadIsInitial(seed int, defect bool) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		reloadInitial(seed, defect, m)
	}
}

func reloadInitial(seed int, defect bool, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let mut h = ui.NewAppTestHost(crashMachine(), 1, crashAppView, ui.Crashes[int, int, Boom]{View: crashOwnView, Report: func(c ui.Crash[int, int, Boom]) {}}, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut script = []ui.Act{}
	for _ in 0..draw(&r, 6) {
		script = script.Append(ui.TapOn{Label: if draw(&r, 2) == 0 { "add" } else { "keep" }})
	}
	script = script.Append(ui.TapOn{Label: if defect { "defect" } else { "fail" }})
	script = script.Append(ui.TapOn{Label: "reload"})
	ui.Play(&h, script) catch {
	case _:
		return false
	}
	let mut fresh = ui.NewAppTestHost(crashMachine(), 1, crashAppView, ui.Crashes[int, int, Boom]{View: crashOwnView, Report: func(c ui.Crash[int, int, Boom]) {}}, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	ui.SameShapes(ui.Screen(&h), ui.Screen(&fresh)) && ui.CrashOf(&h) == None
}

func workCrashStep(s int, e int) Decision[int, int] fails Boom {
	if e == 1 {
		Unreachable("crash app: a defect, on purpose")
	}
	if e == 5 {
		return Enter[int]{Next: -1}
	}
	Enter[int]{Next: s}
}

func workCrashWork(gate queue.Queue[int]) func(s int) Option[func(send func(e int))] {
	func(s int) Option[func(send func(e int))] {
		if s != -1 {
			return None
		}
		Some{Value: func(send func(e int)) {
			let _ = gate.Pop() catch {
			case queue.Closed:
				return
			}
			Unreachable("crash app: a defect of the work, on purpose")
		}}
	}
}

func keyView(f *ui.Frame[int], s int) {
	ui.OnKey(f, func(k ui.KeyPress) Option[int] {
		if k.Name == "1" {
			return Some{Value: 1}
		}
		if k.Name == "5" {
			return Some{Value: 5}
		}
		None
	})
	ui.Text(f, "at \(s)")
}

func keyCrashView(f *ui.Frame[int], c ui.Crash[int, int, Boom]) {
	ui.OnKey(f, func(k ui.KeyPress) Option[int] { Some{Value: 0} })
	ui.Text(f, "crashed")
}

func key(name string) ui.Event[int] {
	ui.KeyDown{Press: ui.KeyPress{Name: name, Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}
}

func endedOf(session *ui.Session[int, int, Decision[int, int], Boom], posted queue.Queue[ui.Event[int]], run int) Option[[]ui.Op[int]] {
	for {
		let ev = posted.Pop() catch {
		case queue.Closed:
			return None
		}
		let ops = ui.Step(session, ev) catch {
		case actor.Stopped:
			return None
		}
		match ev {
		case ui.Ended as x:
			if x.Run == run {
				return Some{Value: ops}
			}
		case _:
		}
	}
}

func lawSessionCrashFollowsTheEnd(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		crashFollows()
	}
}

func crashFollows() bool needs Scheduler {
	let gate = queue.New[int](1)
	let posted = queue.New[ui.Event[int]](64)
	let reports = sync.NewRef(0)
	let mach = Machine[int, int, int, Boom]{Initial: 0, Step: workCrashStep, After: replyTimer, Work: workCrashWork(gate)}
	let reported = sync.NewRef([]ui.Crash[int, int, Boom]{})
	let crashes = ui.Crashes[int, int, Boom]{View: keyCrashView, Report: func(c ui.Crash[int, int, Boom]) {
		reports.Update(func(k int) int { k + 1 })
		reported.Update(func(cs []ui.Crash[int, int, Boom]) []ui.Crash[int, int, Boom] { cs.Append(c) })
	}}
	let (s, first) = ui.StartApp(mach, 1, keyView, crashes, func(ev ui.Event[int]) {
		posted.Push(ev) catch {
		case queue.Closed:
		}
	})
	let mut session = s
	if !writesText(first, "at 0") {
		return false
	}
	// A defect of a step; its actor's ui.Ended comes before the reload.
	let a = ui.Step(&session, key("1")) catch {
	case actor.Stopped:
		return false
	}
	if !writesText(a, "crashed") || reports.Get() != 1 || !endsQuiet(&session, posted, 0) || reports.Get() != 1 {
		return false
	}
	let b = ui.Step(&session, key("r")) catch {
	case actor.Stopped:
		return false
	}
	// A defect of the second actor; its ui.Ended comes after the reload.
	let c = ui.Step(&session, key("1")) catch {
	case actor.Stopped:
		return false
	}
	let d = ui.Step(&session, key("r")) catch {
	case actor.Stopped:
		return false
	}
	if !writesText(b, "at 0") || !writesText(c, "crashed") || !writesText(d, "at 0") || !endsQuiet(&session, posted, 1) || reports.Get() != 2 {
		return false
	}
	// A defect of the third actor's work.
	let e = ui.Step(&session, key("5")) catch {
	case actor.Stopped:
		return false
	}
	if !writesText(e, "at -1") {
		return false
	}
	gate.Push(0) catch {
	case queue.Closed:
		return false
	}
	let shown = match endedOf(&session, posted, 2) {
	case Some[[]ui.Op[int]] as ops:
		writesText(ops.Value, "crashed") && reports.Get() == 3
	case None:
		false
	}
	let cs = reported.Get()
	let step = ui.Crash[int, int, Boom]{Cause: ui.DefectCrash{Reason: "crash app: a defect, on purpose"}, State: 0, Signal: Some{Value: 1}}
	let work = ui.Crash[int, int, Boom]{Cause: ui.DefectCrash{Reason: "crash app: a defect of the work, on purpose"}, State: -1, Signal: None}
	shown && len(cs) == 3 && sameCrash(Some{Value: cs[0]}, Some{Value: step}) && sameCrash(Some{Value: cs[1]}, Some{Value: step}) && sameCrash(Some{Value: cs[2]}, Some{Value: work})
}

func endsQuiet(session *ui.Session[int, int, Decision[int, int], Boom], posted queue.Queue[ui.Event[int]], run int) bool {
	match endedOf(session, posted, run) {
	case Some[[]ui.Op[int]] as ops:
		len(ops.Value) == 0
	case None:
		false
	}
}

type statsApp { texts int, paints int, rows int, stops bool, key int, canvas bool, rev bool }

type Halt {}

func haltIf(b bool) fails Halt {
	if b {
		return Halt
	}
}

func statsStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func statsMachine() Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: statsStep, After: replyTimer, Work: replyWork(queue.New[int](1), 0)}
}

func statsView(apps []statsApp) func(f *ui.Frame[int], s int) {
	func(f *ui.Frame[int], s int) {
		let st = apps[s]
		ui.OnKey(f, func(k ui.KeyPress) Option[int] {
			for i in 0..len(apps) {
				if k.Name == "\(i)" {
					return Some{Value: i}
				}
			}
			None
		})
		ui.Column(f, ui.Plain()) {
			for j in 0..st.texts {
				ui.Text(f, "t\(j)")
			}
			if st.canvas {
				ui.Canvas(f, "c", 40, 40) func(p *ui.Paint) {
					for j in 0..st.paints {
						ui.Fill(p, ui.RectPath(float64(j), 0.0, 1.0, 1.0), tone(j, j))
					}
				}
			}
			let order = if st.rev { []int{2, 1, 0} } else { []int{0, 1, 2} }
			ui.Each(f, order, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
				ui.Text(f, "e\(x)")
			}
			ui.Rows(f, "list", st.rows, 10, func(i int) ui.Key { ui.ByInt(i) }) func(i int) {
				ui.Text(f, "r\(i)")
			}
			ui.Boundary(f) {
				ui.Text(f, "in")
				try haltIf(st.stops)
			} catch {
			case Halt:
				ui.Text(f, "out")
			}
			let key = st.key
			ui.Load(f, ui.ByInt(key), func() int { s })
		}
	}
}

func opCounts(ops []ui.Op[int]) (int, int, int, int, int) {
	let mut ins = 0
	let mut mov = 0
	let mut rem = 0
	let mut set = 0
	let mut paint = 0
	for op in ops {
		let mut node Option[ui.Node[int]] = None
		match op {
		case ui.Insert[int] as x:
			ins = ins + 1
			node = Some{Value: x.Node}
		case ui.Move[int]:
			mov = mov + 1
		case ui.Remove[int]:
			rem = rem + 1
		case ui.Set[int] as x:
			set = set + 1
			node = Some{Value: x.Node}
		}
		match node {
		case Some[ui.Node[int]] as n:
			match n.Value {
			case ui.CanvasNode as c:
				paint = paint + len(c.Paint)
			case _:
			}
		case None:
		}
	}
	(ins, mov, rem, set, paint)
}

func lawStatsCountEveryPhase(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		statsFollow(seed, steps)
	}
}

func statsFollow(seed int, steps int) bool needs Scheduler {
	let mut r = seeded(seed)
	let mut apps = []statsApp{statsApp{texts: 0, paints: 0, rows: 0, stops: false, key: 0, canvas: false, rev: false}}
	let n = (steps%12+12)%12 + 1
	for _ in 0..n {
		apps = apps.Append(statsApp{texts: draw(&r, 5), paints: draw(&r, 4), rows: draw(&r, 3) * 10, stops: draw(&r, 2) == 0, key: draw(&r, 2), canvas: draw(&r, 3) != 0, rev: draw(&r, 2) == 0})
	}
	let a = actor.StartMachine(statsMachine(), 1, func(o int) {})
	let (s, _) = ui.Start(a, statsView(apps), func(ev ui.Event[int]) {})
	let mut session = s
	let mut prev = apps[0]
	for i in 1..len(apps) {
		let ops = ui.Step(&session, ui.KeyDown{Press: ui.KeyPress{Name: "\(i)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}) catch {
		case actor.Stopped:
			return false
		}
		let st = apps[i]
		let shown = len(shownRows(st.rows, 10.0, 0.0, 0.0))
		let stopped = if st.stops { 1 } else { 0 }
		let changed = if st.key != prev.key { 1 } else { 0 }
		let canvas = if st.canvas { 1 } else { 0 }
		let (ins, mov, rem, set, paint) = opCounts(ops)
		let want = ui.Stats{Signals: 1, Written: 6 + canvas + st.texts + shown + stopped, Rows: shown, Skipped: 0, Stopped: stopped, Committed: 6 + canvas + st.texts + shown, Inserted: ins, Moved: mov, Removed: rem, Set: set, Painted: paint, Fetches: changed, Cancels: changed}
		let got = ui.SessionStats(&session)
		if got != want || len(ui.StoppedAt(&session)) != stopped {
			return false
		}
		// The paint ops are the canvas's when it is new or its paint
		// changed; three rows move when the ui.Each's order flips.
		if st.canvas && (!prev.canvas || st.paints != prev.paints) && paint != st.paints {
			return false
		}
		if st.rev != prev.rev && mov == 0 {
			return false
		}
		prev = st
	}
	true
}

type slowLog { texts int, log []ui.Slow }

type slowSignal = MoreText{} | WasSlow{S ui.Slow}

func slowStep(s slowLog, e slowSignal) Decision[slowLog, int] {
	match e {
	case MoreText:
		Enter[slowLog]{Next: slowLog{texts: s.texts + 1, log: s.log}}
	case WasSlow as w:
		Enter[slowLog]{Next: slowLog{texts: s.texts, log: s.log.Append(w.S)}}
	}
}

func slowTimer(s slowLog) Option[Timeout[slowSignal]] { None }

func slowWork(s slowLog) Option[func(send func(e slowSignal))] { None }

func lawSlowAboveTheBudget(seed int, bound int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		slowFollow(seed, (bound%5+5)%5+1)
	}
}

func slowFollow(seed int, bound int) bool needs Scheduler {
	let mut r = seeded(seed)
	let view = func(f *ui.Frame[slowSignal], s slowLog) {
		// Every third pass asks for nothing: an ui.OnSlow holds for its pass.
		if s.texts%3 != 0 {
			ui.OnSlow(f, ui.Budget{Signals: 0, Written: bound, Committed: 0, Ops: 0, Painted: 0, Fetches: 0}, func(x ui.Slow) slowSignal { WasSlow{S: x} })
		}
		ui.OnKey(f, func(k ui.KeyPress) Option[slowSignal] { Some{Value: MoreText} })
		ui.Text(f, "log \(len(s.log))")
		for j in 0..s.texts {
			ui.Text(f, "t\(j)")
		}
	}
	let posted = queue.New[ui.Event[slowSignal]](256)
	let a = actor.StartMachine(Machine[slowLog, slowSignal, int]{Initial: slowLog{texts: 0, log: []ui.Slow{}}, Step: slowStep, After: slowTimer, Work: slowWork}, 1, func(o int) {})
	let (s, _) = ui.Start(a, view, func(ev ui.Event[slowSignal]) {
		posted.Push(ev) catch {
		case queue.Closed:
		}
	})
	let mut session = s
	let mut want = []ui.Slow{}
	for _ in 0..draw(&r, 8)+3 {
		let _ = ui.Step(&session, ui.KeyDown{Press: ui.KeyPress{Name: "k", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}}) catch {
		case actor.Stopped:
			return false
		}
		// The pass wrote the log's line and the texts.
		let texts = a.State().texts
		if texts%3 != 0 && texts+1 > bound {
			want = want.Append(ui.Slow{Counter: ui.WrittenCounter, Count: texts + 1, Bound: bound})
			// The pass that shows the ui.Slow, a ui.Committed's, is not judged:
			// the law takes the posted events until it shows it.
			// A ui.Slow is one commit, so it shows within a few posted
			// events; a loop that judges its own passes never settles.
			let mut shown = false
			let mut taken = 0
			for !shown {
				taken = taken + 1
				if taken > 16 {
					return false
				}
				let ev = posted.Pop() catch {
				case queue.Closed:
					return false
				}
				let ops = ui.Step(&session, ev) catch {
				case actor.Stopped:
					return false
				}
				for op in ops {
					match op {
					case ui.Set[slowSignal] as x:
						shown = shown || x.Node == ui.TextNode{Value: "log \(len(want))", Style: ui.Plain()}
					case _:
					}
				}
			}
		}
	}
	let log = a.State().log
	if len(log) != len(want) {
		return false
	}
	for i, x in want {
		if log[i] != x {
			return false
		}
	}
	true
}

func sameHit(a Option[ui.ZoneHit], b Option[ui.ZoneHit]) bool {
	match a {
	case Some[ui.ZoneHit] as x:
		match b {
		case Some[ui.ZoneHit] as y:
			if len(x.Value.Path) != len(y.Value.Path) || x.Value.Box != y.Value.Box || x.Value.X != y.Value.X || x.Value.Y != y.Value.Y {
				return false
			}
			for i, k in x.Value.Path {
				if k != y.Value.Path[i] {
					return false
				}
			}
			true
		case None:
			false
		}
	case None:
		match b {
		case Some[ui.ZoneHit]:
			false
		case None:
			true
		}
	}
}

func foundId[S, D, F](h *ui.TestHost[S, int, D, F], label string) ui.Id[int] {
	match ui.Find(h, label) {
	case Some[ui.Id[int]] as id:
		id.Value
	case None:
		Unreachable("ui laws: no node \(label)")
	}
}

func zoneStep(s int, e int) Decision[int, int] { Enter[int]{Next: e} }

func zoneTimer(s int) Option[Timeout[int]] { None }

func zoneWork(s int) Option[func(send func(e int))] { None }

func zoneMachine() Machine[int, int, int] {
	Machine[int, int, int]{Initial: 0, Step: zoneStep, After: zoneTimer, Work: zoneWork}
}

func lawZoneHitNamesItsPath(seed int, n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		zoneFollows(seed, n, m)
	}
}

func zoneFollows(seed int, n int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let ncols = draw(&r, 3) + 1
	let mut cards = []int{}
	for _ in 0..ncols {
		cards = cards.Append(draw(&r, 3) + 1)
	}
	let got = sync.NewRef[Option[ui.ZoneHit]](None)
	let view = func(f *ui.Frame[int], s int) {
		ui.Pressable(f, "drop", 1) {
			ui.Text(f, "drop")
		}
		ui.Area(f, "board", ui.Plain(), func(p ui.PointerEvent) Option[int] {
			got.Set(p.Zone)
			None
		}) {
			for c in 0..ncols {
				ui.Zone(f, ui.ByInt(c), ui.Plain()) {
					ui.Text(f, "col \(c)")
					// A pass at state 1 drops column 0's cards.
					let shown = if s == 1 && c == 0 { 0 } else { cards[c] }
					for k in 0..shown {
						ui.Box(f, ui.Plain()) {
							ui.Zone(f, ui.ByText("\(c)-\(k)"), ui.Plain()) {
								ui.Text(f, "card \(c)-\(k)")
							}
						}
					}
				}
			}
		}
		ui.Zone(f, ui.ByInt(99), ui.Plain()) {
			ui.Text(f, "outside")
		}
	}
	let a = actor.StartMachine(zoneMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let board = foundId(&h, "board")
	let gone = ui.Locate(&h, ui.WithZone{Key: ui.ByText("0-0")})[0]
	for i in 0..(n%16+16)%16+1 {
		if i == 1 {
			ui.Play(&h, []ui.Act{ui.TapOn{Label: "drop"}}) catch {
			case _:
				return false
			}
		}
		let box = ui.Rect{X: ui.Length(float64(draw(&r, 50))), Y: ui.Length(float64(draw(&r, 50))), W: 40, H: 20}
		// A host sends a ui.Zone of its own, which the composer replaces.
		let p = ui.PointerEvent{Phase: ui.Moved, X: float64(draw(&r, 100)), Y: float64(draw(&r, 100)), Zone: Some{Value: ui.ZoneHit{Path: []ui.Key{ui.ByInt(-2)}, Box: box, X: 0.0, Y: 0.0}}}
		let c = draw(&r, ncols)
		let k = draw(&r, cards[c])
		let hit = Some{Value: ui.ZoneHit{Path: []ui.Key{}, Box: box, X: p.X - float64(box.X), Y: p.Y - float64(box.Y)}}
		let mut want Option[ui.ZoneHit] = None
		let mut over Option[ui.Id[int]] = None
		match draw(&r, 6) {
		case 0:
			over = Some{Value: ui.Locate(&h, ui.WithZone{Key: ui.ByInt(c)})[0]}
			want = Some{Value: ui.ZoneHit{..hit.Value, Path: []ui.Key{ui.ByInt(c)}}}
		case 1:
			if c == 0 && ui.StateOf(&h) == 1 {
				over = Some{Value: gone}
			} else {
				over = Some{Value: ui.Locate(&h, ui.WithZone{Key: ui.ByText("\(c)-\(k)")})[0]}
				want = Some{Value: ui.ZoneHit{..hit.Value, Path: []ui.Key{ui.ByInt(c), ui.ByText("\(c)-\(k)")}}}
			}
		case 2:
			over = Some{Value: ui.Locate(&h, ui.WithZone{Key: ui.ByInt(99)})[0]}
		case 3:
			over = Some{Value: foundId(&h, "col \(c)")}
		case 4:
			// Card 0-0, which the drop at the second event removes.
			over = Some{Value: gone}
			if ui.StateOf(&h) != 1 {
				want = Some{Value: ui.ZoneHit{..hit.Value, Path: []ui.Key{ui.ByInt(0), ui.ByText("0-0")}}}
			}
		case _:
		}
		got.Set(Some{Value: ui.ZoneHit{Path: []ui.Key{ui.ByInt(-1)}, Box: box, X: 0.0, Y: 0.0}})
		match over {
		case Some[ui.Id[int]] as o:
			ui.PointOver(&h, board, o.Value, box, p) catch {
			case _:
				return false
			}
		case None:
			ui.PointAt(&h, board, p) catch {
			case _:
				return false
			}
		}
		if !sameHit(got.Get(), want) {
			return false
		}
	}
	true
}

func lawCaptureKeepsThePointer(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		captureFollows(seed, steps, m)
	}
}

func phaseCode(p ui.Phase) int {
	match p {
	case ui.Down:
		1
	case ui.Moved:
		2
	case ui.Up:
		3
	case ui.Wheel:
		4
	}
}

func captureFollows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let view = func(f *ui.Frame[int], s int) {
		ui.Pressable(f, "hide", 99) {
			ui.Text(f, "hide")
		}
		ui.Text(f, "top")
		if s != 99 {
			ui.Area(f, "a", ui.Plain(), func(p ui.PointerEvent) Option[int] { Some{Value: 10 + phaseCode(p.Phase)} }) {
				ui.Text(f, "in a")
			}
		}
		ui.Area(f, "b", ui.Plain(), func(p ui.PointerEvent) Option[int] { Some{Value: 20 + phaseCode(p.Phase)} }) {
			ui.Text(f, "in b")
		}
	}
	let a = actor.StartMachine(zoneMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	// The model: the ui.Area holding the pointer (0 none, 10 a, 20 b) and the
	// actor's state.
	let mut held = 0
	let mut state = 0
	for _ in 0..(steps%24+24)%24+1 {
		let shownA = state != 99
		if draw(&r, 8) == 0 {
			ui.Play(&h, []ui.Act{ui.TapOn{Label: "hide"}}) catch {
			case _:
				return false
			}
			state = 99
			if held == 10 {
				held = 0
			}
		} else {
			let phases = []ui.Phase{ui.Down, ui.Moved, ui.Up, ui.Wheel{DX: 0.0, DY: 1.0}}
			let phase = phases[draw(&r, 4)]
			// The node under the pointer: a (when shown), b or the ui.Text.
			let mut at = []int{10, 20, 0}[draw(&r, 3)]
			if at == 10 && !shownA {
				at = 0
			}
			let label = if at == 10 { "a" } else if at == 20 { "b" } else { "top" }
			let id = foundId(&h, label)
			ui.PointAt(&h, id, ui.PointerEvent{Phase: phase, X: 1.0, Y: 1.0, Zone: None}) catch {
			case _:
				return false
			}
			let to = if held != 0 { held } else { at }
			match phase {
			case ui.Down:
				if to != 0 {
					held = to
				}
			case ui.Up:
				held = 0
			case _:
			}
			if to != 0 {
				state = to + phaseCode(phase)
			}
		}
		if ui.StateOf(&h) != state {
			return false
		}
		let want = if held == 10 { ui.Find(&h, "a") } else if held == 20 { ui.Find(&h, "b") } else { None }
		if ui.Captured(&h) != want {
			return false
		}
	}
	true
}

func lawAnnounceSaysEachNewText(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		announceFollows(seed, steps, m)
	}
}

func announceFollows(seed int, steps int, m time.Manual) bool needs Scheduler {
	let mut r = seeded(seed)
	let words = []string{"", "moved", "dropped", "cancelled"}
	let mut texts = []string{}
	for _ in 0..6 {
		texts = texts.Append(words[draw(&r, 4)])
	}
	let shift = sync.NewRef(0)
	let view = func(f *ui.Frame[int], s int) {
		let shown = (s + shift.Get()) % 6
		ui.OnKey(f, func(k ui.KeyPress) Option[int] {
			for i in 0..6 {
				if k.Name == "\(i)" {
					return Some{Value: i}
				}
			}
			None
		})
		ui.Text(f, "at \(s)")
		// At text 5 there is no live region, so the next text's is new.
		if shown != 5 {
			ui.Announce(f, texts[shown])
		}
	}
	let a = actor.StartMachine(zoneMachine(), 1, func(o int) {})
	let mut h = ui.NewTestHost(a, view, m, 4) catch {
	case ui.BadPatch:
		return false
	}
	let mut want = []string{}
	let mut prev Option[string] = Some{Value: texts[0]}
	for _ in 0..(steps%12+12)%12+1 {
		let s = draw(&r, 6)
		let by = draw(&r, 3)
		if by == 0 {
			ui.PressKey(&h, ui.KeyPress{Name: "\(s)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
			case _:
				return false
			}
		} else if by == 1 {
			a.Send(s) catch {
			case _:
				return false
			}
			ui.Settle(&h) catch {
			case _:
				return false
			}
		} else {
			shift.Set(draw(&r, 6))
			ui.Redraw(&h) catch {
			case _:
				return false
			}
		}
		let k = (ui.StateOf(&h) + shift.Get()) % 6
		// prev is the region's text, None while there is no region.
		if k == 5 {
			prev = None
		} else {
			if prev != (Some{Value: texts[k]}) && texts[k] != "" {
				want = want.Append(texts[k])
			}
			prev = Some{Value: texts[k]}
		}
		let regions = if k == 5 { 0 } else { 1 }
		if len(ui.Locate(&h, ui.WithRole{Role: ui.Status, Name: texts[k]})) != regions {
			return false
		}
	}
	let said = ui.Announced(&h)
	if len(said) != len(want) {
		return false
	}
	for i, x in want {
		if said[i] != x {
			return false
		}
	}
	true
}

func posMod(x int, m int) int { (x%m + m) % m }

func lawEdgeScrollStaysInRange(o int, hh int, c int, yy int, e int, st int) bool {
	let height = ui.Length(float64(posMod(hh, 500) + 1))
	let content = ui.Length(float64(posMod(c, 2000)))
	let offset = ui.Length(float64(posMod(o, 2500)))
	let y = ui.Length(float64(posMod(yy, 700) - 100))
	let edge = ui.Length(float64(posMod(e, 60)))
	let step = ui.Length(float64(posMod(st, 40) + 1))
	let most = if content > height { content - height } else { ui.Length(0.0) }
	let at = if offset > most { most } else { offset }
	let next = ui.EdgeScroll(offset, height, content, y, edge, step)
	if next < ui.Length(0.0) || next > most {
		return false
	}
	if edge > ui.Length(0.0) && y < edge {
		return next < at || at == ui.Length(0.0)
	}
	if edge > ui.Length(0.0) && y > height-edge {
		return next > at || at == most
	}
	next == at
}

func lawTransitionEndsAtLayout(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let lists = presenceLists(seed)
		let view = func(f *ui.Frame[int], s int) {
			ui.OnKey(f, func(k ui.KeyPress) Option[int] {
				for i in 0..8 {
					if k.Name == "\(i)" {
						return Some{Value: i}
					}
				}
				None
			})
			ui.Column(f, ui.Plain()) {
				ui.Presence(f, lists[s], func(r int) ui.Key { ui.ByInt(r) }, 100ms) func(r int, p ui.Progress) {
					ui.Text(f, "row \(r)")
				}
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		ui.FrameEvery(&h, 10ms)
		let mut x = seed
		for _ in 0..(steps%10+10)%10+1 {
			x = (x*1103515245 + 12345) % 2147483648
			if x < 0 {
				x = -x
			}
			ui.PressKey(&h, ui.KeyPress{Name: "\(x % 8)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
			case _:
				return false
			}
			ui.Advance(&h, duration(int64(x%15)*int64(10ms))) catch {
			case _:
				return false
			}
		}
		ui.Advance(&h, 2s) catch {
		case _:
			return false
		}
		let s = ui.StateOf(&h)
		for k in presenceKeys(ui.Screen(&h)) {
			let (_, motion, t) = k
			if motion != ui.Present || t != 1.0 {
				return false
			}
		}
		let mut g = ui.NewFrame[int]()
		let want = ui.Render(&g, func(f *ui.Frame[int], xs []int) {
			ui.Column(f, ui.Plain()) {
				ui.Each(f, xs, func(r int) ui.Key { ui.ByInt(r) }) func(r int) {
					ui.Box(f, ui.Plain()) {
						ui.Text(f, "row \(r)")
					}
				}
			}
		}, lists[s])
		let _ = want
		!ui.WantsFrames(&h) && ui.SameShapes(presenceStripped(ui.Screen(&h)), ui.FrameShape(&g))
	}
}

func lawPresenceKeepsTheList(seed int, steps int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let lists = presenceLists(seed)
		let view = func(f *ui.Frame[int], s int) {
			ui.OnKey(f, func(k ui.KeyPress) Option[int] {
				for i in 0..8 {
					if k.Name == "\(i)" {
						return Some{Value: i}
					}
				}
				None
			})
			ui.Presence(f, lists[s], func(r int) ui.Key { ui.ByInt(r) }, 50ms) func(r int, p ui.Progress) {
				ui.Text(f, "row \(r)")
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		ui.FrameEvery(&h, 10ms)
		let mut x = seed + 7
		for _ in 0..(steps%14+14)%14+1 {
			let before = presenceKeys(ui.Screen(&h))
			x = (x*1103515245 + 12345) % 2147483648
			if x < 0 {
				x = -x
			}
			let i = x % 8
			ui.PressKey(&h, ui.KeyPress{Name: "\(i)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
			case _:
				return false
			}
			let after = presenceKeys(ui.Screen(&h))
			let list = lists[ui.StateOf(&h)]
			// The rows not exiting are the list.
			let mut staying = []int{}
			let mut moving = false
			for k in after {
				let (key, motion, t) = k
				if t < 0.0 || t > 1.0 || (motion == ui.Present && t != 1.0) {
					return false
				}
				if motion != ui.Exiting {
					staying = staying.Append(key)
				}
				if motion != ui.Present {
					moving = true
				}
				let mut shown = false
				for b in before {
					let (bk, _, _) = b
					shown = shown || bk == key
				}
				let mut listed = false
				for y in list {
					listed = listed || y == key
				}
				if !shown && (motion != ui.Entering || t != 0.0) {
					return false
				}
				if !listed && motion != ui.Exiting {
					return false
				}
			}
			if staying != list || ui.WantsFrames(&h) != moving {
				return false
			}
			// A lost row comes right after the row before it, or first.
			for j, b in before {
				let (bk, _, _) = b
				let mut listed = false
				for y in list {
					listed = listed || y == bk
				}
				if listed {
					continue
				}
				let mut at = -1
				for q, k in after {
					let (key, _, _) = k
					if key == bk {
						at = q
					}
				}
				if at < 0 {
					// gone at once only when it was exiting already, or
					// had not come in at all (T 0)
					let (_, bm, bt) = b
					if bm != ui.Exiting && bt != 0.0 {
						return false
					}
					continue
				}
				if j > 0 && at > 0 {
					let (prev, _, _) = before[j-1]
					let (got, _, _) = after[at-1]
					let mut prevListed = false
					for y in list {
						prevListed = prevListed || y == prev
					}
					if prevListed && got != prev {
						return false
					}
				}
			}
			if x%2 == 0 {
				ui.Advance(&h, 10ms) catch {
				case _:
					return false
				}
			}
		}
		true
	}
}

func lawExitTakesItsDuration(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let k = (n%10+10)%10 + 1
		let lists = [][]int{[]int{0}, []int{0, 1}, []int{0}}
		let view = func(f *ui.Frame[int], s int) {
			ui.OnKey(f, func(kp ui.KeyPress) Option[int] {
				for i in 0..3 {
					if kp.Name == "\(i)" {
						return Some{Value: i}
					}
				}
				None
			})
			ui.Presence(f, lists[s], func(r int) ui.Key { ui.ByInt(r) }, 100ms) func(r int, p ui.Progress) {
				ui.Text(f, "row \(r)")
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		ui.FrameEvery(&h, 10ms)
		let first = presenceKeys(ui.Screen(&h))
		if len(first) != 1 || !near(first[0], rowAt(0, ui.Present, 1.0)) {
			return false
		}
		ui.PressKey(&h, ui.KeyPress{Name: "1", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
		case _:
			return false
		}
		// The entry starts at the first frame after the press: k+1 frames
		// bring it k tenths in.
		for _ in 0..k+1 {
			ui.Advance(&h, 10ms) catch {
			case _:
				return false
			}
		}
		let reached = presenceKeys(ui.Screen(&h))
		let mut t = 1.0
		if k < 10 {
			t = float64(k) / 10.0
		}
		let mut want = rowAt(1, ui.Present, 1.0)
		if k < 10 {
			want = rowAt(1, ui.Entering, t)
		}
		if len(reached) != 2 || !near(reached[1], want) {
			return false
		}
		ui.PressKey(&h, ui.KeyPress{Name: "2", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
		case _:
			return false
		}
		let removed = presenceKeys(ui.Screen(&h))
		if len(removed) != 2 || !near(removed[1], rowAt(1, ui.Exiting, t)) {
			return false
		}
		// The exit starts at the next frame and loses a tenth per frame.
		for j in 1..k+2 {
			ui.Advance(&h, 10ms) catch {
			case _:
				return false
			}
			let shown = presenceKeys(ui.Screen(&h))
			let left = t - float64(j-1)/10.0
			if left <= 0.000001 {
				if len(shown) != 1 {
					return false
				}
			} else if len(shown) != 2 || !near(shown[1], rowAt(1, ui.Exiting, left)) {
				return false
			}
		}
		len(presenceKeys(ui.Screen(&h))) == 1 && !ui.WantsFrames(&h)
	}
}

func lawNoTickWhenRowsSettle(seed int, wait int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let lists = presenceLists(seed)
		let view = func(f *ui.Frame[int], s int) {
			ui.OnKey(f, func(k ui.KeyPress) Option[int] {
				for i in 0..8 {
					if k.Name == "\(i)" {
						return Some{Value: i}
					}
				}
				None
			})
			ui.Presence(f, lists[s], func(r int) ui.Key { ui.ByInt(r) }, 30ms) func(r int, p ui.Progress) {
				ui.Text(f, "row \(r)")
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		ui.FrameEvery(&h, 10ms)
		let mut x = seed + 3
		for _ in 0..4 {
			x = (x*1103515245 + 12345) % 2147483648
			if x < 0 {
				x = -x
			}
			ui.PressKey(&h, ui.KeyPress{Name: "\(x % 8)", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
			case _:
				return false
			}
			ui.Advance(&h, 1s) catch {
			case _:
				return false
			}
			for k in presenceKeys(ui.Screen(&h)) {
				let (_, motion, _) = k
				if motion != ui.Present {
					return false
				}
			}
			if ui.WantsFrames(&h) {
				return false
			}
			let before = ui.Frames(&h)
			ui.Advance(&h, duration(int64((wait%50+50)%50+1)*int64(10ms))) catch {
			case _:
				return false
			}
			if ui.Frames(&h) != before {
				return false
			}
		}
		true
	}
}

func presenceListStep(s int, e int) Decision[int, int] {
	Enter[int]{Next: e}
}

func presenceTimer(s int) Option[Timeout[int]] {
	None
}

func presenceWork(s int) Option[func(send func(e int))] {
	None
}

func presenceLists(seed int) [][]int {
	let mut x = seed
	let mut out = [][]int{}
	for _ in 0..8 {
		let mut xs = []int{}
		for k in 0..6 {
			x = (x*1103515245 + 12345) % 2147483648
			if x < 0 {
				x = -x
			}
			if x%3 != 0 {
				xs = xs.Append(k)
			}
		}
		// a reorder: odd lists run backward
		if len(out)%2 == 1 {
			let mut back = []int{}
			for i in 0..len(xs) {
				back = back.Append(xs[len(xs)-1-i])
			}
			xs = back
		}
		out = out.Append(xs)
	}
	out
}

func presenceKeys(shapes []ui.Shape[int]) [](int, ui.Motion, float64) {
	let mut out = [](int, ui.Motion, float64){}
	for s in shapes {
		match s.Node {
		case ui.TransitionNode as t:
			match t.Key {
			case ui.IntKey as k:
				out = out.Append((k.Value, t.Motion, t.T))
			case _:
				out = out.Append((-1, t.Motion, t.T))
			}
		case _:
			for x in presenceKeys(s.Kids) {
				out = out.Append(x)
			}
		}
	}
	out
}

func presenceStripped(shapes []ui.Shape[int]) []ui.Shape[int] {
	let mut out = []ui.Shape[int]{}
	for s in shapes {
		match s.Node {
		case ui.TransitionNode:
			for k in presenceStripped(s.Kids) {
				out = out.Append(k)
			}
		case _:
			out = out.Append(ui.Shape[int]{Node: s.Node, Kids: presenceStripped(s.Kids)})
		}
	}
	out
}

func near(a (int, ui.Motion, float64), b (int, ui.Motion, float64)) bool {
	let (ak, am, at) = a
	let (bk, bm, bt) = b
	ak == bk && am == bm && at-bt < 0.000001 && bt-at < 0.000001
}

func rowAt(k int, m ui.Motion, t float64) (int, ui.Motion, float64) {
	(k, m, t)
}

func lawSettleRunsTheWork(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let last = (n%8+8)%8 + 1
		let a = actor.StartMachine(Machine[chain, int, int]{Initial: chain{at: 0, last: last}, Step: chainStep, After: chainTimer, Work: chainWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, chainView, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		let before = m.Now()
		ui.PressKey(&h, ui.KeyPress{Name: "go", Mods: ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch {
		case _:
			return false
		}
		ui.Settle(&h) catch {
		case _:
			return false
		}
		let mut shown = false
		for s in ui.Screen(&h) {
			shown = shown || s.Node == (ui.TextNode{Value: "at \(last)", Style: ui.Plain()})
		}
		ui.StateOf(&h).at == last && shown && m.Now() == before
	}
}

func lawPressIsOnePass(n int, at int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let small = (n%8+8)%8 + 2
		let row = (at%small + small) % small
		match (pressCounts(small, row, m), pressCounts(4*small, row, m)) {
		case (Some[(ui.StepCounts, ui.StepCounts)] as x, Some[(ui.StepCounts, ui.StepCounts)] as y):
			let (p1, r1) = x.Value
			let (p4, r4) = y.Value
			p1.GroupsRan == 1 && p4.GroupsRan == 1 && p4.Ran-p1.Ran == (r4.Ran-p4.Ran)-(r1.Ran-p1.Ran) && p4.Ran > p1.Ran
		case _:
			false
		}
	}
}

func lawCountsAreTheStep(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let a = actor.StartMachine(Machine[chain, int, int]{Initial: chain{at: 0, last: 1}, Step: chainStep, After: chainTimer, Work: chainWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, chainView, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		let mods = ui.Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}
		ui.PressKey(&h, ui.KeyPress{Name: "go", Mods: mods}) catch {
		case _:
			return false
		}
		let pressed = ui.Counts(&h)
		ui.Settle(&h) catch {
		case _:
			return false
		}
		let settled = ui.Counts(&h)
		// ui.Each ui.Redraw adds its one pass of one node; a ui.Settle, none.
		for _ in 0..(n%3+3)%3+1 {
			let ran = ui.Counts(&h).Ran
			ui.Redraw(&h) catch {
			case _:
				return false
			}
			ui.Settle(&h) catch {
			case _:
				return false
			}
			if ui.Counts(&h).Ran != ran+1 {
				return false
			}
		}
		ui.PressKey(&h, ui.KeyPress{Name: "noop", Mods: mods}) catch {
		case _:
			return false
		}
		if ui.Counts(&h) != ui.NoCounts() {
			return false
		}
		// ui.PlayTrace prints that step as its line.
		let trace = ui.PlayTrace(&h, []ui.Act{ui.KeyIn{Press: ui.KeyPress{Name: "noop", Mods: mods}}}) catch {
		case _:
			return false
		}
		let line = "step 1 KeyIn \"noop\": no ops; ran 0 nodes, kept 0; groups ran 0, kept 0; visits 0"
		pressed.Set == 1 && pressed.Ran == 1 && settled == pressed && trace == []string{line}
	}
}

func lawAmbiguousActFails(seed int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let k = (seed%4+4)%4 + 2
		let view = func(f *ui.Frame[int], s int) {
			ui.Column(f, ui.Plain()) {
				ui.Text(f, "at \(s)")
				for i in 0..k {
					ui.Pressable(f, "dup", i+1) {
						ui.Text(f, "x")
					}
					ui.Pressable(f, "one\(i)", 50+i) {
						ui.Text(f, "y")
					}
					ui.Input(f, "", "dupi", func(t string) int { 100 + i })
				}
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: lastStep, After: lastTimer, Work: lastWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		let acts = []ui.Act{ui.TapOn{Label: "dup"}, ui.ClickOn{At: ui.WithRole{Role: ui.Button, Name: "dup"}}, ui.ClickOn{At: ui.WithLabel{Label: "dup"}}, ui.TypeInto{Label: "dupi", Text: "t"}, ui.FillIn{At: ui.WithPlaceholder{Text: "dupi"}, Text: "t"}}
		for act in acts {
			let mut count = -1
			ui.Play(&h, []ui.Act{act}) catch {
			case ui.Ambiguous as e:
				count = e.Count
			case _:
				return false
			}
			ui.Settle(&h) catch {
			case _:
				return false
			}
			if count != k || ui.StateOf(&h) != 0 {
				return false
			}
		}
		// A unique label acts, and ui.First names the first match.
		ui.Play(&h, []ui.Act{ui.TapOn{Label: "one1"}, ui.SettleAll}) catch {
		case _:
			return false
		}
		if ui.StateOf(&h) != 51 {
			return false
		}
		ui.Play(&h, []ui.Act{ui.ClickOn{At: ui.First{At: ui.WithRole{Role: ui.Button, Name: "dup"}}}, ui.SettleAll}) catch {
		case _:
			return false
		}
		if ui.StateOf(&h) != 1 {
			return false
		}
		ui.Play(&h, []ui.Act{ui.FillIn{At: ui.First{At: ui.WithPlaceholder{Text: "dupi"}}, Text: "t"}, ui.SettleAll}) catch {
		case _:
			return false
		}
		let firsts = ui.Locate(&h, ui.First{At: ui.WithLabel{Label: "dup"}})
		ui.StateOf(&h) == 100 && len(firsts) == 1 && ui.Find(&h, "dup") == (Some{Value: firsts[0]})
	}
}

type chain { at int, last int }

func chainStep(s chain, e int) Decision[chain, int] {
	Enter[chain]{Next: chain{at: e, last: s.last}}
}

func chainTimer(s chain) Option[Timeout[int]] {
	None
}

func chainWork(s chain) Option[func(send func(e int))] {
	if s.at < 1 || s.at >= s.last {
		return None
	}
	let next = s.at + 1
	// The work runs a while first, so a ui.Settle that does not wait for
	// it ends before it sends.
	Some{Value: func(send func(e int)) {
		let mut x = next
		for i in 0..2000000 {
			x = (x*31 + i) % 1000003
		}
		if x >= 0 {
			send(next)
		}
	}}
}

func chainView(f *ui.Frame[int], s chain) {
	ui.OnKey(f, func(k ui.KeyPress) Option[int] {
		if k.Name == "go" {
			return Some{Value: 1}
		}
		None
	})
	ui.Text(f, "at \(s.at)")
}

func tallyStep(s []int, e int) Decision[[]int, int] {
	let mut next = []int{}
	for i, c in s {
		next = next.Append(if i == e { c + 1 } else { c })
	}
	Enter[[]int]{Next: next}
}

func tallyTimer(s []int) Option[Timeout[int]] {
	None
}

func tallyWork(s []int) Option[func(send func(e int))] {
	None
}

func tallyView(f *ui.Frame[int], s []int) {
	ui.Column(f, ui.Plain()) {
		let mut idx = []int{}
		for i in 0..len(s) {
			idx = idx.Append(i)
		}
		ui.Each(f, idx, func(i int) ui.Key { ui.ByInt(i) }) func(i int) {
			ui.Same(f, ui.ByInt(s[i])) {
				ui.Text(f, "row \(i): \(s[i])")
				ui.Pressable(f, "plus \(i)", i) {
					ui.Text(f, "+")
				}
			}
		}
	}
}

func pressCounts(n int, at int, m time.Manual) Option[(ui.StepCounts, ui.StepCounts)] needs Scheduler {
	let mut rows = []int{}
	for _ in 0..n {
		rows = rows.Append(0)
	}
	let a = actor.StartMachine(Machine[[]int, int, int]{Initial: rows, Step: tallyStep, After: tallyTimer, Work: tallyWork}, 1, func(o int) {})
	let mut h = ui.NewTestHost(a, tallyView, m, 4) catch {
	case ui.BadPatch:
		return None
	}
	let id = match ui.Find(&h, "plus \(at)") {
	case Some[ui.Id[int]] as x:
		x.Value
	case None:
		return None
	}
	ui.Tap(&h, id) catch {
	case _:
		return None
	}
	ui.Settle(&h) catch {
	case _:
		return None
	}
	let pressed = ui.Counts(&h)
	ui.Redraw(&h) catch {
	case _:
		return None
	}
	let redrawn = ui.Counts(&h)
	a.Stop()
	Some{Value: (pressed, redrawn)}
}

func lastStep(s int, e int) Decision[int, int] {
	Enter[int]{Next: e}
}

func lastTimer(s int) Option[Timeout[int]] {
	None
}

func lastWork(s int) Option[func(send func(e int))] {
	None
}

func lawAccessIsWhatWasDeclared(seed int, count int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let n = (count%6+6)%6 + 2
		let view = func(f *ui.Frame[int], s int) {
			let ds = semDecls(seed+s*7919, n)
			ui.Column(f, ui.Plain()) {
				for i, d in ds {
					if i%2 == 0 {
						ui.Semantic(f, d, Some{Value: i + 1}, ui.Plain()) {
							ui.Text(f, "t\(i)")
						}
					} else {
						ui.Fieldset(f, true, "fs") {
							ui.Semantic(f, d, Some{Value: i + 1}, ui.Plain()) {
								ui.Text(f, "t\(i)")
							}
						}
					}
				}
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: semStep, After: semTimer, Work: semWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		for s in 0..2 {
			let ds = semDecls(seed+ui.StateOf(&h)*7919, n)
			for i, d in ds {
				let ids = ui.Locate(&h, ui.WithLabel{Label: d.Name})
				if len(ids) != 1 {
					return false
				}
				let mut want = d
				if i%2 == 1 {
					want.Disabled = true
					if d.Description == "" {
						want.Description = "fs"
					}
				}
				if ui.AccessAt(&h, ids[0]) != want {
					return false
				}
				// The matchers of the new states read the same record.
				if semHolds(&h, d.Name, ui.ToBeSelected) != (want.Selected == (Some{Value: true})) || semHolds(&h, d.Name, ui.ToBeExpanded) != (want.Expanded == (Some{Value: true})) || semHolds(&h, d.Name, ui.ToBeCollapsed) != (want.Expanded == (Some{Value: false})) || semHolds(&h, d.Name, ui.ToBeInvalid) != want.Invalid {
					return false
				}
			}
			if s == 0 {
				// A press on the first: the signal when it is enabled.
				let first = ui.Locate(&h, ui.WithLabel{Label: "n0"})
				ui.Tap(&h, first[0]) catch {
				case _:
					return false
				}
				ui.Settle(&h) catch {
				case _:
					return false
				}
				if ds[0].Disabled != (ui.StateOf(&h) == 0) {
					return false
				}
			}
		}
		true
	}
}

func lawWithinFindsUnder(seed int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let mut r = seeded(seed)
		let mut board = [][]int{}
		for _ in 0..draw(&r, 4)+1 {
			let mut col = []int{}
			for _ in 0..draw(&r, 5) {
				col = col.Append(draw(&r, 8))
			}
			board = board.Append(col)
		}
		let none Option[int] = None
		let decl = func(role ui.Role, name string, level Option[int]) ui.Access {
			ui.Access{Role: role, Name: name, Disabled: false, Description: "", Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: level}
		}
		let view = func(f *ui.Frame[int], s int) {
			ui.Column(f, ui.Plain()) {
				for i, col in board {
					ui.Semantic(f, decl(ui.Region, "col \(i)", None), none, ui.Plain()) {
						ui.Semantic(f, decl(ui.Heading, "col \(i)", Some{Value: 2}), none, ui.Plain()) {
							ui.Text(f, "col \(i)")
						}
						for j, c in col {
							if c%2 == 0 {
								ui.Pressable(f, withinLabel(c/2), 100*i+j) {
									ui.Text(f, "x")
								}
							} else {
								ui.Box(f, ui.Plain()) {
									ui.Pressable(f, withinLabel(c/2), 100*i+j) {
										ui.Text(f, "x")
									}
								}
							}
						}
					}
				}
			}
		}
		let a = actor.StartMachine(Machine[int, int, int]{Initial: -1, Step: semStep, After: semTimer, Work: semWork}, 1, func(o int) {})
		let mut h = ui.NewTestHost(a, view, m, 4) catch {
		case ui.BadPatch:
			return false
		}
		for i, col in board {
			let column = ui.WithRole{Role: ui.Region, Name: "col \(i)"}
			let found = ui.Locate(&h, ui.Within{In: column, At: ui.WithRole{Role: ui.Button, Name: ""}})
			if len(found) != len(col) {
				return false
			}
			for j, c in col {
				if ui.AccessAt(&h, found[j]).Name != withinLabel(c/2) {
					return false
				}
			}
			let nested = ui.Locate(&h, ui.Within{In: ui.WithRole{Role: ui.Region, Name: ""}, At: ui.Within{In: column, At: ui.WithRole{Role: ui.Button, Name: ""}}})
			if nested != found {
				return false
			}
			for j, c in col {
				let label = withinLabel(c / 2)
				let mut first = j
				for k in 0..j {
					if withinLabel(col[k]/2) == label && first == j {
						first = k
					}
				}
				if first == j {
					ui.Play(&h, []ui.Act{ui.ClickOn{At: ui.First{At: ui.Within{In: column, At: ui.WithLabel{Label: label}}}}, ui.SettleAll}) catch {
					case _:
						return false
					}
					if ui.StateOf(&h) != 100*i+j {
						return false
					}
				}
			}
		}
		// A column is not within itself, nor within another: none nests.
		let regions = ui.WithRole{Role: ui.Region, Name: ""}
		len(ui.Locate(&h, ui.Within{In: ui.WithRole{Role: ui.Region, Name: "col 9"}, At: ui.WithRole{Role: ui.Button, Name: ""}})) == 0 && len(ui.Locate(&h, ui.Within{In: regions, At: regions})) == 0
	}
}

func semRoles() []ui.Role {
	[]ui.Role{ui.Button, ui.Checkbox, ui.Switch, ui.TextBox, ui.Static, ui.Group, ui.Region, ui.Image, ui.List, ui.Status, ui.Generic, ui.Tab, ui.TabList, ui.TabPanel, ui.Dialog, ui.Menu, ui.MenuItem, ui.Tree, ui.TreeItem, ui.Heading, ui.Alert, ui.Link, ui.Table, ui.TableRow, ui.TableCell}
}

func semMaybe(r *rng) Option[bool] {
	let k = draw(r, 3)
	if k == 0 {
		return None
	}
	Some{Value: k == 1}
}

func semDecls(seed int, n int) []ui.Access {
	let mut r = seeded(seed)
	let roles = semRoles()
	let mut out = []ui.Access{}
	for i in 0..n {
		let mut level Option[int] = None
		if draw(&r, 2) == 0 {
			level = Some{Value: draw(&r, 6) + 1}
		}
		let mut value Option[string] = None
		if draw(&r, 2) == 0 {
			value = Some{Value: "v\(draw(&r, 9))"}
		}
		let mut desc = ""
		if draw(&r, 2) == 0 {
			desc = "d\(draw(&r, 9))"
		}
		out = out.Append(ui.Access{Role: roles[draw(&r, len(roles))], Name: "n\(i)", Disabled: draw(&r, 4) == 0, Description: desc, Checked: semMaybe(&r), Value: value, Selected: semMaybe(&r), Expanded: semMaybe(&r), Pressed: semMaybe(&r), Invalid: draw(&r, 2) == 0, Busy: draw(&r, 2) == 0, HasPopup: draw(&r, 2) == 0, Level: level})
	}
	out
}

func semHolds[D, F](h *ui.TestHost[int, int, D, F], name string, is ui.Matcher) bool {
	ui.Expect(h, ui.WithLabel{Label: name}, is) catch {
	case ui.Mismatch:
		return false
	}
	true
}

func semStep(s int, e int) Decision[int, int] {
	Enter[int]{Next: e}
}

func semTimer(s int) Option[Timeout[int]] {
	None
}

func semWork(s int) Option[func(send func(e int))] {
	None
}

func withinLabel(j int) string {
	"card \(j % 2)"
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

test UiAdvanceTakesThePosted(n int) {
	assert lawAdvanceTakesThePosted(n)
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

test UiLoadUsesTheBoundService(seed int) {
	assert lawLoadUsesTheBoundService(seed)
}

test UiRowsKeepTheCursor(seed int, steps int) {
	assert lawRowsKeepTheCursor(seed, steps)
}

test UiEscapeRoundTrips(s string, t string) {
	assert lawEscapeRoundTrips(s, t)
}

test UiSegmentsReadPathOf(xs []string) {
	assert lawSegmentsReadPathOf(xs)
}

test UiQueryRoundTrips(seed int, a string, b string, tags []string) {
	assert lawQueryRoundTrips(seed, a, b, tags)
}

test UiWorkReplyIsShown(n int) {
	assert lawWorkReplyIsShown(n)
}

test UiWatcherHoldsOneState(n int) {
	assert lawWatcherHoldsOneState(n)
}

test UiWatcherShowsWorkReplies(n int) {
	assert lawWatcherShowsWorkReplies(n)
}

test UiCrashShowsTheCrashView(seed int, steps int) {
	assert lawCrashShowsTheCrashView(seed, steps)
}

test UiPlainCrashSaysWhatItKnows(reason string, e int) {
	assert lawPlainCrashSaysWhatItKnows(reason, e)
}

test UiReloadIsInitial(seed int, defect bool) {
	assert lawReloadIsInitial(seed, defect)
}

test UiSessionCrashFollowsTheEnd(n int) {
	assert lawSessionCrashFollowsTheEnd(n)
}

test UiStatsCountEveryPhase(seed int, steps int) {
	assert lawStatsCountEveryPhase(seed, steps)
}

test UiSlowAboveTheBudget(seed int, bound int) {
	assert lawSlowAboveTheBudget(seed, bound)
}

test UiZoneHitNamesItsPath(seed int, n int) {
	assert lawZoneHitNamesItsPath(seed, n)
}

test UiCaptureKeepsThePointer(seed int, steps int) {
	assert lawCaptureKeepsThePointer(seed, steps)
}

test UiAnnounceSaysEachNewText(seed int, steps int) {
	assert lawAnnounceSaysEachNewText(seed, steps)
}

test UiEdgeScrollStaysInRange(o int, hh int, c int, yy int, e int, st int) {
	assert lawEdgeScrollStaysInRange(o, hh, c, yy, e, st)
}

test UiTransitionEndsAtLayout(seed int, steps int) {
	assert lawTransitionEndsAtLayout(seed, steps)
}

test UiPresenceKeepsTheList(seed int, steps int) {
	assert lawPresenceKeepsTheList(seed, steps)
}

test UiExitTakesItsDuration(n int) {
	assert lawExitTakesItsDuration(n)
}

test UiNoTickWhenRowsSettle(seed int, wait int) {
	assert lawNoTickWhenRowsSettle(seed, wait)
}

test UiSettleRunsTheWork(n int) {
	assert lawSettleRunsTheWork(n)
}

test UiPressIsOnePass(n int, at int) {
	assert lawPressIsOnePass(n, at)
}

test UiCountsAreTheStep(n int) {
	assert lawCountsAreTheStep(n)
}

test UiAmbiguousActFails(seed int) {
	assert lawAmbiguousActFails(seed)
}

test UiAccessIsWhatWasDeclared(seed int, count int) {
	assert lawAccessIsWhatWasDeclared(seed, count)
}

test UiWithinFindsUnder(seed int) {
	assert lawWithinFindsUnder(seed)
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
	"ui AdvanceTakesThePosted":         `AdvanceTakesThePosted(n int) { let m = time.NewManual(0) with context { Scheduler: m } { advanceTakes(n, m) } }`,
	"ui InputGivesSignal":              `InputGivesSignal(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { inputFollows(seed, steps, m) } }`,
	"ui PaintMatchesList":              `PaintMatchesList(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { paintFollows(seed, steps, m) } }`,
	"ui RowsAreTheCutEach":             `RowsAreTheCutEach(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { rowsFollow(seed, steps, m) } }`,
	"ui HrefParses":                    `HrefParses(seed int) { let mut r = seeded(seed) let parts = []string{"", "a", "b/c", "x=1", "q=2&y", "top"} let u = URL{Path: "/" + parts[draw(&r, len(parts))], Query: parts[draw(&r, len(parts))], Fragment: parts[draw(&r, len(parts))]} ParseURL(Href(u)) == u }`,
	"ui NavigationFollowsModel":        `NavigationFollowsModel(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { navigationFollows(seed, steps, m) } }`,
	"ui LoadUsesTheBoundService":       `LoadUsesTheBoundService(seed int) { let m = time.NewManual(0) with context { Scheduler: m } { loadsBound(seed, m) } }`,
	"ui RowsKeepTheCursor":             `RowsKeepTheCursor(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { cursorKept(seed, steps, m) } }`,
	"ui EscapeRoundTrips":              `EscapeRoundTrips(s string, t string) { let e = Escape(s) let mut lower = "" for i in 0..len(s) { lower = lower + "%" + hex.Encode(s[i:i+1]) } let plain = strings.ReplaceAll(t, "%", "") escapedForm(e) && Unescape(e) == s && Unescape(lower) == s && Unescape(plain) == plain && Unescape(s+"%4g"+plain) == Unescape(s)+"%4g"+plain }`,
	"ui SegmentsReadPathOf":            `SegmentsReadPathOf(xs []string) { let path = PathOf(xs) let mut want = []string{} for x in xs { if x != "" { want = want.Append(x) } } let got = Segments(URL{Path: path, Query: "", Fragment: ""}) if len(got) != len(want) || ParseURL(path).Path != path { return false } for i, x in want { if got[i] != x { return false } } true }`,
	"ui QueryRoundTrips":               `QueryRoundTrips(seed int, a string, b string, tags []string) { let mut r = seeded(seed) let mut note json.Optional[string] = json.Absent if draw(&r, 2) == 0 { note = json.Present{Value: b + a} } let mut ts = tags if len(ts) == 0 { ts = []string{a} } let v = queryRecord{Name: a, Sort: b, Page: draw(&r, 2001) - 1000, Big: uint64(draw(&r, 1000000)) * 18446744073709, Ratio: float64(draw(&r, 2001)-1000) / 8.0, On: draw(&r, 2) == 0, Note: note, Tags: ts} let u = WithQuery(URL{Path: "/list", Query: "", Fragment: "top"}, v) catch { case QueryShape: return false } let back = QueryOf[queryRecord](ParseURL(Href(u))) catch { case _: return false } let nested = EncodeQuery(queryNested{Name: a, Inner: v}) catch { case QueryShape as q: q.Key } let scalar = EncodeQuery(draw(&r, 9)) catch { case QueryShape as q: "none" + q.Key } back == v && u.Path == "/list" && u.Fragment == "top" && nested == "inner" && scalar == "none" }`,
	"ui WorkReplyIsShown":              `WorkReplyIsShown(n int) { let m = time.NewManual(0) with context { Scheduler: m } { replyShown(n, m) } }`,
	"ui WatcherHoldsOneState":          `WatcherHoldsOneState(n int) { let m = time.NewManual(0) with context { Scheduler: m } { watcherHolds(n, m) } }`,
	"ui WatcherShowsWorkReplies":       `WatcherShowsWorkReplies(n int) { let m = time.NewManual(0) with context { Scheduler: m } { watchReplies(n) } }`,
	"ui CrashShowsTheCrashView":        `CrashShowsTheCrashView(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { crashShown(seed, steps, m) } }`,
	"ui PlainCrashSaysWhatItKnows":     `PlainCrashSaysWhatItKnows(reason string, e int) { let mut f = NewFrame[int]() let sent = Render(&f, PlainCrash[int, int, Boom], Crash[int, int, Boom]{Cause: DefectCrash{Reason: "at \(e): " + reason}, State: 0, Signal: Some{Value: e}}) let mut g = NewFrame[int]() let none = Render(&g, PlainCrash[int, int, Boom], Crash[int, int, Boom]{Cause: FailureCrash[Boom]{Failure: Boom{N: e}}, State: 0, Signal: None}) writesText(sent, "The program stopped: a defect ended it: at \(e): \(reason)") && writesText(sent, "signal: the last one the window sent (or a timer or work around it)") && writesText(none, "signal: none (timer or work)") }`,
	"ui ReloadIsInitial":               `ReloadIsInitial(seed int, defect bool) { let m = time.NewManual(0) with context { Scheduler: m } { reloadInitial(seed, defect, m) } }`,
	"ui SessionCrashFollowsTheEnd":     `SessionCrashFollowsTheEnd(n int) { let m = time.NewManual(0) with context { Scheduler: m } { crashFollows() } }`,
	"ui StatsCountEveryPhase":          `StatsCountEveryPhase(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { statsFollow(seed, steps) } }`,
	"ui SlowAboveTheBudget":            `SlowAboveTheBudget(seed int, bound int) { let m = time.NewManual(0) with context { Scheduler: m } { slowFollow(seed, (bound%5+5)%5+1) } }`,
	"ui ZoneHitNamesItsPath":           `ZoneHitNamesItsPath(seed int, n int) { let m = time.NewManual(0) with context { Scheduler: m } { zoneFollows(seed, n, m) } }`,
	"ui CaptureKeepsThePointer":        `CaptureKeepsThePointer(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { captureFollows(seed, steps, m) } }`,
	"ui AnnounceSaysEachNewText":       `AnnounceSaysEachNewText(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { announceFollows(seed, steps, m) } }`,
	"ui EdgeScrollStaysInRange":        `EdgeScrollStaysInRange(o int, hh int, c int, yy int, e int, st int) { let height = Length(float64(posMod(hh, 500) + 1)) let content = Length(float64(posMod(c, 2000))) let offset = Length(float64(posMod(o, 2500))) let y = Length(float64(posMod(yy, 700) - 100)) let edge = Length(float64(posMod(e, 60))) let step = Length(float64(posMod(st, 40) + 1)) let most = if content > height { content - height } else { Length(0.0) } let at = if offset > most { most } else { offset } let next = EdgeScroll(offset, height, content, y, edge, step) if next < Length(0.0) || next > most { return false } if edge > Length(0.0) && y < edge { return next < at || at == Length(0.0) } if edge > Length(0.0) && y > height-edge { return next > at || at == most } next == at }`,
	"ui TransitionEndsAtLayout":        `TransitionEndsAtLayout(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { let lists = presenceLists(seed) let view = func(f *Frame[int], s int) { OnKey(f, func(k KeyPress) Option[int] { for i in 0..8 { if k.Name == "\(i)" { return Some{Value: i} } } None }) Column(f, Plain()) { Presence(f, lists[s], func(r int) Key { ByInt(r) }, 100ms) func(r int, p Progress) { Text(f, "row \(r)") } } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } FrameEvery(&h, 10ms) let mut x = seed for _ in 0..(steps%10+10)%10+1 { x = (x*1103515245 + 12345) % 2147483648 if x < 0 { x = -x } PressKey(&h, KeyPress{Name: "\(x % 8)", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } Advance(&h, duration(int64(x%15)*int64(10ms))) catch { case _: return false } } Advance(&h, 2s) catch { case _: return false } let s = StateOf(&h) for k in presenceKeys(Screen(&h)) { let (_, motion, t) = k if motion != Present || t != 1.0 { return false } } let mut g = NewFrame[int]() let want = Render(&g, func(f *Frame[int], xs []int) { Column(f, Plain()) { Each(f, xs, func(r int) Key { ByInt(r) }) func(r int) { Box(f, Plain()) { Text(f, "row \(r)") } } } }, lists[s]) let _ = want !WantsFrames(&h) && SameShapes(presenceStripped(Screen(&h)), FrameShape(&g)) } }`,
	"ui PresenceKeepsTheList":          `PresenceKeepsTheList(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { let lists = presenceLists(seed) let view = func(f *Frame[int], s int) { OnKey(f, func(k KeyPress) Option[int] { for i in 0..8 { if k.Name == "\(i)" { return Some{Value: i} } } None }) Presence(f, lists[s], func(r int) Key { ByInt(r) }, 50ms) func(r int, p Progress) { Text(f, "row \(r)") } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } FrameEvery(&h, 10ms) let mut x = seed + 7 for _ in 0..(steps%14+14)%14+1 { let before = presenceKeys(Screen(&h)) x = (x*1103515245 + 12345) % 2147483648 if x < 0 { x = -x } let i = x % 8 PressKey(&h, KeyPress{Name: "\(i)", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } let after = presenceKeys(Screen(&h)) let list = lists[StateOf(&h)] // The rows not exiting are the list. let mut staying = []int{} let mut moving = false for k in after { let (key, motion, t) = k if t < 0.0 || t > 1.0 || (motion == Present && t != 1.0) { return false } if motion != Exiting { staying = staying.Append(key) } if motion != Present { moving = true } let mut shown = false for b in before { let (bk, _, _) = b shown = shown || bk == key } let mut listed = false for y in list { listed = listed || y == key } if !shown && (motion != Entering || t != 0.0) { return false } if !listed && motion != Exiting { return false } } if staying != list || WantsFrames(&h) != moving { return false } // A lost row comes right after the row before it, or first. for j, b in before { let (bk, _, _) = b let mut listed = false for y in list { listed = listed || y == bk } if listed { continue } let mut at = -1 for q, k in after { let (key, _, _) = k if key == bk { at = q } } if at < 0 { // gone at once only when it was exiting already, or // had not come in at all (T 0) let (_, bm, bt) = b if bm != Exiting && bt != 0.0 { return false } continue } if j > 0 && at > 0 { let (prev, _, _) = before[j-1] let (got, _, _) = after[at-1] let mut prevListed = false for y in list { prevListed = prevListed || y == prev } if prevListed && got != prev { return false } } } if x%2 == 0 { Advance(&h, 10ms) catch { case _: return false } } } true } }`,
	"ui ExitTakesItsDuration":          `ExitTakesItsDuration(n int) { let m = time.NewManual(0) with context { Scheduler: m } { let k = (n%10+10)%10 + 1 let lists = [][]int{[]int{0}, []int{0, 1}, []int{0}} let view = func(f *Frame[int], s int) { OnKey(f, func(kp KeyPress) Option[int] { for i in 0..3 { if kp.Name == "\(i)" { return Some{Value: i} } } None }) Presence(f, lists[s], func(r int) Key { ByInt(r) }, 100ms) func(r int, p Progress) { Text(f, "row \(r)") } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } FrameEvery(&h, 10ms) let first = presenceKeys(Screen(&h)) if len(first) != 1 || !near(first[0], rowAt(0, Present, 1.0)) { return false } PressKey(&h, KeyPress{Name: "1", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } // The entry starts at the first frame after the press: k+1 frames // bring it k tenths in. for _ in 0..k+1 { Advance(&h, 10ms) catch { case _: return false } } let reached = presenceKeys(Screen(&h)) let mut t = 1.0 if k < 10 { t = float64(k) / 10.0 } let mut want = rowAt(1, Present, 1.0) if k < 10 { want = rowAt(1, Entering, t) } if len(reached) != 2 || !near(reached[1], want) { return false } PressKey(&h, KeyPress{Name: "2", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } let removed = presenceKeys(Screen(&h)) if len(removed) != 2 || !near(removed[1], rowAt(1, Exiting, t)) { return false } // The exit starts at the next frame and loses a tenth per frame. for j in 1..k+2 { Advance(&h, 10ms) catch { case _: return false } let shown = presenceKeys(Screen(&h)) let left = t - float64(j-1)/10.0 if left <= 0.000001 { if len(shown) != 1 { return false } } else if len(shown) != 2 || !near(shown[1], rowAt(1, Exiting, left)) { return false } } len(presenceKeys(Screen(&h))) == 1 && !WantsFrames(&h) } }`,
	"ui NoTickWhenRowsSettle":          `NoTickWhenRowsSettle(seed int, wait int) { let m = time.NewManual(0) with context { Scheduler: m } { let lists = presenceLists(seed) let view = func(f *Frame[int], s int) { OnKey(f, func(k KeyPress) Option[int] { for i in 0..8 { if k.Name == "\(i)" { return Some{Value: i} } } None }) Presence(f, lists[s], func(r int) Key { ByInt(r) }, 30ms) func(r int, p Progress) { Text(f, "row \(r)") } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: presenceListStep, After: presenceTimer, Work: presenceWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } FrameEvery(&h, 10ms) let mut x = seed + 3 for _ in 0..4 { x = (x*1103515245 + 12345) % 2147483648 if x < 0 { x = -x } PressKey(&h, KeyPress{Name: "\(x % 8)", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } Advance(&h, 1s) catch { case _: return false } for k in presenceKeys(Screen(&h)) { let (_, motion, _) = k if motion != Present { return false } } if WantsFrames(&h) { return false } let before = Frames(&h) Advance(&h, duration(int64((wait%50+50)%50+1)*int64(10ms))) catch { case _: return false } if Frames(&h) != before { return false } } true } }`,
	"ui SettleRunsTheWork":             `SettleRunsTheWork(n int) { let m = time.NewManual(0) with context { Scheduler: m } { let last = (n%8+8)%8 + 1 let a = actor.StartMachine(Machine[chain, int, int]{Initial: chain{at: 0, last: last}, Step: chainStep, After: chainTimer, Work: chainWork}, 1, func(o int) {}) let mut h = NewTestHost(a, chainView, m, 4) catch { case BadPatch: return false } let before = m.Now() PressKey(&h, KeyPress{Name: "go", Mods: Mods{Shift: false, Ctrl: false, Alt: false, Meta: false}}) catch { case _: return false } Settle(&h) catch { case _: return false } let mut shown = false for s in Screen(&h) { shown = shown || s.Node == (TextNode{Value: "at \(last)", Style: Plain()}) } StateOf(&h).at == last && shown && m.Now() == before } }`,
	"ui PressIsOnePass":                `PressIsOnePass(n int, at int) { let m = time.NewManual(0) with context { Scheduler: m } { let small = (n%8+8)%8 + 2 let row = (at%small + small) % small match (pressCounts(small, row, m), pressCounts(4*small, row, m)) { case (Some[(StepCounts, StepCounts)] as x, Some[(StepCounts, StepCounts)] as y): let (p1, r1) = x.Value let (p4, r4) = y.Value p1.GroupsRan == 1 && p4.GroupsRan == 1 && p4.Ran-p1.Ran == (r4.Ran-p4.Ran)-(r1.Ran-p1.Ran) && p4.Ran > p1.Ran case _: false } } }`,
	"ui CountsAreTheStep":              `CountsAreTheStep(n int) { let m = time.NewManual(0) with context { Scheduler: m } { let a = actor.StartMachine(Machine[chain, int, int]{Initial: chain{at: 0, last: 1}, Step: chainStep, After: chainTimer, Work: chainWork}, 1, func(o int) {}) let mut h = NewTestHost(a, chainView, m, 4) catch { case BadPatch: return false } let mods = Mods{Shift: false, Ctrl: false, Alt: false, Meta: false} PressKey(&h, KeyPress{Name: "go", Mods: mods}) catch { case _: return false } let pressed = Counts(&h) Settle(&h) catch { case _: return false } let settled = Counts(&h) // Each Redraw adds its one pass of one node; a Settle, none. for _ in 0..(n%3+3)%3+1 { let ran = Counts(&h).Ran Redraw(&h) catch { case _: return false } Settle(&h) catch { case _: return false } if Counts(&h).Ran != ran+1 { return false } } PressKey(&h, KeyPress{Name: "noop", Mods: mods}) catch { case _: return false } if Counts(&h) != NoCounts() { return false } // PlayTrace prints that step as its line. let trace = PlayTrace(&h, []Act{KeyIn{Press: KeyPress{Name: "noop", Mods: mods}}}) catch { case _: return false } let line = "step 1 KeyIn \"noop\": no ops; ran 0 nodes, kept 0; groups ran 0, kept 0; visits 0" pressed.Set == 1 && pressed.Ran == 1 && settled == pressed && trace == []string{line} } }`,
	"ui AmbiguousActFails":             `AmbiguousActFails(seed int) { let m = time.NewManual(0) with context { Scheduler: m } { let k = (seed%4+4)%4 + 2 let view = func(f *Frame[int], s int) { Column(f, Plain()) { Text(f, "at \(s)") for i in 0..k { Pressable(f, "dup", i+1) { Text(f, "x") } Pressable(f, "one\(i)", 50+i) { Text(f, "y") } Input(f, "", "dupi", func(t string) int { 100 + i }) } } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: lastStep, After: lastTimer, Work: lastWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } let acts = []Act{TapOn{Label: "dup"}, ClickOn{At: WithRole{Role: Button, Name: "dup"}}, ClickOn{At: WithLabel{Label: "dup"}}, TypeInto{Label: "dupi", Text: "t"}, FillIn{At: WithPlaceholder{Text: "dupi"}, Text: "t"}} for act in acts { let mut count = -1 Play(&h, []Act{act}) catch { case Ambiguous as e: count = e.Count case _: return false } Settle(&h) catch { case _: return false } if count != k || StateOf(&h) != 0 { return false } } // A unique label acts, and First names the first match. Play(&h, []Act{TapOn{Label: "one1"}, SettleAll}) catch { case _: return false } if StateOf(&h) != 51 { return false } Play(&h, []Act{ClickOn{At: First{At: WithRole{Role: Button, Name: "dup"}}}, SettleAll}) catch { case _: return false } if StateOf(&h) != 1 { return false } Play(&h, []Act{FillIn{At: First{At: WithPlaceholder{Text: "dupi"}}, Text: "t"}, SettleAll}) catch { case _: return false } let firsts = Locate(&h, First{At: WithLabel{Label: "dup"}}) StateOf(&h) == 100 && len(firsts) == 1 && Find(&h, "dup") == (Some{Value: firsts[0]}) } }`,
	"ui AccessIsWhatWasDeclared":       `AccessIsWhatWasDeclared(seed int, count int) { let m = time.NewManual(0) with context { Scheduler: m } { let n = (count%6+6)%6 + 2 let view = func(f *Frame[int], s int) { let ds = semDecls(seed+s*7919, n) Column(f, Plain()) { for i, d in ds { if i%2 == 0 { Semantic(f, d, Some{Value: i + 1}, Plain()) { Text(f, "t\(i)") } } else { Fieldset(f, true, "fs") { Semantic(f, d, Some{Value: i + 1}, Plain()) { Text(f, "t\(i)") } } } } } } let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: semStep, After: semTimer, Work: semWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } for s in 0..2 { let ds = semDecls(seed+StateOf(&h)*7919, n) for i, d in ds { let ids = Locate(&h, WithLabel{Label: d.Name}) if len(ids) != 1 { return false } let mut want = d if i%2 == 1 { want.Disabled = true if d.Description == "" { want.Description = "fs" } } if AccessAt(&h, ids[0]) != want { return false } // The matchers of the new states read the same record. if semHolds(&h, d.Name, ToBeSelected) != (want.Selected == (Some{Value: true})) || semHolds(&h, d.Name, ToBeExpanded) != (want.Expanded == (Some{Value: true})) || semHolds(&h, d.Name, ToBeCollapsed) != (want.Expanded == (Some{Value: false})) || semHolds(&h, d.Name, ToBeInvalid) != want.Invalid { return false } } if s == 0 { // A press on the first: the signal when it is enabled. let first = Locate(&h, WithLabel{Label: "n0"}) Tap(&h, first[0]) catch { case _: return false } Settle(&h) catch { case _: return false } if ds[0].Disabled != (StateOf(&h) == 0) { return false } } } true } }`,
	"ui WithinFindsUnder":              `WithinFindsUnder(seed int) { let m = time.NewManual(0) with context { Scheduler: m } { let mut r = seeded(seed) let mut board = [][]int{} for _ in 0..draw(&r, 4)+1 { let mut col = []int{} for _ in 0..draw(&r, 5) { col = col.Append(draw(&r, 8)) } board = board.Append(col) } let none Option[int] = None let decl = func(role Role, name string, level Option[int]) Access { Access{Role: role, Name: name, Disabled: false, Description: "", Checked: None, Value: None, Selected: None, Expanded: None, Pressed: None, Invalid: false, Busy: false, HasPopup: false, Level: level} } let view = func(f *Frame[int], s int) { Column(f, Plain()) { for i, col in board { Semantic(f, decl(Region, "col \(i)", None), none, Plain()) { Semantic(f, decl(Heading, "col \(i)", Some{Value: 2}), none, Plain()) { Text(f, "col \(i)") } for j, c in col { if c%2 == 0 { Pressable(f, withinLabel(c/2), 100*i+j) { Text(f, "x") } } else { Box(f, Plain()) { Pressable(f, withinLabel(c/2), 100*i+j) { Text(f, "x") } } } } } } } } let a = actor.StartMachine(Machine[int, int, int]{Initial: -1, Step: semStep, After: semTimer, Work: semWork}, 1, func(o int) {}) let mut h = NewTestHost(a, view, m, 4) catch { case BadPatch: return false } for i, col in board { let column = WithRole{Role: Region, Name: "col \(i)"} let found = Locate(&h, Within{In: column, At: WithRole{Role: Button, Name: ""}}) if len(found) != len(col) { return false } for j, c in col { if AccessAt(&h, found[j]).Name != withinLabel(c/2) { return false } } let nested = Locate(&h, Within{In: WithRole{Role: Region, Name: ""}, At: Within{In: column, At: WithRole{Role: Button, Name: ""}}}) if nested != found { return false } for j, c in col { let label = withinLabel(c / 2) let mut first = j for k in 0..j { if withinLabel(col[k]/2) == label && first == j { first = k } } if first == j { Play(&h, []Act{ClickOn{At: First{At: Within{In: column, At: WithLabel{Label: label}}}}, SettleAll}) catch { case _: return false } if StateOf(&h) != 100*i+j { return false } } } } // A column is not within itself, nor within another: none nests. let regions = WithRole{Role: Region, Name: ""} len(Locate(&h, Within{In: WithRole{Role: Region, Name: "col 9"}, At: WithRole{Role: Button, Name: ""}})) == 0 && len(Locate(&h, Within{In: regions, At: regions})) == 0 } }`,
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
