package lower

import (
	"fmt"
	"strings"
)

// Partly built values and owned loop elements in TestOwnershipRunModel's
// generator (genOwnRun), and so in TestStaticReleaseAgrees under both
// release lowerings (16a fix r1; resources R35, R43, Q7).
//
// An orAgg statement builds a value of parts left to right: a tuple (also
// nested, bound whole or by a pattern), a record bound by a pattern, the
// arguments of a call that takes both, or a tuple a callee returns. A part
// is a call that opens a resource, a literal, an owned local moved in, a
// try of a call that fails when n > cond, or a call that raises a defect
// when n > cond, at any position. A part made before a later part leaves
// is released on that edge, and once the value is built it is the value's
// alone (Info.PartHands). An orQLoop statement iterates a queue's Values()
// that main filled and ended: each element is the iteration's own, with a
// continue, a break, a return, a failure and a defect out of its body.
//
// Mutants it catches that nothing else does (the check and lower
// mutants.json, 16a-r1-*): a part left without an owner while a later
// part runs (a try, a call, a division), in a literal or a call's
// operands; a part handed to its value with its entry kept; a part
// registered after a later operand ran; an elided element unregistered;
// a held std take given a second owner; a Values binding unregistered.
//
// 16a fix r2 adds an orUpdate (a record update or spread whose base is a
// literal, a call, a moved local, a try or an update made in a frame of
// its own; a later value or key that leaves), a dot call's receiver
// ((P).keep2(P)) and an operation's taken receiver (sink(id, bad).Put(P)).
// Mutants (16a-r2-*): a receiver or a base left without an owner while a
// later operand runs; a base part not handed to its value; a field the
// value does not copy never released, released out of written order, or
// unregistered; a spread's dropped field kept; the static plan blind to
// such a field; a frame that owns only such a field given no scope.

const orPartsPrelude = `type Pair { R Res, S Res }

func openf(id int, bad bool, n int, c int) Res fails Bad needs Console {
	if n > c {
		return Bad{n: n}
	}
	open(id, bad)
}

func openb(id int, bad bool, n int, c int) Res needs Console {
	if n > c {
		Unreachable("boom")
	}
	open(id, bad)
}

func use2(t (Res, Res)) {}

func keep2(a Res, b Res) needs Console takes a, b {
	Console.Println("keep2 \(a.id) \(b.id)")
}

func give2(a int, b int, n int, c int) (Res, Res) fails Bad needs Console {
	return (open(a, false), try openf(b, false, n, c))
}

type Tag { Name string, R Res }

type Core { R Res }

type Big { ..Core, S (Res, Res), k int }

type Small { ..Core, k int }

type Sink {
	Put(r Res) takes,
	Close(),
}

type Snk { id int, bad bool }

func Close(s Snk) {
	if s.bad {
		Unreachable("sink \(s.id)")
	}
}

func Put(s Snk, r Res) takes s {}

func sink(id int, bad bool) Sink {
	Snk{id: id, bad: bad}
}

func mkp(a int, b int, x bool, y bool) Pair needs Console {
	Pair{R: open(a, x), S: open(b, y)}
}

func mkpf(a int, b int, x bool, y bool, n int, c int) Pair fails Bad needs Console {
	if n > c {
		return Bad{n: n}
	}
	mkp(a, b, x, y)
}

func mkpr(a int, b int, c int, y bool, z bool) Pair needs Console {
	Pair{..mkp(a, b, false, y), R: Res{id: c, bad: z}}
}

func mkt(id int, bad bool) Tag needs Console {
	Tag{Name: "t", R: open(id, bad)}
}

func mkbig(a int, b int, c int, x bool, y bool, z bool) Big needs Console {
	Big{R: open(a, x), S: (open(b, y), open(c, z)), k: 0}
}

func fkey(n int, c int) string fails Bad {
	if n > c {
		return Bad{n: n}
	}
	"k"
}

func bkey(n int, c int) string {
	if n > c {
		Unreachable("boom")
	}
	"k"
}

`

const orQueuePrelude = `func fill(q queue.Queue[Res], id int, bad bool) needs Console {
	q.Push(open(id, bad)) catch {
	case queue.Closed:
		Console.Println("closed")
	}
}

`

type aggForm uint8

const (
	aggWhole  aggForm = iota // let tV = (P, P); use2(tV)
	aggTuple                 // let (rA, (rB, rC)) = (P, (P, P)), nested
	aggRecord                // let Pair{R: rA, S: rB} = Pair{R: P, S: P}
	aggCall                  // keep2(P, P): the callee takes both
	aggGive                  // let (rA, rB) = try give2(…): a returned tuple
	aggAppend                // let sA = []Res{P}; let sB = sA.Append(P): a held std take
	aggUpdate                // a record update or spread whose ..base is a part (orUpdate)
	aggSink                  // sink(id, bad).Put(P): an operation's taken receiver, then its argument
)

// An orUpdate is a record update or spread (16a fix r2): its base is
// evaluated first, then its named values; the value built takes the
// fields it copies, and each field of the base it does not copy (one it
// names, or one a spread's type lacks) is the statement's, released at the
// statement's end in written order (Info.Replaced).
type upTarget uint8

const (
	upPair   upTarget = iota // let Pair{R: rA, S: rB} = Pair{..BASE, F: P, …}
	upTag                    // let Tag{Name: _, R: rA} = Tag{..BASE, Name: KEY}
	upSpread                 // let Small{R: rA, k: _} = Small{..BIG, k: 1}: Big's S, a tuple (a generated release), is dropped
)

// nres is the count of resources a base of t holds: its opens.
func (t upTarget) nres() int {
	return [...]int{upPair: 2, upTag: 1, upSpread: 3}[t]
}

type baseKind uint8

const (
	bLit  baseKind = iota // Pair{R: P, S: P} (Tag{Name: "x", R: P}, Big{R: P, S: (P, P), k: 0})
	bCall                 // mkp(a, b, x, y) (mkt, mkbig)
	bVar                  // pK, bound just before: let pK = <bLit>
	bTry                  // try mkpf(a, b, x, y, n, cond): fails when n > cond (Pair only)
	bUpd                  // mkpr(a, b, c, y, z): an update made in a frame that owns only the field it replaces (Pair only)
)

type keyKind uint8

const (
	kLit  keyKind = iota // "k"
	kTry                 // try fkey(n, cond)
	kBoom                // bkey(n, cond): a defect when n > cond
)

type orUpdate struct {
	target upTarget
	base   baseKind
	bparts []*orPart // bLit, bVar: the base's resource fields, in field order
	ids    [3]int    // bCall, bTry: the base's ids, nres of them
	bads   [3]bool
	cond   int   // bTry
	pk     int   // bVar: the local
	fields []int // upPair: the named fields (0 R, 1 S), in written order
	vals   []*orPart
	key    keyKind // upTag
	kcond  int
	vs     [2]int // the bindings (upTag, upSpread: vs[0])
}

// update generates an orUpdate; flat draws a part.
func (g *orGen) update(flat func() *orPart) *orUpdate {
	u := &orUpdate{target: upTarget(g.r.IntN(3)), base: baseKind(g.r.IntN(3))}
	if u.target == upPair && g.r.IntN(3) == 0 {
		u.base = []baseKind{bTry, bUpd}[g.r.IntN(2)]
		u.cond = g.r.IntN(9)
	}
	nres := u.target.nres()
	if u.base == bUpd {
		nres = 3 // the old R (never bad: its release is mkpr's), S, the new R
	}
	switch u.base {
	case bLit, bVar:
		for i := 0; i < nres; i++ {
			u.bparts = append(u.bparts, flat())
		}
		if u.target == upSpread {
			// Big's S is a tuple of the last two.
			u.bparts = []*orPart{u.bparts[0], {kind: pTuple, elems: u.bparts[1:]}}
		}
		if u.base == bVar {
			u.pk = g.nextV
			g.nextV++
		}
	default:
		for i := 0; i < nres; i++ {
			u.ids[i], u.bads[i] = g.id(), g.bad()
		}
		if u.base == bUpd {
			u.bads[0] = false
		}
	}
	switch u.target {
	case upPair:
		// {1, 0} twice: a written order apart from the declaration's.
		u.fields = [][]int{{0}, {1}, {0, 1}, {1, 0}, {1, 0}}[g.r.IntN(5)]
		leave := g.r.IntN(3) == 0 // the last value leaves (n is known), after the base was made
		for i := range u.fields {
			if leave && i == len(u.fields)-1 {
				p := []*orPart{{kind: pTry, cond: g.n - 1}, {kind: pBoom, cond: g.n - 1}, {kind: pDiv, cond: g.n}}[g.r.IntN(3)]
				p.id = g.id()
				u.vals = append(u.vals, p)
				continue
			}
			u.vals = append(u.vals, flat())
		}
		u.vs = [2]int{g.declare(false), g.declare(false)}
	case upTag:
		u.key = keyKind(g.r.IntN(3))
		u.kcond = g.r.IntN(9)
		if u.key != kLit && g.r.IntN(2) == 0 {
			u.kcond = g.n - 1 // the key leaves, after the base was made
		}
		u.vs[0] = g.declare(false)
	case upSpread:
		u.vs[0] = g.declare(false)
	}
	return u
}

func (u *orUpdate) write(b *strings.Builder, ind string) {
	texts := func(ps []*orPart) []string {
		var xs []string
		for _, p := range ps {
			xs = append(xs, p.text())
		}
		return xs
	}
	var lit, call string
	switch u.target {
	case upPair:
		if u.bparts != nil {
			x := texts(u.bparts)
			lit = fmt.Sprintf("Pair{R: %s, S: %s}", x[0], x[1])
		}
		call = fmt.Sprintf("mkp(%d, %d, %v, %v)", u.ids[0], u.ids[1], u.bads[0], u.bads[1])
		switch u.base {
		case bTry:
			call = fmt.Sprintf("(try mkpf(%d, %d, %v, %v, n, %d))", u.ids[0], u.ids[1], u.bads[0], u.bads[1], u.cond)
		case bUpd:
			call = fmt.Sprintf("mkpr(%d, %d, %d, %v, %v)", u.ids[0], u.ids[1], u.ids[2], u.bads[1], u.bads[2])
		}
	case upTag:
		if u.bparts != nil {
			lit = fmt.Sprintf("Tag{Name: \"x\", R: %s}", texts(u.bparts)[0])
		}
		call = fmt.Sprintf("mkt(%d, %v)", u.ids[0], u.bads[0])
	case upSpread:
		if u.bparts != nil {
			x := texts(u.bparts)
			lit = fmt.Sprintf("Big{R: %s, S: %s, k: 0}", x[0], x[1])
		}
		call = fmt.Sprintf("mkbig(%d, %d, %d, %v, %v, %v)", u.ids[0], u.ids[1], u.ids[2], u.bads[0], u.bads[1], u.bads[2])
	}
	base := call
	switch u.base {
	case bLit:
		base = lit
	case bVar:
		fmt.Fprintf(b, "%slet p%d = %s\n", ind, u.pk, lit)
		base = fmt.Sprintf("p%d", u.pk)
	}
	switch u.target {
	case upPair:
		var named []string
		vals := texts(u.vals)
		for i, f := range u.fields {
			named = append(named, []string{"R", "S"}[f]+": "+vals[i])
		}
		fmt.Fprintf(b, "%slet Pair{R: r%d, S: r%d} = Pair{..%s, %s}\n%suse(r%d)\n%suse(r%d)\n", ind, u.vs[0], u.vs[1], base, strings.Join(named, ", "), ind, u.vs[0], ind, u.vs[1])
	case upTag:
		key := `"k"`
		switch u.key {
		case kTry:
			key = fmt.Sprintf("try fkey(n, %d)", u.kcond)
		case kBoom:
			key = fmt.Sprintf("bkey(n, %d)", u.kcond)
		}
		fmt.Fprintf(b, "%slet Tag{Name: _, R: r%d} = Tag{..%s, Name: %s}\n%suse(r%d)\n", ind, u.vs[0], base, key, ind, u.vs[0])
	case upSpread:
		fmt.Fprintf(b, "%slet Small{R: r%d, k: _} = Small{..%s, k: 1}\n%suse(r%d)\n", ind, u.vs[0], base, ind, u.vs[0])
	}
}

// update runs an orUpdate in scope sc; eval evaluates one part.
func (m *orModel) update(u *orUpdate, sc int, eval func(p *orPart) (*orEntry, orExit)) orExit {
	evalAll := func(ps []*orPart) ([]*orEntry, orExit) {
		var es []*orEntry
		for _, p := range ps {
			e, x := eval(p)
			if x != exNormal {
				return nil, x
			}
			es = append(es, e)
		}
		for _, e := range es {
			m.remove(e)
		}
		return es, exNormal
	}
	var base *orEntry
	switch u.base {
	case bLit, bVar:
		es, x := evalAll(u.bparts)
		if x != exNormal {
			return x
		}
		if u.base == bVar {
			// let pK = …: its scope owns it; the update moves it.
			pk := &orEntry{scope: sc, parts: es}
			m.push(pk)
			m.remove(pk)
		}
		base = &orEntry{scope: -1, parts: es}
	default:
		if u.base == bTry && m.n > u.cond {
			return exFail
		}
		var es []*orEntry
		if u.base == bUpd {
			// mkpr's frame: mkp opens the old R and S, the update makes the
			// new R, and the old R, the statement's, closes at its end.
			m.say("open %d", u.ids[0])
			m.say("open %d", u.ids[1])
			m.say("close %d", u.ids[0])
			base = &orEntry{scope: -1, parts: []*orEntry{{id: u.ids[2], bad: u.bads[2], scope: -1}, {id: u.ids[1], bad: u.bads[1], scope: -1}}}
			break
		}
		for i := range u.target.nres() {
			m.say("open %d", u.ids[i])
			es = append(es, &orEntry{id: u.ids[i], bad: u.bads[i], scope: -1})
		}
		if u.target == upSpread {
			es = []*orEntry{es[0], {scope: -1, parts: es[1:]}}
		}
		base = &orEntry{scope: -1, parts: es}
	}
	m.push(base)
	var vals []*orEntry
	switch u.target {
	case upPair:
		for _, p := range u.vals {
			e, x := eval(p)
			if x != exNormal {
				return x
			}
			vals = append(vals, e)
		}
	case upTag:
		if u.key != kLit && m.n > u.kcond {
			if u.key == kTry {
				return exFail
			}
			return exDefect
		}
	}
	// The value is built: it takes what it copies; the rest of the base
	// is the statement's.
	m.remove(base)
	for _, e := range vals {
		m.remove(e)
	}
	leaves := append([]*orEntry(nil), base.parts...)
	var dropped []*orEntry
	switch u.target {
	case upPair:
		for i, f := range u.fields {
			dropped = append(dropped, base.parts[f])
			leaves[f] = vals[i]
		}
	case upSpread:
		dropped = []*orEntry{base.parts[1]}
		leaves = leaves[:1]
	}
	for i := len(dropped) - 1; i >= 0; i-- {
		dropped[i].scope = -1
		m.push(dropped[i])
	}
	for i, e := range leaves {
		m.acquire(u.vs[i], e.id, e.bad, sc)
	}
	for _, e := range dropped {
		m.remove(e)
		if m.release(e) {
			return exDefect
		}
	}
	for _, e := range leaves {
		m.say("use %d", e.id)
	}
	return exNormal
}

type partKind uint8

const (
	pOpen  partKind = iota // open(id, bad)
	pLit                   // Res{id: id, bad: bad}
	pVar                   // rW, an owned local moved in
	pTry                   // try openf(id, bad, n, cond): fails when n > cond
	pBoom                  // openb(id, bad, n, cond): a defect when n > cond
	pDiv                   // Res{id: id + 0 / (n - cond), …}: a defect, with no call, when n == cond
	pTuple                 // (P, P)
)

type orPart struct {
	kind  partKind
	id    int
	bad   bool
	w     int // pVar: the local moved in
	v     int // a pattern's binding of this leaf
	cond  int
	elems []*orPart
}

type orAggStmt struct {
	form  aggForm
	parts []*orPart
	t     int    // aggWhole: the tuple's local
	ids   [2]int // aggGive
	vs    [2]int // aggGive: the bindings
	cond  int    // aggGive
	recv  bool   // aggCall: written (P).keep2(P), the first part the receiver
	up    *orUpdate
	sid   int  // aggSink: the sink's id
	sbad  bool // aggSink: its Close raises a defect
}

// aggregate generates an orAgg statement: its parts, then its bindings.
func (g *orGen) aggregate() *orAggStmt {
	// An update has the most shapes (target, base, named fields), so it
	// is drawn three times as often, and a call (two writings) and a held
	// take (its local moved in last, rarely) twice.
	forms := []aggForm{aggWhole, aggTuple, aggRecord, aggCall, aggCall, aggGive, aggAppend, aggAppend, aggUpdate, aggUpdate, aggUpdate, aggSink}
	a := &orAggStmt{form: forms[g.r.IntN(len(forms))]}
	taken := map[int]bool{}
	noVar := false // aggSink: its argument is lent, so no local is moved in
	flat := func() *orPart {
		for {
			switch k := partKind(g.r.IntN(6)); k {
			case pDiv:
				return &orPart{kind: pDiv, id: g.id(), bad: g.bad(), cond: 1 + g.r.IntN(4)}
			case pOpen, pLit:
				return &orPart{kind: k, id: g.id(), bad: g.bad()}
			case pVar:
				var free []int
				for _, w := range g.owned() {
					if !taken[w] {
						free = append(free, w)
					}
				}
				if len(free) == 0 || noVar {
					continue
				}
				w := free[g.r.IntN(len(free))]
				taken[w] = true
				return &orPart{kind: pVar, w: w}
			case pTry:
				return &orPart{kind: pTry, id: g.id(), bad: g.bad(), cond: g.r.IntN(9)} // fails less often than orFail, so later statements run
			case pBoom:
				if g.r.IntN(2) == 0 {
					continue
				}
				return &orPart{kind: pBoom, id: g.id(), bad: g.bad(), cond: g.r.IntN(9)}
			}
		}
	}
	switch a.form {
	case aggGive:
		a.ids = [2]int{g.id(), g.id()}
		a.cond = g.r.IntN(5)
		a.vs = [2]int{g.declare(false), g.declare(false)}
		return a
	case aggUpdate:
		a.up = g.update(flat)
		for w := range taken {
			g.state[w] = 1
		}
		return a
	case aggSink:
		// The receiver's release shows only as its defect, and only an
		// argument that leaves tells an owned receiver from a lost one:
		// so the sink is bad, and the argument fails (a typed failure, which
		// the defect replaces) two times in three.
		noVar = true
		a.sid, a.sbad = g.id(), true
		a.parts = []*orPart{flat()}
		if g.r.IntN(3) != 0 {
			a.parts[0] = &orPart{kind: pTry, id: g.id(), cond: g.n - 1}
		}
		return a
	case aggCall:
		a.recv = g.r.IntN(4) != 0
		a.parts = []*orPart{flat(), flat()}
	case aggTuple:
		for i := 2 + g.r.IntN(2); i > 0; i-- {
			if g.r.IntN(3) == 0 {
				a.parts = append(a.parts, &orPart{kind: pTuple, elems: []*orPart{flat(), flat()}})
			} else {
				a.parts = append(a.parts, flat())
			}
		}
	case aggAppend:
		a.parts = []*orPart{flat(), flat(), flat()}
	default:
		a.parts = []*orPart{flat(), flat()}
	}
	if a.recv || g.r.IntN(3) == 0 {
		// The last part leaves (n is known): every part before it has
		// been made and is released on that edge (a receiver: always).
		last := &a.parts[len(a.parts)-1]
		if (*last).kind == pVar {
			delete(taken, (*last).w)
		}
		switch g.r.IntN(3) {
		case 0:
			*last = &orPart{kind: pTry, id: g.id(), cond: g.n - 1}
		case 1:
			*last = &orPart{kind: pBoom, id: g.id(), cond: g.n - 1}
		default:
			*last = &orPart{kind: pDiv, id: g.id(), cond: g.n}
		}
	}
	for w := range taken {
		g.state[w] = 1
	}
	switch a.form {
	case aggWhole:
		a.t = g.nextV
		g.nextV++
	case aggAppend:
		a.t = g.nextV
		g.nextV += 2
	case aggTuple, aggRecord:
		var bind func(p *orPart)
		bind = func(p *orPart) {
			if p.kind == pTuple {
				for _, e := range p.elems {
					bind(e)
				}
				return
			}
			p.v = g.declare(false)
		}
		for _, p := range a.parts {
			bind(p)
		}
	}
	return a
}

func (p *orPart) text() string {
	switch p.kind {
	case pOpen:
		return fmt.Sprintf("open(%d, %v)", p.id, p.bad)
	case pLit:
		return fmt.Sprintf("Res{id: %d, bad: %v}", p.id, p.bad)
	case pVar:
		return fmt.Sprintf("r%d", p.w)
	case pTry:
		return fmt.Sprintf("try openf(%d, %v, n, %d)", p.id, p.bad, p.cond)
	case pBoom:
		return fmt.Sprintf("openb(%d, %v, n, %d)", p.id, p.bad, p.cond)
	case pDiv:
		return fmt.Sprintf("Res{id: %d + 0 / (n - %d), bad: %v}", p.id, p.cond, p.bad)
	}
	var xs []string
	for _, e := range p.elems {
		xs = append(xs, e.text())
	}
	return "(" + strings.Join(xs, ", ") + ")"
}

// pattern is the tuple pattern that binds p's leaves.
func (p *orPart) pattern() string {
	if p.kind != pTuple {
		return fmt.Sprintf("r%d", p.v)
	}
	var xs []string
	for _, e := range p.elems {
		xs = append(xs, e.pattern())
	}
	return "(" + strings.Join(xs, ", ") + ")"
}

// leaves are p's bindings in written order.
func (p *orPart) leaves(into []int) []int {
	if p.kind != pTuple {
		return append(into, p.v)
	}
	for _, e := range p.elems {
		into = e.leaves(into)
	}
	return into
}

func (a *orAggStmt) write(b *strings.Builder, ind string) {
	texts := func() []string {
		var xs []string
		for _, p := range a.parts {
			xs = append(xs, p.text())
		}
		return xs
	}
	var bound []int
	switch a.form {
	case aggWhole:
		fmt.Fprintf(b, "%slet t%d = (%s)\n%suse2(t%d)\n", ind, a.t, strings.Join(texts(), ", "), ind, a.t)
	case aggTuple:
		root := &orPart{kind: pTuple, elems: a.parts}
		fmt.Fprintf(b, "%slet %s = (%s)\n", ind, root.pattern(), strings.Join(texts(), ", "))
		bound = root.leaves(nil)
	case aggRecord:
		x := texts()
		fmt.Fprintf(b, "%slet Pair{R: r%d, S: r%d} = Pair{R: %s, S: %s}\n", ind, a.parts[0].v, a.parts[1].v, x[0], x[1])
		bound = []int{a.parts[0].v, a.parts[1].v}
	case aggCall:
		if a.recv {
			x := texts()
			fmt.Fprintf(b, "%s(%s).keep2(%s)\n", ind, x[0], x[1])
		} else {
			fmt.Fprintf(b, "%skeep2(%s)\n", ind, strings.Join(texts(), ", "))
		}
	case aggSink:
		fmt.Fprintf(b, "%ssink(%d, %v).Put(%s)\n", ind, a.sid, a.sbad, texts()[0])
	case aggUpdate:
		a.up.write(b, ind)
	case aggAppend:
		x := texts()
		for i, p := range a.parts[:2] {
			if p.kind == pLit {
				x[i] = strings.TrimPrefix(x[i], "Res") // an elided literal
			}
		}
		fmt.Fprintf(b, "%slet s%d = []Res{%s, %s}\n%slet s%d = s%d.Append(%s)\n", ind, a.t, x[0], x[1], ind, a.t+1, a.t, x[2])
	case aggGive:
		fmt.Fprintf(b, "%slet (r%d, r%d) = try give2(%d, %d, n, %d)\n", ind, a.vs[0], a.vs[1], a.ids[0], a.ids[1], a.cond)
		bound = a.vs[:]
	}
	for _, v := range bound {
		fmt.Fprintf(b, "%suse(r%d)\n", ind, v) // a pattern's bindings are used (S110)
	}
}

// aggregate runs an orAgg statement in scope sc.
func (m *orModel) aggregate(a *orAggStmt, sc int) orExit {
	if a.form == aggGive {
		m.say("open %d", a.ids[0])
		if m.n > a.cond {
			// give2's frame releases its operand; the failure leaves run.
			m.say("close %d", a.ids[0])
			return exFail
		}
		m.say("open %d", a.ids[1])
		for i, v := range a.vs {
			m.acquire(v, a.ids[i], false, sc)
		}
		for _, id := range a.ids {
			m.say("use %d", id)
		}
		return exNormal
	}
	// Each part is the statement's (a temporary on the frame list) until
	// the value is built.
	var eval func(p *orPart) (*orEntry, orExit)
	eval = func(p *orPart) (*orEntry, orExit) {
		var e *orEntry
		switch p.kind {
		case pOpen:
			m.say("open %d", p.id)
			e = &orEntry{id: p.id, bad: p.bad, scope: -1}
		case pLit:
			e = &orEntry{id: p.id, bad: p.bad, scope: -1}
		case pVar:
			old := m.vars[p.w]
			m.vars[p.w] = nil
			m.remove(old)
			e = &orEntry{id: old.id, bad: old.bad, scope: -1, parts: old.parts}
		case pDiv:
			if m.n == p.cond {
				return nil, exDefect // an integer division by zero
			}
			e = &orEntry{id: p.id, bad: p.bad, scope: -1}
		case pTry, pBoom:
			if m.n > p.cond {
				if p.kind == pTry {
					return nil, exFail
				}
				return nil, exDefect
			}
			m.say("open %d", p.id)
			e = &orEntry{id: p.id, bad: p.bad, scope: -1}
		case pTuple:
			var es []*orEntry
			for _, x := range p.elems {
				pe, ex := eval(x)
				if ex != exNormal {
					return nil, ex
				}
				es = append(es, pe)
			}
			for _, pe := range es {
				m.remove(pe)
			}
			e = &orEntry{scope: -1, parts: es}
		}
		m.push(e)
		return e, exNormal
	}
	switch a.form {
	case aggUpdate:
		return m.update(a.up, sc, eval)
	case aggSink:
		// The receiver is a part while the argument runs; Put takes it
		// and its frame releases it (a defect when sbad, with no line);
		// the argument is lent, the statement's temporary.
		recv := &orEntry{id: a.sid, bad: a.sbad, silent: true, scope: -1}
		m.push(recv)
		arg, x := eval(a.parts[0])
		if x != exNormal {
			return x
		}
		m.remove(recv)
		if m.release(recv) {
			return exDefect
		}
		m.remove(arg)
		if m.release(arg) {
			return exDefect
		}
		return exNormal
	}
	if a.form == aggAppend {
		// sA owns the first two; Append's entry for sA stays until the
		// call returns, while its other operand may still leave.
		var es []*orEntry
		for _, p := range a.parts[:2] {
			e, x := eval(p)
			if x != exNormal {
				return x
			}
			es = append(es, e)
		}
		for _, e := range es {
			m.remove(e)
		}
		s1 := &orEntry{scope: sc, parts: es}
		m.push(s1)
		e2, x := eval(a.parts[2])
		if x != exNormal {
			return x
		}
		m.remove(e2)
		m.remove(s1)
		m.push(&orEntry{scope: sc, parts: append(es, e2)})
		return exNormal
	}
	var tops []*orEntry
	for _, p := range a.parts {
		e, x := eval(p)
		if x != exNormal {
			return x
		}
		tops = append(tops, e)
	}
	for _, e := range tops {
		m.remove(e) // the value is built: its parts are its own
	}
	switch a.form {
	case aggWhole:
		m.push(&orEntry{scope: sc, parts: tops})
	case aggTuple, aggRecord:
		var leaves []*orEntry
		var flatten func(e *orEntry)
		flatten = func(e *orEntry) {
			if e.parts != nil {
				for _, x := range e.parts {
					flatten(x)
				}
				return
			}
			leaves = append(leaves, e)
		}
		var vs []int
		for i, e := range tops {
			flatten(e)
			vs = a.parts[i].leaves(vs)
		}
		for i, v := range vs {
			m.acquire(v, leaves[i].id, leaves[i].bad, sc)
		}
		for _, e := range leaves {
			m.say("use %d", e.id)
		}
	case aggCall:
		m.say("keep2 %d %d", tops[0].id, tops[1].id)
		// keep2's frame releases what it took, last first.
		b1 := m.release(tops[1])
		b0 := m.release(tops[0])
		if b1 || b0 {
			return exDefect
		}
	}
	return exNormal
}

// queueLoop generates an orQLoop: its queue's elements, its continue and
// break points, and a body that sees only its own element (as orFor's).
func (g *orGen) queueLoop() orStmt {
	s := orStmt{kind: orQLoop, breakAt: -1, contAt: -1}
	for i := 1 + g.r.IntN(3); i > 0; i-- {
		s.elems = append(s.elems, orEntry{id: g.id(), bad: g.bad()})
	}
	if g.r.IntN(2) == 0 {
		s.breakAt = g.r.IntN(len(s.elems))
	}
	if g.r.IntN(2) == 0 {
		s.contAt = g.r.IntN(len(s.elems))
	}
	saved := g.scopes
	g.scopes = [][]int{nil}
	g.depth++
	s.v = g.declare(false)
	for i := 1 + g.r.IntN(3); i > 0; i-- {
		s.body = append(s.body, g.stmt())
	}
	g.depth--
	g.scopes = saved
	s.q = len(g.queues)
	g.queues = append(g.queues, s)
	return s
}

func orWriteQueueLoop(b *strings.Builder, s orStmt, depth int, muts map[int]bool) {
	ind := strings.Repeat("\t", depth)
	fmt.Fprintf(b, "%sfor r%d in q%d.Values() {\n", ind, s.v, s.q)
	fmt.Fprintf(b, "%s\tuse(r%d)\n", ind, s.v)
	if s.contAt >= 0 {
		fmt.Fprintf(b, "%s\tif r%d.id == %d {\n%s\t\tcontinue\n%s\t}\n", ind, s.v, s.elems[s.contAt].id, ind, ind)
	}
	if s.breakAt >= 0 {
		fmt.Fprintf(b, "%s\tif r%d.id == %d {\n%s\t\tbreak\n%s\t}\n", ind, s.v, s.elems[s.breakAt].id, ind, ind)
	}
	orWrite(b, s.body, depth+1, muts)
	fmt.Fprintf(b, "%s}\n", ind)
}

// queueLoop runs an orQLoop: each element popped is the iteration's own,
// released at the iteration's end, a continue or a break, or by the
// frame's End when the body leaves it.
func (m *orModel) queueLoop(s orStmt) orExit {
	for i := 0; len(m.left[s.q]) > 0; i++ {
		e := m.left[s.q][0]
		m.left[s.q] = m.left[s.q][1:]
		m.nextSc++
		isc := m.nextSc
		m.acquire(s.v, e.id, e.bad, isc)
		m.say("use %d", e.id)
		if i == s.contAt {
			if m.endScope(isc) {
				return exDefect
			}
			continue
		}
		if i == s.breakAt {
			if m.endScope(isc) {
				return exDefect
			}
			break
		}
		if x := m.stmts(s.body, isc); x != exNormal {
			return x
		}
		if m.endScope(isc) {
			return exDefect
		}
	}
	return exNormal
}
