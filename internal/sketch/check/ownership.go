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
// block's value, a part of a tuple or composite literal that is handed
// off (R14), an argument for a parameter the callee's takes clause names
// (written, or inferred: takes.go), the value an operation with a bare
// takes is called on, and the receiver of a checked Close (R1, R19).
// Every other use lends (R16). A value of a type parameter is tracked
// too; a refusal of one is a copy site (takes.go), not a diagnostic.
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
	st  ownSt
	at  syntax.Node // the hand-off (moved, maybe)
	how string
	// The fields of an owned record taken in place (`b.r.Close()`, R39,
	// Gap 38), by field index: on every path (fields), on some (maybe);
	// fieldAt is the first such take, for the diagnostics.
	fields, maybe uint64
	fieldAt       syntax.Node
}

// live reports an owned or maybe-moved slot: one whose fields count.
func (s ownSlot) live() bool { return s.st == ownOwned || s.st == ownMaybe }

// ownLocal is what a slot holds: a local, a parameter or a temporary.
type ownLocal struct {
	v     *Var
	temp  syntax.Expr
	lent  bool // a parameter the function does not take
	depth int  // the frame that owns it
	param int  // the declaration's parameter index plus 1; 0 for none
	// from names what a lent binding is lent from: a for-in's container
	// (R35) or a match subject that is lent (Gap 36).
	from string
}

// typ is the type of the value l holds.
func (w *ownWalk) typ(l ownLocal) Type {
	if l.v != nil {
		return l.v.Type
	}
	return w.info.Types[l.temp]
}

// genericOnly reports a type that holds a resource only through a type
// parameter: a refusal on such a value is a copy site (takes.go).
func (w *ownWalk) genericOnly(t Type) bool {
	return t != nil && !w.c.holdsResource(t)
}

// refuse reports a refusal on slot i, or records it as a copy site when
// the slot's value is a resource only through a type parameter.
func (w *ownWalk) refuse(i int, at syntax.Node, what string, format string, args ...any) {
	if w.st == nil {
		return
	}
	if t := w.typ(w.vars[i]); w.genericOnly(t) {
		w.copySite(t, at, what, false)
		return
	}
	w.report(at, format, args...)
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
}

type ownDiag struct {
	at   syntax.Node
	code string
	msg  string
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
	// Takes inference (takes.go): infer is the silent pass that promotes
	// fn's lent parameters (promote) and records its copy sites (copies);
	// fn is the declaration walked.
	infer   bool
	fn      *Func
	promote map[int]bool
	copies  map[*TypeParam]CopySite
}

func (c *checker) newOwnWalk() *ownWalk {
	return &ownWalk{c: c, info: c.pkg.Info, slot: map[*Var]int{}, loopMemo: map[syntax.Node]*ownLoopMemo{}}
}

// checkOwnership runs the flow pass over every body of the package, after
// the takes clauses and copy sites are inferred.
func (c *checker) checkOwnership() {
	saved := c.cur
	defer func() { c.cur = saved }()
	c.inferTakes()
	for _, fi := range c.files {
		c.cur = fi
		for _, d := range fi.syntax.Decls {
			w := c.newOwnWalk()
			switch d := d.(type) {
			case *syntax.FuncDecl:
				if d.Body == nil || d.Extern != nil {
					continue
				}
				f, _ := c.pkg.Info.Defs[d.Name].(*Func)
				if f == nil || f.Sig == nil {
					continue
				}
				w.fn = f
				w.frame(d.Body, takesOf(f), w.paramVars(d.Recv, d.Type.Params), d.Name.Name, "", f.Name == "Close" && c.pkg.Info.Closes[d] != nil)
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
					c.errorf(g.at, g.code, "%s", g.msg)
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
	w.reportCode(at, CodeType, format, args...)
}

func (w *ownWalk) reportCode(at syntax.Node, code, format string, args ...any) {
	if w.st == nil || w.infer {
		return
	}
	w.diags = append(w.diags, ownDiag{at: at, code: code, msg: fmt.Sprintf(format, args...)})
}

func (w *ownWalk) pos(n syntax.Node) string {
	line, col := w.c.cur.syntax.Position(n.Pos())
	return fmt.Sprintf("%d:%d", line, col)
}

// tracked reports whether a value of type t has an owner here: a value of
// a type parameter counts, so a generic body's copies are found (A3).
func (w *ownWalk) tracked(t Type) bool {
	return t != nil && !isInvalid(t) && w.c.mayHoldResource(t)
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
		// The fields taken in place: on every path where the record is
		// still held, or on some (a drop flag each, B2).
		switch {
		case x.live() && y.live():
			all := x.fields & y.fields
			a[i].fields, a[i].maybe = all, (x.fields|y.fields|x.maybe|y.maybe)&^all
		case x.live():
			a[i].fields, a[i].maybe = x.fields, x.maybe
		case y.live():
			a[i].fields, a[i].maybe = y.fields, y.maybe
		}
		a[i].fieldAt = x.fieldAt
		if a[i].fieldAt == nil {
			a[i].fieldAt = y.fieldAt
		}
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
		if a[i].st != b[i].st || a[i].fields != b[i].fields || a[i].maybe != b[i].maybe {
			return false
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
		out = append(out, Release{Var: l.v, Temp: l.temp, Flag: s == ownMaybe, Fields: w.st[i].fields, MaybeFields: w.st[i].maybe})
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
// literal's or trailing block's. params are its parameters in its
// signature's order; a parameter its takes clause (takes) names is owned,
// any other is lent. A nested frame starts from the enclosing state (its
// captures are lent) and leaves it unchanged.
func (w *ownWalk) frame(body *syntax.BlockStmt, takes []int, params []*Var, name, nested string, lendAll bool) {
	saved := ownFrameSave{loops: w.loops, catches: w.catches, first: w.first, name: w.name, nested: w.nested}
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
	for _, i := range takes {
		taken[i] = true
	}
	for i, v := range params {
		if v != nil && w.tracked(v.Type) {
			p := 0
			if w.depth == 1 {
				p = i + 1
			}
			w.acquire(ownLocal{v: v, lent: lendAll || !taken[i], param: p})
		}
	}
	w.stmts(body.List, true)
	w.close()
	w.vars = w.vars[:nvars]
	w.st = outer
	w.depth--
	w.loops, w.catches, w.first, w.name, w.nested = saved.loops, saved.catches, saved.first, saved.name, saved.nested
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
			w.region(s, func() { w.expr(s.X, mMove, hand{at: s.X, how: "it is the block's value"}) })
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
		var subj syntax.Expr
		if len(s.Subjects) == 1 {
			subj = s.Subjects[0].X
		}
		w.matchClauses(s.Clauses, subj)
	case *syntax.ForStmt:
		w.loop(s, s.Cond, s.Body, nil)
	case *syntax.RangeStmt:
		w.region(s.X, func() { w.expr(s.X, mLend, hand{}) })
		w.loop(s, nil, s.Body, w.rangeBinds(s))
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

// matchClauses checks a match's arms (Gap 36). An arm that binds a value
// holding a resource, on an owned local subject, moves the subject into
// the binding: the arm's scope owns the variant. On a lent subject (a
// parameter not taken, a field, a call) the binding is lent from it. An
// arm that binds nothing leaves the subject with its owner.
func (w *ownWalk) matchClauses(cs []*syntax.CaseClause, subj syntax.Expr) {
	if len(cs) == 0 {
		return
	}
	n := len(w.vars)
	entry := w.copyState(w.st)
	var out []ownSlot
	id, si, owned := w.rootSlot(subj)
	from := ""
	if subj != nil {
		from = exprText(subj)
	}
	for _, cl := range cs {
		w.st = w.copyState(entry)
		var binds []*Var
		for _, e := range cl.List {
			syntax.Inspect(e, func(x syntax.Node) bool {
				if b, ok := x.(*syntax.Ident); ok {
					if v, ok := w.info.Defs[b].(*Var); ok && w.tracked(v.Type) {
						binds = append(binds, v)
					}
				}
				return true
			})
		}
		w.open(cl)
		if len(binds) > 0 {
			l := ownLocal{}
			switch {
			case owned && !w.vars[si].lent && w.vars[si].depth == w.depth:
				w.move(si, id, hand{at: id, how: fmt.Sprintf("the match moves it into %s", binds[0].Name)})
			case owned:
				// Lent from a lent local: a hand-off of the binding is
				// one of the subject (inferred, or refused as its own).
				l = ownLocal{lent: true, param: w.vars[si].param, from: from}
			default:
				l = ownLocal{lent: true, from: from}
			}
			for _, v := range binds {
				l.v = v
				w.acquire(l)
			}
		}
		w.stmts(cl.Body, false)
		w.close()
		out = w.join(out, w.st, n)
	}
	w.st = out
}

// rangeBinds is a for-in's bindings that hold a resource: an element of a
// queue's Values() is the body's own (Q7); any other is lent from the
// container (R35).
func (w *ownWalk) rangeBinds(s *syntax.RangeStmt) []ownLocal {
	lp := w.info.Loops[s]
	if lp == nil {
		return nil
	}
	owned := false
	if call, ok := unparen(s.X).(*syntax.CallExpr); ok && lp.Domain == LoopSeq {
		if k := w.info.Calls[call]; k != nil && k.Kind == CallOp && k.Op != nil && k.Op.Name == "Values" {
			if sel, ok := call.Fun.(*syntax.SelectorExpr); ok && isQueue(w.info.Types[sel.X]) {
				owned = true
			}
		}
	}
	var out []ownLocal
	for _, v := range lp.Bindings {
		if v == nil || !w.tracked(v.Type) {
			continue
		}
		if owned {
			out = append(out, ownLocal{v: v})
		} else {
			out = append(out, ownLocal{v: v, lent: true, from: exprText(s.X)})
		}
	}
	return out
}

// isQueue reports std queue.Queue[T] or an instance of it.
func isQueue(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Pkg != nil && o.Pkg.std && o.Pkg.Path == "queue" && o.Name == "Queue"
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
func (w *ownWalk) loop(n syntax.Node, cond syntax.Expr, body *syntax.BlockStmt, binds []ownLocal) {
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
		w.open(body)
		for _, b := range binds {
			w.acquire(b) // each iteration's binding (R35, Q7)
		}
		w.stmts(body.List, false)
		w.close()
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
					if id.Name == "_" && i < len(tup.Types) && w.c.holdsResource(tup.Types[i]) {
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
			rs = []Release{{Var: v, Flag: st == ownMaybe}}
		}
		w.record(id, rs)
		if w.vars[i].depth != w.depth {
			w.refuse(i, id, fmt.Sprintf("%s replaces %s, which may run more than once", w.nestedText(), v.Name), "%s belongs to the enclosing function; %s cannot replace it (resources R4)", v.Name, w.nestedText())
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
)

// hand names a hand-off for the diagnostics: where it is and what it does.
type hand struct {
	at  syntax.Node
	how string
}

func (w *ownWalk) nestedText() string {
	if w.nested != "" {
		return w.nested
	}
	return "this function literal"
}

// use checks a lend of slot i at x: of the whole value, so none of its
// fields may be taken (R39).
func (w *ownWalk) use(i int, x *syntax.Ident) {
	w.useRoot(i, x)
	if w.st == nil {
		return
	}
	if s := w.st[i]; s.live() && s.fields|s.maybe != 0 {
		w.refuse(i, x, fmt.Sprintf("%s is used whole after a field is taken at %s", x.Name, w.pos(s.fieldAt)), "a field of %s was taken at %s, so %s cannot be used whole (resources R39, Gap 38); use its other fields", x.Name, w.pos(s.fieldAt), x.Name)
	}
}

// useRoot checks a use of slot i at x as the root of a place (x.f, x[i]).
func (w *ownWalk) useRoot(i int, x *syntax.Ident) {
	if w.st == nil {
		return
	}
	s := w.st[i]
	switch s.st {
	case ownMoved:
		w.refuse(i, x, fmt.Sprintf("%s is used after it is handed off at %s", x.Name, w.pos(s.at)), "%s was handed off at %s (%s) and cannot be used after that (resources R15, R20); use %s before it is handed off", x.Name, w.pos(s.at), s.how, x.Name)
	case ownMaybe:
		w.refuse(i, x, fmt.Sprintf("%s is used after it may be handed off at %s", x.Name, w.pos(s.at)), "%s may have been handed off at %s (%s) on one path, and cannot be used after that (resources R21); hand it off on every path or on none", x.Name, w.pos(s.at), s.how)
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
		w.refuse(i, x, fmt.Sprintf("%s hands off %s, which may run more than once", w.nestedText(), x.Name), "%s cannot hand off %s (%s): %s belongs to the enclosing function, which releases it, and the block may run at another time or more than once (resources R4, R30)", w.nestedText(), x.Name, h.how, x.Name)
		return
	case l.lent && w.infer && l.param > 0 && w.c.infersTakes(w.fn):
		// Inference (takes.go): the parameter is taken.
		w.promote[l.param-1] = true
	case l.lent && l.from != "":
		w.refuse(i, x, fmt.Sprintf("%s, lent from %s, is handed off (%s)", x.Name, l.from, h.how), "%s is lent from %s, so it cannot be handed off (%s) (resources R35, Gap 36); hand off %s whole", x.Name, l.from, h.how, l.from)
		return
	case l.lent:
		fn := w.name
		if w.nested != "" {
			fn = w.nested
		}
		// A lent T handed off is a copy of it (the caller keeps its own):
		// a mode, not a mark, so a call with a resource is refused (q56).
		w.refuse(i, x, fmt.Sprintf("%s is lent and handed off (%s); write takes %s to take it", x.Name, h.how, x.Name), "%s is lent to %s, so it cannot hand %s off (%s) (resources R10, R17); write takes %s in the signature", x.Name, fn, x.Name, h.how, x.Name)
		return
	}
	w.st[i] = ownSlot{st: ownMoved, at: h.at, how: h.how}
}

// expr evaluates x in mode.
func (w *ownWalk) expr(x syntax.Expr, mode ownMode, h hand) {
	if x == nil {
		return
	}
	w.step(1)
	switch x := x.(type) {
	case *syntax.Ident:
		v, _ := w.info.Uses[x].(*Var)
		i, ok := w.slot[v]
		if v == nil || !ok {
			w.funcValue(x, w.info.Types[x])
			return
		}
		if mode == mMove {
			w.move(i, x, h)
		} else {
			w.use(i, x)
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
		w.catch(x, mode, h)
		return
	case *syntax.CallExpr:
		if w.call(x, mode, h) {
			return
		}
	case *syntax.FuncLit:
		var takes []int
		if s, ok := w.info.Literals[x]; ok {
			takes = s.Takes
		}
		var params *syntax.FieldList
		if x.Type != nil {
			params = x.Type.Params
		}
		w.frame(x.Body, takes, w.paramVars(params), w.name, "this function literal", false)
		return
	case *syntax.IndexExpr:
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			if _, fn := w.info.Uses[id].(*Func); fn {
				w.funcValue(id, w.info.Types[x]) // f[T] as a value
				return
			}
		}
		if mode == mMove {
			w.placeMove(x, "an element", "it is an element of %s, lent by the index; an element cannot be moved or taken out by index (resources R35, R36); hand off %s whole, or lend the element")
		}
		w.children(x)
	case *syntax.SelectorExpr:
		if w.selector(x, mode) {
			return
		}
		w.children(x)
	case *syntax.TupleExpr, *syntax.CompositeLit:
		if mode == mMove {
			// R14: a value built to be handed off moves its parts.
			w.parts(x, h)
			return
		}
		if _, lit := x.(*syntax.CompositeLit); lit && w.tracked(w.info.Types[x]) {
			// R14: a literal moves its parts, also one lent where it is
			// made: its statement owns it (R43).
			w.parts(x, hand{at: x, how: "it moves into the literal"})
			break
		}
		w.children(x)
	default:
		w.children(x)
	}
	w.produced(x, mode)
}

// fieldIndex is the index of the record field x selects, or -1 when x is
// no field of a record value (a method, an operation, a package member).
func (w *ownWalk) fieldIndex(x *syntax.SelectorExpr) int {
	t := w.info.Types[x.X]
	if t == nil {
		return -1
	}
	s, ok := under(t).(*Struct)
	if !ok {
		return -1
	}
	for k, f := range s.Fields {
		if f.Name == x.Sel.Name {
			return k
		}
	}
	return -1
}

// rootSlot is the slot of the local a place x.f is selected from.
func (w *ownWalk) rootSlot(x syntax.Expr) (*syntax.Ident, int, bool) {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil, 0, false
	}
	v, _ := w.info.Uses[id].(*Var)
	i, ok := w.slot[v]
	if v == nil || !ok {
		return nil, 0, false
	}
	return id, i, true
}

// placeMove refuses a hand-off of a place that holds a resource: a field
// (R39) or an element (R35). A generic-only one is a copy site (A3).
func (w *ownWalk) placeMove(x syntax.Expr, what, format string) {
	t := w.info.Types[x]
	if !w.tracked(t) {
		return
	}
	var root syntax.Expr
	switch p := x.(type) {
	case *syntax.IndexExpr:
		switch under(w.info.Types[p.X]).(type) {
		case *Slice, *Map:
		default:
			return
		}
		root = p.X
	case *syntax.SelectorExpr:
		root = p.X
	}
	if w.genericOnly(t) {
		w.copySite(t, x, fmt.Sprintf("%s %s is handed off", what, placeText(x)), false)
		return
	}
	w.report(x, "%s cannot be handed off: "+format, placeText(x), exprText(root), exprText(root))
}

// placeText is a place for a message: b.r, xs[…].
func placeText(x syntax.Expr) string {
	if ix, ok := unparen(x).(*syntax.IndexExpr); ok {
		return exprText(ix.X) + "[…]"
	}
	return exprText(x)
}

// selector evaluates a field x.f of a record; it reports false for any
// other selector. The field is lent from its root, and a field taken in
// place cannot be used again (R39, Gap 38).
func (w *ownWalk) selector(x *syntax.SelectorExpr, mode ownMode) bool {
	k := w.fieldIndex(x)
	if k < 0 {
		return false
	}
	if mode == mMove {
		w.placeMove(x, "a field", "it is a field of %s; a field cannot be moved out of its record (resources R39); move %s whole, or lend the field")
	}
	id, i, ok := w.rootSlot(x.X)
	if !ok {
		w.expr(x.X, mLend, hand{})
		return true
	}
	w.useRoot(i, id)
	if w.st == nil || k >= 64 {
		return true
	}
	if s := w.st[i]; s.live() && (s.fields|s.maybe)&(1<<k) != 0 {
		w.refuse(i, x.Sel, fmt.Sprintf("%s is used after it is taken", exprText(x)), "%s was taken in place (on this path or another) and cannot be used after that (resources R39, R20)", exprText(x))
	}
	return true
}

// fieldTake takes the field x.f of an owned record in place, as the value
// a consuming call is on (`b.r.Close()`): the field is marked, and the
// record's release skips it (R39, Gap 38). It reports false when x is no
// field of a local; the caller then hands x off as a whole value.
func (w *ownWalk) fieldTake(x syntax.Expr, h hand) bool {
	sel, ok := unparen(x).(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	k := w.fieldIndex(sel)
	id, i, ok := w.rootSlot(sel.X)
	if k < 0 || !ok || !w.tracked(w.info.Types[sel]) {
		return false
	}
	w.useRoot(i, id)
	if w.st == nil {
		return true
	}
	s := w.st[i]
	l := w.vars[i]
	switch {
	case !s.live():
		return true // reported by useRoot
	case k >= 64:
		w.report(sel, "%s has more than 64 fields, so its field %s cannot be taken in place; take %s whole", id.Name, sel.Sel.Name, id.Name)
	case (s.fields|s.maybe)&(1<<k) != 0:
		w.refuse(i, sel.Sel, fmt.Sprintf("%s is taken twice", exprText(sel)), "%s was taken in place (on this path or another) and cannot be taken again (resources R39, R20)", exprText(sel))
	case l.depth != w.depth:
		w.refuse(i, sel, fmt.Sprintf("%s takes %s, which may run more than once", w.nestedText(), exprText(sel)), "%s cannot take %s (%s): %s belongs to the enclosing function, which releases it, and the block may run at another time or more than once (resources R4, R30)", w.nestedText(), exprText(sel), h.how, id.Name)
	case l.lent && w.infer && l.param > 0 && w.c.infersTakes(w.fn):
		w.promote[l.param-1] = true
	case l.lent:
		w.refuse(i, sel, fmt.Sprintf("%s is lent and its field is taken", id.Name), "%s is lent to %s, so its field %s cannot be taken (%s) (resources R10, R17, R39); write takes %s in the signature", id.Name, w.name, sel.Sel.Name, h.how, id.Name)
	default:
		w.st[i].fields |= 1 << k
		if w.st[i].fieldAt == nil {
			w.st[i].fieldAt = sel
		}
	}
	return true
}

// children lends each operand of x.
func (w *ownWalk) children(x syntax.Expr) {
	syntax.Inspect(x, func(n syntax.Node) bool {
		if n == syntax.Node(x) {
			return true
		}
		if e, ok := n.(syntax.Expr); ok && !w.c.typeNodes[e] {
			w.expr(e, mLend, hand{})
		}
		return false
	})
}

// parts moves the parts of a tuple or composite literal that is handed
// off: a field's or element's value (not a field name or a map key, which
// is plain, R40), and a `..base`.
func (w *ownWalk) parts(x syntax.Expr, h hand) {
	var elts []syntax.Expr
	switch x := x.(type) {
	case *syntax.TupleExpr:
		elts = x.Elts
	case *syntax.CompositeLit:
		w.expr(x.Base, mMove, h)
		elts = x.Elts
	}
	for _, e := range elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if _, field := kv.Key.(*syntax.Ident); !field {
				w.expr(kv.Key, mLend, hand{})
			}
			e = kv.Value
		}
		w.expr(e, mMove, h)
	}
}

// produced owns or refuses a new value x of a resource type: a call's
// result or a literal. A lent one is a temporary of the statement (R43);
// a dropped one is discarded (R42); a moved one has its new owner.
func (w *ownWalk) produced(x syntax.Expr, mode ownMode) {
	switch x.(type) {
	case *syntax.CallExpr, *syntax.CompositeLit:
	default:
		return
	}
	t := w.info.Types[x]
	if !w.tracked(t) || w.st == nil {
		return
	}
	switch mode {
	case mLend:
		w.acquire(ownLocal{temp: x})
	case mDiscard:
		if w.genericOnly(t) {
			return // a generic body may drop a T (plan 1.3); B2 releases it
		}
		w.report(x, "the %s this produces is discarded, and a resource has one owner; bind it with let, so its scope releases it, or hand it off (resources R42)", t)
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
			if mode != mMove && w.converts(a, w.info.Types[x]) {
				// R23: the interface value owns it; the statement owns
				// the interface value (R43).
				w.expr(a, mMove, hand{at: a, how: fmt.Sprintf("converting it to %s moves it", w.info.Types[x])})
				continue
			}
			w.expr(a, mode, h)
		}
		return true
	case CallWith:
		for _, a := range x.Args {
			w.expr(a, mLend, hand{})
		}
		if x.Trailing != nil {
			w.block(x.Trailing, x.Trailing.List)
		}
		return false
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
	takes := []int(nil)
	if sig != nil {
		takes, selfTaken = sig.Takes, sig.TakesSelf
	}
	if call.Kind == CallFunc && call.Func != nil {
		takes = takesOf(call.Func)
		if len(call.TypeArgs) > 0 {
			w.instance(x, call.Func, call.TypeArgs)
		}
	}
	for _, i := range takes {
		taken[i] = true
	}
	closes := w.info.Closes[x] != nil || w.info.Closes[x.Fun] != nil
	if closes {
		selfTaken = true
		if call.Kind == CallFunc {
			taken[0] = true
		}
	}
	type lend struct {
		i  int
		id *syntax.Ident
	}
	var lends []lend
	operand := func(a syntax.Expr, take, recv bool, pname string, pt Type) {
		if take {
			how := fmt.Sprintf("%s takes %s", name, pname)
			if pname == "" || closes {
				how = fmt.Sprintf("%s takes it", name)
			}
			if recv && w.fieldTake(a, hand{at: a, how: how}) {
				return // a field taken in place (R39, Gap 38)
			}
			w.expr(a, mMove, hand{at: a, how: how})
			return
		}
		if w.converts(a, pt) {
			// R23: a resource converted to an operation type with Close
			// moves into the interface value.
			w.expr(a, mMove, hand{at: a, how: fmt.Sprintf("converting it to %s moves it", pt)})
			return
		}
		if id, ok := unparen(a).(*syntax.Ident); ok {
			if v, _ := w.info.Uses[id].(*Var); v != nil {
				if i, ok := w.slot[v]; ok {
					if w.st != nil && w.st[i].st == ownOwned {
						lends = append(lends, lend{i, id})
					}
				}
			}
		}
		w.expr(a, mLend, hand{})
	}
	pname := func(i int) string {
		if sig != nil && i < len(sig.Params) {
			return sig.Params[i].Name
		}
		return ""
	}
	ptype := func(i int) Type {
		if sig != nil && i < len(sig.Params) {
			return sig.Params[i].Type
		}
		return nil
	}
	off := 0
	switch {
	case call.Recv != nil && call.Kind == CallFunc:
		operand(call.Recv, taken[0], true, pname(0), nil)
		off = 1
	case call.Recv != nil:
		operand(call.Recv, selfTaken, true, "", nil)
	case call.Kind == CallFunc:
		// The callee is a function's name: no value of its own.
	default:
		w.expr(x.Fun, mLend, hand{})
	}
	for i, a := range x.Args {
		operand(a, taken[i+off], closes, pname(i+off), ptype(i+off))
	}
	if w.st != nil {
		for _, l := range lends {
			if s := w.st[l.i]; s.st == ownMoved || s.st == ownMaybe {
				if s.at != nil && s.at.Pos() >= x.Pos() && s.at.End() <= x.End() {
					w.refuse(l.i, l.id, fmt.Sprintf("%s is lent and handed off in one call", l.id.Name), "%s is lent to %s here and handed off at %s (%s) in the same call (resources R17); hand it off in a statement of its own", l.id.Name, name, w.pos(s.at), s.how)
				}
			}
		}
	}
	if x.Trailing != nil {
		var takes []int
		if s, ok := w.info.Literals[x.Trailing]; ok {
			takes = s.Takes
		}
		w.frame(x.Trailing, takes, nil, w.name, fmt.Sprintf("the block passed to %s", calleeText(x)), false)
	}
	return false
}

// converts reports whether a, a value that holds a resource and is no
// interface value, is converted to the operation type t (R23).
func (w *ownWalk) converts(a syntax.Expr, t Type) bool {
	if t == nil {
		return false
	}
	if _, tp := t.(*TypeParam); tp {
		return false
	}
	if _, ok := under(t).(*Interface); !ok || !w.tracked(t) {
		return false
	}
	at := w.info.Types[a]
	if !w.tracked(at) {
		return false
	}
	_, iface := under(at).(*Interface)
	return !iface
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
