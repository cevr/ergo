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

test UiStateKeeps(seed int, steps int) {
	assert lawStateKeeps(seed, steps)
}

test UiFreshMountInits(seed int, steps int) {
	assert lawFreshMountInits(seed, steps)
}

test UiLoadFollowsModel(seed int, steps int) {
	assert lawLoadFollowsModel(seed, steps)
}
`

// stdUILoadStatements is the statement of each ui law, as the ledger writes it,
// by ledger key.
var stdUILoadStatements = map[string]string{
	"ui StateKeeps":       `StateKeeps(seed int, steps int) { cellsFollowModel(seed, steps, false) }`,
	"ui FreshMountInits":  `FreshMountInits(seed int, steps int) { cellsFollowModel(seed, steps, true) }`,
	"ui LoadFollowsModel": `LoadFollowsModel(seed int, steps int) { let mut r = seeded(seed) let mut f = NewFrame[int]() let mut cur Option[loaded] = None let mut flight = []fetching{} let mut used = Map[int, bool]{} let mut ready = -1 for _ in 0..(steps%24+24)%24+2 { if draw(&r, 2) == 0 || len(flight) == 0 { let mut w = want{at: Nowhere, key: draw(&r, 3)} match draw(&r, 3) { case 0: case 1: w.at = Top case _: w.at = Boxed } let _ = Render(&f, loadView, w) let mut fetches = []Fetch[int]{} let mut cancels = []int{} for j in Jobs(&f) { match j { case Fetch[int] as x: fetches = fetches.Append(x) case Cancel as c: cancels = cancels.Append(c.Epoch) } } let mut wantCancel = -1 let mut wantFetch = false match cur { case Some[loaded] as c: if c.Value.at != w.at || c.Value.key != w.key { wantCancel = c.Value.epoch wantFetch = w.at != Nowhere } case None: wantFetch = w.at != Nowhere } if wantCancel >= 0 { if len(cancels) != 1 || cancels[0] != wantCancel { return false } cur = None ready = -1 } else if len(cancels) != 0 { return false } if wantFetch { if len(fetches) != 1 || used.Get(fetches[0].Epoch) != None { return false } used[fetches[0].Epoch] = true cur = Some{Value: loaded{at: w.at, key: w.key, epoch: fetches[0].Epoch}} flight = flight.Append(fetching{epoch: fetches[0].Epoch, run: fetches[0].Run}) } else if len(fetches) != 0 { return false } } else { let i = draw(&r, len(flight)) let x = flight[i] let mut rest = []fetching{} for j, y in flight { if j != i { rest = rest.Append(y) } } flight = rest let mut wanted = false match cur { case Some[loaded] as c: wanted = c.Value.epoch == x.epoch case None: } let got = sent(Handle(&f, Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal) if Arrive(&f, x.epoch) != wanted || (got != -1) != wanted { return false } if wanted { ready = got } } } for x in flight { let got = sent(Handle(&f, Arrived[int]{Epoch: x.epoch, Signal: x.run()}).Signal) if got != -1 { ready = got } } match cur { case Some[loaded] as c: ready == c.Value.key case None: ready == -1 } }`,
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
