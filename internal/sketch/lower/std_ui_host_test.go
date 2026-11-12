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
		ui.Play(&h, []ui.Act{ui.TapOn{Label: label}}) catch {
		case _:
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
`

// stdUIHostStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUIHostStatements = map[string]string{
	"ui ScriptShowsTheView":  `ScriptShowsTheView(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { scriptShows(seed, steps, m) } }`,
	"ui CancelledFetchStops": `CancelledFetchStops(steps int) { let m = time.NewManual(0) with context { Scheduler: m } { cancelStops(steps, m) } }`,
	"ui NoTickWhenSettled":   `NoTickWhenSettled(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { ticksFollow(seed, steps, m) } }`,
	"ui InputGivesSignal":    `InputGivesSignal(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { inputFollows(seed, steps, m) } }`,
	"ui PaintMatchesList":    `PaintMatchesList(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { paintFollows(seed, steps, m) } }`,
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
