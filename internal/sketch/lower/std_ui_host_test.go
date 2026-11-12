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

test UiScriptShowsTheView(seed int, steps int) {
	assert lawScriptShowsTheView(seed, steps)
}

test UiCancelledFetchStops(steps int) {
	assert lawCancelledFetchStops(steps)
}
`

// stdUIHostStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUIHostStatements = map[string]string{
	"ui ScriptShowsTheView":  `ScriptShowsTheView(seed int, steps int) { let m = time.NewManual(0) with context { Scheduler: m } { scriptShows(seed, steps, m) } }`,
	"ui CancelledFetchStops": `CancelledFetchStops(steps int) { let m = time.NewManual(0) with context { Scheduler: m } { cancelStops(steps, m) } }`,
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
