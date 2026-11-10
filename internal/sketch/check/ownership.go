package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Ownership flow (#197 L8 A2; resources R4, R5, R10, R15-R17, R19-R21,
// R41-R43). A value whose type holds a resource (resource.go) has one
// owner. One forward pass per frame over the structured control flow
// decides, for each owned local, taken parameter and temporary, whether
// it is owned, handed off (moved) or handed off on some paths only
// (maybe), and reports:
//
//  1. a use after a hand-off, or of a value handed off on some path (R15,
//     R19, R20, R21): the use names the hand-off;
//  2. a hand-off of a lent value: a parameter the function does not take
//     (R10, R16, R17), or a local of an enclosing frame inside a function
//     literal or a trailing block (scope.OnExit, task.Go), which may run
//     any number of times;
//  3. a lend and a hand-off of one value in one call (R17);
//  4. a discarded resource: a call or literal whose resource value no one
//     owns, as a statement or bound to _ (R42, D4).
//
// A hand-off is: `let q = r`, an assignment `x = r`, `return r` or the
// block's value, an argument for a parameter the callee's written takes
// clause names, the value an operation with a bare takes is called on,
// and the receiver of a checked Close (R1, R19). Every other use lends
// (R16). The takes clause of an unexported function is inferred in A3;
// here a parameter is taken only when the clause is written.
//
// The pass records the release plan in Info.Releases: at each scope's end,
// each statement's end (its temporaries, R43), each exit edge (return,
// break, continue, a try's failure that leaves the function) and each
// assignment (the old value, R41), what is still owned there, last
// acquired first, with a drop flag for a value owned on some paths only.
//
// A loop body runs to its fixpoint: the lattice per value is owned,
// moved < maybe, so a body runs at most twice per change of its entry
// state, and each loop memoizes its last fixpoint (entry, exit state,
// diagnostics), so an enclosing loop's later pass reuses it and nesting
// does not multiply the work. Diagnostics of a pass that is not the last
// are dropped; a later pass overwrites its releases. ownSteps counts
// visited nodes and copied state slots.
//
// Prior art: Rust's move checker and drop elaboration (drop flags,
// assignment drops the old value, a temporary lives to the statement's
// end); Lean 4's owned and borrowed parameters; Swift's consuming and
// borrowing parameters. Gleam, Elixir and ReScript have no static
// ownership: the BEAM closes a port when its process ends.

type ownSt uint8

const (
	ownNone  ownSt = iota // not live here
	ownOwned              // owned on every path
	ownMoved              // handed off on every path
	ownMaybe              // handed off on some paths
)

// ownSlot is one value's state at a program point.
type ownSlot struct {
	st    ownSt
	at    syntax.Node // the hand-off (moved, maybe)
	how   string
	loans []ownLoan // result origins still lent to calls being evaluated
}

type ownLoanCall struct {
	name   string
	active bool
}

type ownLoan struct {
	call *ownLoanCall
	id   *syntax.Ident
}

// liveLoans unions immutable path facts. A completed or aborted call no
// longer lends its operands, including in saved failure-edge states.
func liveLoans(lists ...[]ownLoan) []ownLoan {
	var out []ownLoan
	for _, list := range lists {
		for _, l := range list {
			if !l.call.active {
				continue
			}
			found := false
			for _, old := range out {
				found = found || old == l
			}
			if !found {
				out = append(out, l)
			}
		}
	}
	return out
}

// ownLocal is what a slot holds: a local, a parameter or a temporary.
type ownLocal struct {
	v     *Var
	temp  syntax.Expr
	lent  bool // a parameter the function does not take
	depth int  // the frame that owns it
}

type ownScope struct {
	key   syntax.Node
	first int
}

type ownLoop struct {
	first        int // slots below belong to the loop's enclosing scopes
	breaks, cont [][]ownSlot
}

type ownCatch struct {
	first int
	fails [][]ownSlot
}

// ownFrameSave is the walk state a nested frame replaces.
type ownFrameSave struct {
	loops   []*ownLoop
	catches []*ownCatch
	first   int
	name    string
	nested  string
	tail    ownTail
}

type ownDiag struct {
	at  syntax.Node
	msg string
}

type ownWalk struct {
	c     *checker
	info  *Info
	st    []ownSlot // nil: unreachable
	vars  []ownLocal
	slot  map[*Var]int
	depth int
	// first is the frame's first slot; name its function's name; nested
	// the text naming a nested frame ("" for a declaration).
	first        int
	name, nested string
	scopes       []ownScope
	loops        []*ownLoop
	catches      []*ownCatch
	loopMemo     map[syntax.Node]*ownLoopMemo
	diags        []ownDiag
	// tail is where the value of the block being walked goes (ownTail).
	tail ownTail
}

// checkOwnership runs the flow pass over every body of the package.
func (c *checker) checkOwnership() {
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, fi := range c.files {
		c.cur = fi
		for _, d := range fi.syntax.Decls {
			w := &ownWalk{c: c, info: c.pkg.Info, slot: map[*Var]int{}, loopMemo: map[syntax.Node]*ownLoopMemo{}}
			switch d := d.(type) {
			case *syntax.FuncDecl:
				if d.Body == nil || d.Extern != nil {
					continue
				}
				f, _ := c.pkg.Info.Defs[d.Name].(*Func)
				if f == nil || f.Sig == nil {
					continue
				}
				w.frame(d.Body, f.Sig, w.paramVars(d.Recv, d.Type.Params), d.Name.Name, "", f.Name == "Close" && c.pkg.Info.Closes[d] != nil)
			case *syntax.TestDecl:
				w.frame(d.Body, nil, w.paramVars(nil, d.Params), d.Name.Name, "", false)
			case *syntax.LawDecl:
				if d.Body != nil {
					w.frame(d.Body, nil, w.paramVars(nil, d.Params), d.Name.Name, "", false)
				}
			default:
				continue
			}
			seen := map[ownDiag]bool{}
			for _, g := range w.diags {
				if !seen[g] {
					seen[g] = true
					c.errorf(g.at, CodeType, "%s", g.msg)
				}
			}
		}
	}
}

// paramVars is the declared parameters, the receiver list first, in the
// signature's order; nil for a parameter written with no name.
func (w *ownWalk) paramVars(lists ...*syntax.FieldList) []*Var {
	var out []*Var
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			if len(f.Names) == 0 {
				out = append(out, nil)
			}
			for _, id := range f.Names {
				v, _ := w.info.Defs[id].(*Var)
				out = append(out, v)
			}
		}
	}
	return out
}

func (w *ownWalk) step(n int) { w.c.ownSteps += n }

func (w *ownWalk) report(at syntax.Node, format string, args ...any) {
	if w.st == nil {
		return
	}
	w.diags = append(w.diags, ownDiag{at: at, msg: fmt.Sprintf(format, args...)})
}

func (w *ownWalk) pos(n syntax.Node) string {
	line, col := w.c.cur.syntax.Position(n.Pos())
	return fmt.Sprintf("%d:%d", line, col)
}

// tracked reports whether a value of type t has an owner here.
func (w *ownWalk) tracked(t Type) bool {
	return t != nil && !isInvalid(t) && w.c.holdsResource(t)
}

func (w *ownWalk) copyState(s []ownSlot) []ownSlot {
	if s == nil {
		return nil
	}
	w.step(len(s))
	return append([]ownSlot(nil), s...)
}

// join is the state where two paths meet, cut to n slots.
func (w *ownWalk) join(a, b []ownSlot, n int) []ownSlot {
	if a == nil {
		if b == nil {
			return nil
		}
		return padState(w.copyState(b), n)
	}
	a = padState(a, n)
	if b == nil {
		return a
	}
	b = padState(b, n)
	w.step(n)
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		switch {
		case x.st == y.st:
		case x.st == ownNone:
			a[i] = y
		case y.st == ownNone:
		case x.st == ownOwned:
			a[i] = ownSlot{st: ownMaybe, at: y.at, how: y.how}
		case y.st == ownOwned || y.st == ownMaybe:
			a[i] = ownSlot{st: ownMaybe, at: x.at, how: x.how}
		}
		a[i].loans = liveLoans(x.loans, y.loans)
	}
	return a
}

// padState cuts s to n slots, or extends it with slots not live yet: a
// failure edge leaves before later temporaries are acquired.
func padState(s []ownSlot, n int) []ownSlot {
	if len(s) >= n {
		return s[:n]
	}
	return append(s, make([]ownSlot, n-len(s))...)
}

func sameState(a, b []ownSlot) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].st != b[i].st {
			return false
		}
		x, y := liveLoans(a[i].loans), liveLoans(b[i].loans)
		if len(x) != len(y) {
			return false
		}
		for j := range x {
			if x[j] != y[j] {
				return false
			}
		}
	}
	return true
}

// acquire adds a value the current scope owns.
func (w *ownWalk) acquire(l ownLocal) int {
	l.depth = w.depth
	w.vars = append(w.vars, l)
	if w.st != nil {
		w.st = append(w.st, ownSlot{st: ownOwned})
	}
	i := len(w.vars) - 1
	if l.v != nil {
		w.slot[l.v] = i
	}
	return i
}

// releases is what slots first… still hold, last acquired first.
func (w *ownWalk) releases(first int) []Release {
	if w.st == nil {
		return nil
	}
	var out []Release
	for i := len(w.st) - 1; i >= first; i-- {
		s := w.st[i].st
		if s != ownOwned && s != ownMaybe {
			continue
		}
		l := w.vars[i]
		if l.lent {
			continue // its owner releases it
		}
		out = append(out, Release{Var: l.v, Temp: l.temp, Flag: s == ownMaybe})
	}
	return out
}

func (w *ownWalk) record(key syntax.Node, rs []Release) {
	if len(rs) == 0 {
		delete(w.info.Releases, key)
		return
	}
	w.info.Releases[key] = rs
}

func (w *ownWalk) open(key syntax.Node) {
	w.scopes = append(w.scopes, ownScope{key: key, first: len(w.vars)})
}

// close ends the innermost scope: it releases what the scope still owns.
func (w *ownWalk) close() {
	s := w.scopes[len(w.scopes)-1]
	w.scopes = w.scopes[:len(w.scopes)-1]
	if w.st != nil {
		// An end no path reaches keeps what its exit edge recorded.
		w.record(s.key, w.releases(s.first))
	}
	for _, l := range w.vars[s.first:] {
		if l.v != nil {
			delete(w.slot, l.v)
		}
	}
	w.vars = w.vars[:s.first]
	if w.st != nil {
		w.st = w.st[:s.first]
	}
}

// frame checks one body: a declaration's (depth 1) or a nested function
// literal's or trailing block's. params are its parameters in sig's order;
// a parameter sig's takes clause names is owned, any other is lent. A
// nested frame starts from the enclosing state (its captures are lent)
// and leaves it unchanged.
func (w *ownWalk) frame(body *syntax.BlockStmt, sig *Signature, params []*Var, name, nested string, lendAll bool) {
	saved := ownFrameSave{loops: w.loops, catches: w.catches, first: w.first, name: w.name, nested: w.nested, tail: w.tail}
	w.tail = frameTail
	outer := w.copyState(w.st)
	nvars := len(w.vars)
	w.loops, w.catches = nil, nil
	w.depth++
	w.first = len(w.vars)
	w.name = name
	w.nested = nested
	if w.st == nil {
		// A body no path reaches is still checked on its own.
		w.st = make([]ownSlot, len(w.vars))
	}
	w.open(body)
	taken := map[int]bool{}
	if sig != nil {
		for _, i := range sig.Takes {
			taken[i] = true
		}
	}
	for i, v := range params {
		if v != nil && w.tracked(v.Type) {
			w.acquire(ownLocal{v: v, lent: lendAll || !taken[i]})
		}
	}
	w.stmts(body.List, true)
	w.close()
	w.vars = w.vars[:nvars]
	w.st = outer
	w.depth--
	w.loops, w.catches, w.first, w.name, w.nested, w.tail = saved.loops, saved.catches, saved.first, saved.name, saved.nested, saved.tail
}

// block checks a scope's statements; key is the scope.
func (w *ownWalk) block(key syntax.Node, list []syntax.Stmt) {
	w.open(key)
	w.stmts(list, false)
	w.close()
}

func (w *ownWalk) stmts(list []syntax.Stmt, _ bool) {
	for _, s := range list {
		w.stmt(s)
	}
}

// region evaluates a statement or header f whose temporaries key owns.
func (w *ownWalk) region(key syntax.Node, f func()) {
	w.open(key)
	f()
	w.close()
}

func (w *ownWalk) stmt(s syntax.Stmt) {
	w.step(1)
	switch s := s.(type) {
	case *syntax.DeclStmt:
		if s.Decl == nil {
			return
		}
		for _, spec := range s.Decl.Specs {
			w.letSpec(s, spec)
		}
	case *syntax.ExprStmt:
		if _, tail := w.info.Tails[s]; tail {
			t := w.tail
			w.region(s, func() { w.expr(s.X, t.mode, hand{at: s.X, how: t.how, lend: t.lend, yieldRoot: t.yieldRoot}) })
			return
		}
		w.region(s, func() { w.expr(s.X, mDiscard, hand{}) })
	case *syntax.AssignStmt:
		w.region(s, func() { w.assign(s) })
	case *syntax.ReturnStmt:
		w.open(s)
		for _, r := range s.Results {
			w.expr(r, mMove, hand{at: r, how: "it is returned"})
		}
		w.record(s, w.releases(w.first))
		w.st = nil
		w.close()
	case *syntax.BranchStmt:
		if len(w.loops) == 0 {
			return
		}
		lp := w.loops[len(w.loops)-1]
		w.record(s, w.releases(lp.first))
		if w.st != nil {
			st := w.copyState(w.st[:lp.first])
			if s.Tok == syntax.BREAK {
				lp.breaks = append(lp.breaks, st)
			} else {
				lp.cont = append(lp.cont, st)
			}
		}
		w.st = nil
	case *syntax.BlockStmt:
		w.block(s, s.List)
	case *syntax.IfStmt:
		w.region(s.Cond, func() { w.expr(s.Cond, mLend, hand{}) })
		n := len(w.vars)
		saved := w.copyState(w.st)
		w.block(s.Body, s.Body.List)
		then := w.st
		w.st = saved
		if s.Else != nil {
			w.stmt(s.Else)
		}
		w.st = w.join(then, w.st, n)
	case *syntax.SwitchStmt:
		if s.Tag != nil {
			w.region(s.Tag, func() { w.expr(s.Tag, mLend, hand{}) })
		}
		w.clauses(s.Clauses)
	case *syntax.TypeSwitchStmt:
		for _, g := range s.Subjects {
			w.region(g, func() { w.expr(g.X, mLend, hand{}) })
		}
		w.clauses(s.Clauses)
	case *syntax.ForStmt:
		w.loop(s, s.Cond, s.Body)
	case *syntax.RangeStmt:
		w.region(s.X, func() { w.expr(s.X, mLend, hand{}) })
		w.loop(s, nil, s.Body)
	case *syntax.AssertStmt:
		w.region(s, func() { w.expr(s.X, mLend, hand{}) })
	}
}

func (w *ownWalk) clauses(cs []*syntax.CaseClause) {
	if len(cs) == 0 {
		return
	}
	n := len(w.vars)
	entry := w.copyState(w.st)
	var out []ownSlot
	for _, cl := range cs {
		w.st = w.copyState(entry)
		w.block(cl, cl.Body)
		out = w.join(out, w.st, n)
	}
	w.st = out
}

// ownLoopMemo is a loop's last fixpoint: its entry state, its exit state
// and the diagnostics of its final pass. An enclosing loop's later pass
// that reaches the loop with the same entry reuses it, so each loop body
// runs a bounded number of times in all (the lattice height), however
// deep it nests.
type ownLoopMemo struct {
	entry, out []ownSlot
	diags      []ownDiag
}

// sameExact reports equal states, hand-offs included (they name the
// diagnostics).
func sameExact(a, b []ownSlot) bool {
	if !sameState(a, b) {
		return false
	}
	for i := range a {
		if a[i].at != b[i].at || a[i].how != b[i].how {
			return false
		}
	}
	return true
}

// loop runs a loop body to its fixpoint. A for-in's body runs zero or
// more times, as a for with a condition does; a bare for ends only by
// break.
func (w *ownWalk) loop(n syntax.Node, cond syntax.Expr, body *syntax.BlockStmt) {
	first := len(w.vars)
	entry := w.st
	if m := w.loopMemo[n]; m != nil && entry != nil {
		entry = w.join(w.copyState(entry), m.entry, first)
		if sameExact(entry, m.entry) {
			// The fixpoint from this entry is known: its exit state and
			// its diagnostics; its release plan is already recorded.
			w.diags = append(w.diags, m.diags...)
			w.st = w.copyState(m.out)
			return
		}
	}
	_, ranged := n.(*syntax.RangeStmt)
	for {
		mark := len(w.diags)
		lp := &ownLoop{first: first}
		w.loops = append(w.loops, lp)
		w.st = w.copyState(entry)
		var exits []ownSlot
		if cond != nil {
			w.region(cond, func() { w.expr(cond, mLend, hand{}) })
			exits = w.copyState(w.st)
		} else if ranged {
			exits = w.copyState(w.st)
		}
		w.block(body, body.List)
		back := w.st
		for _, c := range lp.cont {
			back = w.join(back, c, first)
		}
		w.loops = w.loops[:len(w.loops)-1]
		next := w.join(w.copyState(entry), back, first)
		if entry == nil || sameState(next, entry) {
			out := exits
			for _, b := range lp.breaks {
				out = w.join(out, b, first)
			}
			if entry != nil {
				w.loopMemo[n] = &ownLoopMemo{entry: entry, out: w.copyState(out), diags: append([]ownDiag(nil), w.diags[mark:]...)}
			}
			w.st = out
			return
		}
		entry = next
		w.diags = w.diags[:mark]
	}
}

// letSpec binds a let's names; the values move into them.
func (w *ownWalk) letSpec(s syntax.Stmt, spec *syntax.VarSpec) {
	var bound []*Var
	w.region(spec, func() {
		pair := len(spec.Values) == len(spec.Names) && spec.Pattern == nil
		for i, x := range spec.Values {
			mode := mMove
			if pair && spec.Names[i].Name == "_" {
				mode = mDiscard
			}
			w.expr(x, mode, hand{at: x, how: "let moves it"})
		}
		if len(spec.Values) == 1 && len(spec.Names) > 1 {
			if tup, ok := w.info.Types[spec.Values[0]].(*Tuple); ok {
				for i, id := range spec.Names {
					if id.Name == "_" && i < len(tup.Types) && w.tracked(tup.Types[i]) {
						w.report(id, "the %s bound to _ is discarded, and a resource has one owner; bind it to a name, so its scope releases it, or hand it off (resources R42)", tup.Types[i])
					}
				}
			}
		}
	})
	_ = s
	for _, id := range spec.Names {
		if v, ok := w.info.Defs[id].(*Var); ok && w.tracked(v.Type) {
			bound = append(bound, v)
		}
	}
	for _, v := range bound {
		w.acquire(ownLocal{v: v})
	}
}

func (w *ownWalk) assign(s *syntax.AssignStmt) {
	if s.Tok != syntax.ASSIGN {
		for _, x := range s.Rhs {
			w.expr(x, mLend, hand{})
		}
		for _, x := range s.Lhs {
			w.expr(x, mLend, hand{})
		}
		return
	}
	for i, x := range s.Rhs {
		mode := mLend
		if i < len(s.Lhs) && w.tracked(w.info.Types[s.Lhs[i]]) || len(s.Lhs) > 1 {
			mode = mMove
		}
		w.expr(x, mode, hand{at: x, how: "it is assigned"})
	}
	for _, x := range s.Lhs {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			w.lhsPlace(x)
			continue
		}
		v, _ := w.info.Uses[id].(*Var)
		i, ok := w.slot[v]
		if v == nil || !ok || w.st == nil {
			continue
		}
		// R41: the old value is released, where it is still owned.
		var rs []Release
		if st := w.st[i].st; st == ownOwned || st == ownMaybe {
			w.loanConflict(i, hand{at: id, how: "it is replaced"})
			rs = []Release{{Var: v, Flag: st == ownMaybe}}
		}
		w.record(id, rs)
		if w.vars[i].depth != w.depth {
			w.report(id, "%s belongs to the enclosing function; %s cannot replace it (resources R4)", v.Name, w.nestedText())
			continue
		}
		w.st[i] = ownSlot{st: ownOwned}
	}
}

// lhsPlace evaluates an assigned place: its root is used, not moved.
func (w *ownWalk) lhsPlace(x syntax.Expr) {
	switch x := x.(type) {
	case *syntax.SelectorExpr:
		w.lhsPlace(x.X)
	case *syntax.IndexExpr:
		w.lhsPlace(x.X)
		for _, i := range x.Indices {
			w.expr(i, mLend, hand{})
		}
	case *syntax.ParenExpr:
		w.lhsPlace(x.X)
	default:
		w.expr(x, mLend, hand{})
	}
}

type ownMode uint8

const (
	mLend    ownMode = iota // the value is lent: read, or passed to a lending parameter
	mMove                   // the value moves to a new owner
	mDiscard                // the value is dropped
	// mHold: a yielded result is owned by w.tail.slot when its origin
	// cannot survive that result's consumer. An enclosing name stays lent.
	// With no slot the context build owns new provider values.
	mHold
)

// hand names a hand-off for the diagnostics: where it is and what it does.
type hand struct {
	at        syntax.Node
	how       string
	lend      func(int, *syntax.Ident) // the root owners lent by this result
	yieldRoot bool                     // this root is projected into an enclosing block result
}

func (w *ownWalk) nestedText() string {
	if w.nested != "" {
		return w.nested
	}
	return "this function literal"
}

// use checks a lend of slot i at x.
func (w *ownWalk) use(i int, x *syntax.Ident) {
	if w.st == nil {
		return
	}
	s := w.st[i]
	switch s.st {
	case ownMoved:
		w.report(x, "%s was handed off at %s (%s) and cannot be used after that (resources R15, R20); use %s before it is handed off", x.Name, w.pos(s.at), s.how, x.Name)
	case ownMaybe:
		w.report(x, "%s may have been handed off at %s (%s) on one path, and cannot be used after that (resources R21); hand it off on every path or on none", x.Name, w.pos(s.at), s.how)
	}
}

// move hands slot i off at h.
func (w *ownWalk) move(i int, x *syntax.Ident, h hand) {
	if w.st == nil {
		return
	}
	w.use(i, x)
	if s := w.st[i]; s.st != ownOwned {
		// Reported by use; the first hand-off stays the one named.
		w.st[i] = ownSlot{st: ownMoved, at: s.at, how: s.how}
		return
	}
	l := w.vars[i]
	switch {
	case l.depth != w.depth:
		w.report(x, "%s cannot hand off %s (%s): %s belongs to the enclosing function, which releases it, and the block may run at another time or more than once (resources R4, R30)", w.nestedText(), x.Name, h.how, x.Name)
		return
	case l.lent:
		fn := w.name
		if w.nested != "" {
			fn = w.nested
		}
		w.report(x, "%s is lent to %s, so it cannot hand %s off (%s) (resources R10, R17); write takes %s in the signature", x.Name, fn, x.Name, h.how, x.Name)
		return
	}
	w.loanConflict(i, h)
	w.st[i] = ownSlot{st: ownMoved, at: h.at, how: h.how}
}

// loanConflict checks on the moving path, before the root owner ends.
// Joining a lend in one arm with a take in the other must not invent a
// path that does both. A lend that survives the join is checked if a later
// operand moves or replaces that owner.
func (w *ownWalk) loanConflict(i int, h hand) {
	for _, l := range w.st[i].loans {
		if l.call.active {
			w.report(l.id, "%s is lent to %s here and handed off at %s (%s) in the same call (resources R17); hand it off in a statement of its own", l.id.Name, l.call.name, w.pos(h.at), h.how)
		}
	}
}

// expr evaluates x in mode. Its type switch names every kind of
// expression and has no default (TestMintFormNamesEveryKind reads it): an
// expression with statement children (a block, a case clause, a catch
// arm, a function body) walks them with the flow, and a new kind is an
// internal compiler error until it has a rule here (batch-15 A2 M1: the
// arms of an if or match used as a value were skipped).
func (w *ownWalk) expr(x syntax.Expr, mode ownMode, h hand) {
	if x == nil || w.c.typeNodes[x] {
		return
	}
	w.step(1)
	lend := func(xs ...syntax.Expr) {
		for _, e := range xs {
			w.expr(e, mLend, hand{})
		}
	}
	switch x := x.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		w.branchValue(x, mode, h)
		return
	case *syntax.ContextLit:
		// Its providers are built and owned by the with that discharges it
		// (Context ownership as built): a name is lent, a new value is no
		// temporary of the statement.
		if x.Body != nil {
			saved := w.tail
			w.tail = ownTail{mode: mHold, slot: -1}
			for _, e := range x.Body.Entries {
				w.expr(e.Value, mHold, hand{})
			}
			w.tail = saved
		}
		return
	case *syntax.BasicLit, *syntax.ArrayType, *syntax.MapType, *syntax.FuncType, *syntax.StructType,
		*syntax.InterfaceType, *syntax.ErrorRow, *syntax.MachineType, *syntax.VariantSpec, *syntax.AsPattern:
		// A literal, a type or a pattern: no value of a local is used.
		return
	case *syntax.KeyValueExpr:
		lend(x.Key, x.Value)
		return
	case *syntax.SelectorExpr:
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		return
	case *syntax.IndexExpr:
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		lend(x.Indices...)
		return
	case *syntax.SliceExpr:
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		lend(x.Low, x.High, x.Max)
		return
	case *syntax.TypeGuard:
		lend(x.X)
		return
	case *syntax.StarExpr:
		lend(x.X)
		return
	case *syntax.UnaryExpr:
		lend(x.X)
		return
	case *syntax.BinaryExpr:
		if x.Op == syntax.LAND || x.Op == syntax.LOR {
			w.shortCircuit(x)
			return
		}
		lend(x.X, x.Y)
		return
	case *syntax.TupleExpr:
		for _, e := range x.Elts {
			w.lendRoot(e, w.info.Types[x], mode, h)
		}
		return
	case *syntax.InterpolatedString:
		lend(x.Exprs...)
		return
	case *syntax.CompositeLit:
		lend(x.Base)
		lend(x.Elts...)
		w.produced(x, mode, h)
		return
	case *syntax.Ident:
		v, _ := w.info.Uses[x].(*Var)
		i, ok := w.slot[v]
		if v == nil || !ok {
			return
		}
		if mode == mHold && w.tail.slot >= 0 && i >= w.tail.first && !h.yieldRoot {
			// The source owner ends inside the result expression. Transfer
			// it before closing that scope, just as for a directly produced
			// result. Nested result blocks share the same destination.
			owned := w.st != nil && w.st[i].st == ownOwned
			w.move(i, x, hand{at: x, how: "it is yielded to its consumer"})
			if owned && w.st[i].st == ownMoved {
				w.st[w.tail.slot] = ownSlot{st: ownOwned}
			}
		} else if mode == mMove {
			w.move(i, x, h)
		} else {
			w.use(i, x)
			if h.yieldRoot && w.tail.slot >= 0 && i >= w.tail.first {
				w.report(x, "the yielded resource is rooted at %s, whose owner ends before its consumer; yield the whole owner instead of a part (resources R43)", x.Name)
			}
			if h.lend != nil && w.st != nil && w.st[i].st == ownOwned {
				h.lend(i, x)
			}
		}
		return
	case *syntax.ParenExpr:
		w.expr(x.X, mode, h)
		return
	case *syntax.TryExpr:
		w.expr(x.X, mode, h)
		w.failure(x)
		return
	case *syntax.CatchExpr:
		w.yieldValue(x, mode, h, func(m ownMode) { w.catch(x, m, h) })
		return
	case *syntax.CallExpr:
		if !w.call(x, mode, h) {
			w.produced(x, mode, h)
		}
		return
	case *syntax.FuncLit:
		var sig *Signature
		if s, ok := w.info.Literals[x]; ok {
			sig = s
		}
		var params *syntax.FieldList
		if x.Type != nil {
			params = x.Type.Params
		}
		w.frame(x.Body, sig, w.paramVars(params), w.name, "this function literal", false)
		return
	}
	panic(fmt.Sprintf("check: internal compiler error: the ownership flow has no rule for a %T (ownWalk.expr)", x))
}

// lendRoot carries the root owner only when the expression's result holds
// a resource. Reading r.id as an int does not lend r to that int's consumer.
func (w *ownWalk) lendRoot(root syntax.Expr, result Type, mode ownMode, h hand) {
	if !w.tracked(result) {
		h.lend = nil
		h.yieldRoot = false
	} else if mode == mHold && w.tail.slot >= 0 {
		h.yieldRoot = true
	}
	w.expr(root, mLend, hand{lend: h.lend, yieldRoot: h.yieldRoot})
}

// produced owns or refuses a new value x of a resource type: a call's
// result or a literal. A lent one is a temporary of the statement (R43);
// a dropped one is discarded (R42); a moved one has its new owner; a held
// one is the value of the lent if or match whose slot is w.tail.slot.
func (w *ownWalk) produced(x syntax.Expr, mode ownMode, h hand) {
	t := w.info.Types[x]
	if !w.tracked(t) || w.st == nil {
		return
	}
	if h.yieldRoot {
		w.report(x, "the yielded resource is rooted in this temporary, whose owner ends before its consumer; bind and yield the whole owner instead of a part (resources R43)")
	}
	switch mode {
	case mLend:
		w.acquire(ownLocal{temp: x})
	case mDiscard:
		w.report(x, "the %s this produces is discarded, and a resource has one owner; bind it with let, so its scope releases it, or hand it off (resources R42)", t)
	case mHold:
		if i := w.tail.slot; i >= 0 && i < len(w.st) {
			w.st[i] = ownSlot{st: ownOwned}
		}
	}
}

// ownTail is where a block's value goes: the mode its tail expression is
// evaluated in, the hand-off it names, and, for mHold, the slot of the
// temporary that owns a new value (-1: an owner outside the flow).
type ownTail struct {
	mode      ownMode
	how       string
	slot      int
	first     int // locals at/after this boundary cannot outlive the consumer
	lend      func(int, *syntax.Ident)
	yieldRoot bool
}

// frameTail is a function body's: its value is returned.
var frameTail = ownTail{mode: mMove, how: "it is the block's value", slot: -1}

// branchValue walks the structured paths of an if or match as a value.
func (w *ownWalk) branchValue(x syntax.Expr, mode ownMode, h hand) {
	w.yieldValue(x, mode, h, func(ownMode) { w.stmt(x.(syntax.Stmt)) })
}

// yieldValue gives every block-valued expression one result destination:
// if, match, catch and with all transfer their result before arm locals
// are released. A lending consumer owns a new or block-local result until
// its statement ends; an enclosing lender keeps its original owner. The
// boundary is that of the outer result, not the innermost arm, so nested
// blocks cannot shorten the yielded value's lifetime. Prior art: Rust's
// MIR transfers an arm result to an outer place before dropping locals.
func (w *ownWalk) yieldValue(x syntax.Expr, mode ownMode, h hand, walk func(ownMode)) {
	saved := w.tail
	t := ownTail{mode: mode, how: h.how, slot: -1, first: len(w.vars), lend: h.lend, yieldRoot: h.yieldRoot}
	switch {
	case mode == mHold:
		t = saved
	case mode == mLend && w.tracked(w.info.Types[x]) && w.st != nil:
		t.mode = mHold
		t.slot = w.acquire(ownLocal{temp: x})
		w.st[t.slot] = ownSlot{st: ownMoved, at: x, how: "no arm made it"}
		t.first = len(w.vars)
		if h.yieldRoot {
			t.first = saved.first
		}
	}
	w.tail = t
	walk(t.mode)
	w.resultAfterOperands(x, t.slot)
	w.tail = saved
}

// resultAfterOperands separates reserving a result place from acquiring its
// value. Operand temporaries are acquired before the result, so the result
// must be released first (R5). Only this expression's own statement-level
// result place moves; an inherited place belongs to an enclosing consumer.
func (w *ownWalk) resultAfterOperands(x syntax.Expr, slot int) {
	if w.st == nil || slot < 0 || slot+1 >= len(w.vars) || w.vars[slot].temp != x {
		return
	}
	n := len(w.vars)
	for _, l := range w.vars[slot+1:] {
		if l.v != nil {
			panic("check: internal compiler error: a result place crosses a named owner")
		}
	}
	l := w.vars[slot]
	copy(w.vars[slot:], w.vars[slot+1:])
	w.vars[n-1] = l
	rotate := func(st []ownSlot) []ownSlot {
		if st == nil {
			return nil
		}
		st = padState(st, n)
		result := st[slot]
		copy(st[slot:], st[slot+1:])
		st[n-1] = result
		w.step(n - slot)
		return st
	}
	w.st = rotate(w.st)
	// An enclosing catch may have saved a failure before the result was
	// acquired. Its states must use the same place layout at the join.
	for _, k := range w.catches {
		for i, st := range k.fails {
			k.fails[i] = rotate(w.copyState(st))
		}
	}
}

// shortCircuit walks `a && b` or `a || b`: a runs on every path, b only on
// some, so the state after it joins the state after a, as an if with no
// else does (resources R21: a value handed off in b is owned on the other
// path and gets a drop flag). A temporary b acquires lives to the end of
// the statement on the paths that ran b only, so it is released under a
// flag too. Prior art: Rust lowers && and || to control flow before
// borrow checking, so a move in the right operand is a conditional move.
func (w *ownWalk) shortCircuit(x *syntax.BinaryExpr) {
	w.expr(x.X, mLend, hand{})
	n := len(w.vars)
	skipped := w.copyState(w.st)
	w.expr(x.Y, mLend, hand{})
	w.st = w.join(w.st, skipped, len(w.vars))
	for i := n; i < len(w.st); i++ {
		if w.st[i].st == ownOwned {
			w.st[i] = ownSlot{st: ownMaybe, at: x.Y, how: "its operand did not run"}
		}
	}
}

// failure is a try's failure edge: to the enclosing catch of this frame,
// or out of the function, releasing what the frame owns.
func (w *ownWalk) failure(x syntax.Node) {
	if w.st == nil {
		return
	}
	if n := len(w.catches); n > 0 {
		k := w.catches[n-1]
		k.fails = append(k.fails, w.copyState(w.st))
		return
	}
	w.record(x, w.releases(w.first))
}

func (w *ownWalk) catch(x *syntax.CatchExpr, mode ownMode, h hand) {
	first := len(w.vars)
	k := &ownCatch{first: first}
	w.catches = append(w.catches, k)
	w.expr(x.X, mode, h)
	w.catches = w.catches[:len(w.catches)-1]
	// The caught call fails after its operands are evaluated; a try inside
	// it fails before the later temporaries exist.
	n := len(w.vars)
	fail := w.copyState(w.st)
	if mode == mHold && w.tail.slot >= 0 && fail != nil {
		// The success result does not exist on a caught failure path.
		fail[w.tail.slot] = ownSlot{st: ownMoved, at: x, how: "the result failed"}
	}
	for _, f := range k.fails {
		fail = w.join(fail, f, n)
	}
	out := w.st
	for _, arm := range x.Arms {
		w.st = w.copyState(fail)
		w.block(arm, arm.Body)
		out = w.join(out, w.st, n)
	}
	w.st = out
}

// call evaluates a call; it reports false when x is to be treated as a
// produced value by expr.
func (w *ownWalk) call(x *syntax.CallExpr, mode ownMode, h hand) bool {
	call := w.info.Calls[x]
	if call == nil {
		for _, a := range x.Args {
			w.expr(a, mLend, hand{})
		}
		return false
	}
	switch call.Kind {
	case CallConversion:
		for _, a := range x.Args {
			w.expr(a, mode, h)
		}
		return true
	case CallWith:
		w.yieldValue(x, mode, h, func(ownMode) {
			for _, a := range x.Args {
				w.expr(a, mLend, hand{})
			}
			if x.Trailing != nil {
				w.block(x.Trailing, x.Trailing.List)
			}
		})
		return true
	}
	var sig *Signature
	name := ""
	switch call.Kind {
	case CallFunc:
		if call.Func != nil {
			sig, name = call.Func.Sig, call.Func.Name
		}
	case CallOp, CallProviderOp, CallLawOp:
		if call.Op != nil {
			sig, name = call.Op.Sig, call.Op.Name
		}
	case CallFuncValue:
		if s, ok := under(w.info.Types[x.Fun]).(*Signature); ok {
			sig = s
		}
		name = "the function value"
	}
	taken := map[int]bool{}
	selfTaken := false
	if sig != nil {
		for _, i := range sig.Takes {
			taken[i] = true
		}
		selfTaken = sig.TakesSelf
	}
	closes := w.info.Closes[x] != nil || w.info.Closes[x.Fun] != nil
	if closes {
		selfTaken = true
		if call.Kind == CallFunc {
			taken[0] = true
		}
	}
	var loan *ownLoanCall
	defer func() {
		if loan != nil {
			loan.active = false
		}
	}()
	operand := func(a syntax.Expr, take bool, pname string) {
		if take {
			how := fmt.Sprintf("%s takes %s", name, pname)
			if pname == "" || closes {
				how = fmt.Sprintf("%s takes it", name)
			}
			w.expr(a, mMove, hand{at: a, how: how})
			return
		}
		w.expr(a, mLend, hand{lend: func(i int, id *syntax.Ident) {
			if loan == nil {
				loan = &ownLoanCall{name: name, active: true}
			}
			w.st[i].loans = append(liveLoans(w.st[i].loans), ownLoan{loan, id})
		}})
	}
	pname := func(i int) string {
		if sig != nil && i < len(sig.Params) {
			return sig.Params[i].Name
		}
		return ""
	}
	off := 0
	switch {
	case call.Recv != nil && call.Kind == CallFunc:
		operand(call.Recv, taken[0], pname(0))
		off = 1
	case call.Recv != nil:
		operand(call.Recv, selfTaken, "")
	default:
		w.expr(x.Fun, mLend, hand{})
	}
	for i, a := range x.Args {
		operand(a, taken[i+off], pname(i+off))
	}
	if x.Trailing != nil {
		var tsig *Signature
		if s, ok := w.info.Literals[x.Trailing]; ok {
			tsig = s
		}
		w.frame(x.Trailing, tsig, nil, w.name, fmt.Sprintf("the block passed to %s", calleeText(x)), false)
	}
	return false
}

// calleeText is the written callee of x, for a message.
func calleeText(x *syntax.CallExpr) string {
	switch f := x.Fun.(type) {
	case *syntax.Ident:
		return f.Name
	case *syntax.SelectorExpr:
		if id, ok := f.X.(*syntax.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	}
	return "the call"
}
