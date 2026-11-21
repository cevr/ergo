package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Moves (V7, VC1). A read of a place rooted at a let mut or a borrow that
// is kept is copied when a write through the root can follow it (V2). Two
// such reads need no copy, because the write that follows is the whole
// place's own overwrite, which leaves the old value to the read alone:
//
//   - a take: `let got = P` whose next statement is `P = e`, with e a
//     literal of fresh parts that names no variable of P's root and calls
//     nothing (`let got = p.out; p.out = []Event{}`). Nothing runs
//     between the read and the overwrite, so no other binding reads or
//     writes P's old value: got holds it alone;
//   - a reslice: `P = P[a:b]` (any depth of slicing) on one place P. The
//     old value of P is overwritten by a part of itself, so the array
//     stays held by P alone (`c.free = c.free[:n-1]`, `xs = xs[1:]`).
//     The reslice is OWN1's own-reslice (storeread.go, reslices), one
//     rule: its bounds write no part of the root, and a test build lowers
//     it to the checking form (shortcuts.go).
//
// P is a place: a variable, its fields, or a borrow's pointee (no index:
// an element's place moves with its index). By the rule's invariant, a
// value that sits in a place is held by that place alone: every kept read
// of a root that a write may follow was copied (V2), and every value that
// enters a root was fresh or copied (V1). A move keeps the invariant: the
// old value leaves the place as the place gets a new one.
//
// A moved read is fresh where it is bound (fresh), so `let mut got = P`
// copies nothing either, and a function that returns got returns a fresh
// value. The reference is valueLowering "copy" (valueOwned): it copies at
// both. TestValueRuleAgrees holds the rule and the reference to the value
// model on programs that take and reslice through a let mut and through a
// borrow, with a write after each.
//
// Prior art: Rust's mem::take and mem::replace, Swift's consume of a
// stored property followed by its reinitialization, and C++'s
// std::exchange hand the old value over with no copy; Go's slice
// self-assignment x = x[1:] shares the array by definition. Gleam, Elixir
// and ReScript have immutable lists, so a take is a rebinding and costs
// nothing.

// decideMoves marks the moved reads of the body (vf.moved), after
// classify and before the escapes (V2).
func (w *valueWalk) decideMoves() {
	if !valueOwned() || valueMutant == "never-copy" {
		return
	}
	for _, vv := range w.order {
		if !vv.root() {
			continue
		}
		for _, o := range vv.occs {
			if o.kind != occEscape {
				continue
			}
			if w.takeOf(vv, o) || w.reslices(o.path, vv) || w.lateTake(vv, o, nil) {
				w.vf.moved[o.path] = true
				w.l.stats.Moves++
			}
		}
	}
	// A late take of a field of an owned parameter, which its bind takes
	// into a let mut whole (ownedBind, fieldflow.go).
	for _, b := range w.ownedBinds() {
		for _, o := range b.param.occs {
			if o.kind == occEscape && o.path != b.bind && w.lateTake(b.into, o, b.param) {
				w.vf.moved[o.path] = true
				w.l.stats.Moves++
			}
		}
	}
}

// placeOf reports x as a place: a variable, its fields, or a pointee,
// rooted at the variable v.
func (w *valueWalk) placeOf(x syntax.Expr, v *check.Var) bool {
	for {
		switch e := x.(type) {
		case *syntax.Ident:
			return w.info.Uses[e] == v
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.StarExpr:
			x = e.X
		default:
			return false
		}
	}
}

// takeOf reports o, a kept read of the root vv, as the value of
// `let got = P` whose next statement overwrites P with a fresh literal.
func (w *valueWalk) takeOf(vv *vvar, o *vocc) bool {
	if !w.placeOf(o.path, vv.v) {
		return false
	}
	spec, ok := w.parentOf(o.path).(*syntax.VarSpec)
	if !ok || spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Values[0] != o.path {
		return false
	}
	decl, ok := w.parentOf(spec).(*syntax.VarDecl)
	if !ok || len(decl.Specs) != 1 {
		return false
	}
	stmt, ok := w.parentOf(decl).(*syntax.DeclStmt)
	if !ok {
		return false
	}
	block, ok := w.parentOf(stmt).(*syntax.BlockStmt)
	if !ok {
		return false
	}
	for i, s := range block.List {
		if s != syntax.Stmt(stmt) {
			continue
		}
		if i+1 >= len(block.List) {
			return false
		}
		next, ok := block.List[i+1].(*syntax.AssignStmt)
		if !ok || next.Tok != syntax.ASSIGN || len(next.Lhs) != 1 || len(next.Rhs) != 1 {
			return false
		}
		return w.placeOf(next.Lhs[0], vv.v) && samePlace(next.Lhs[0], o.path) && w.plainFresh(next.Rhs[0], vv.v)
	}
	return false
}

// plainFresh reports e as a fresh value whose evaluation calls nothing,
// indexes nothing and names no variable of root v: a literal of fresh
// parts.
func (w *valueWalk) plainFresh(e syntax.Expr, v *check.Var) bool {
	ok := w.fresh(e)
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr, *syntax.IndexExpr, *syntax.SliceExpr, *syntax.FuncLit, *syntax.StarExpr:
			ok = false
		case *syntax.Ident:
			if w.info.Uses[n] == v {
				ok = false
			}
		}
		return ok
	})
	return ok
}

// moveOver (wf10 ST X1) lets a bind take a dead, unshared local of a Map
// or slice type whole, `let mut next = all`, when the only uses that keep
// it from moving are earlier pure calls that keep a part of it in their
// result (`let s = shelfOf(all, k)`, alias.go): their results are copied
// instead (vf.clone[o.call], as V2 copies a result), so they share nothing
// with the local, and the bind moves it. A store's sync.Modify that reads
// one entry and writes one key back then copies the entry, not the whole
// Map (pinata's payments: 83% of the bytes a request allocated). Each such
// result is a part of the local, not the whole: its type reaches no Map or
// slice of the local's own type and hides no part (a function value). And
// each call runs at most once per bind: it is in no loop the bind is
// outside.
//
// The reference is valueLowering "copy" (valueOwned), which copies the
// local at the bind; TestValueRuleAgrees holds both to the value model.
func (w *valueWalk) moveOver(e syntax.Expr) {
	if !valueOwned() || w.fresh(e) {
		return
	}
	id, ok := unparen(e).(*syntax.Ident)
	if !ok {
		return
	}
	v, ok := w.info.Uses[id].(*check.Var)
	if !ok {
		return
	}
	vv := w.varOf(v)
	if !vv.local || vv.ptr || !vv.mut && !vv.handed {
		return
	}
	var own string
	switch u := underEqual(w.typeOf(id)).(type) {
	case *check.Map, *check.Slice:
		own = w.l.typeKey(u)
	default:
		return
	}
	var use *vocc
	for _, o := range vv.occs {
		if o.id == id {
			use = o
		}
	}
	if use == nil {
		return
	}
	var calls []*syntax.CallExpr
	for _, o := range vv.occs {
		if o == use || o.kind != occEscape && o.kind != occReturn || w.vf.clone[o.path] || w.vf.handed[o.path] {
			continue
		}
		if o.pre > use.pre || w.exclusive(o.id, use.id) {
			// freshPath decides whether a later use stops the move (one
			// in another arm, or past a return, does not).
			continue
		}
		if o.kind != occEscape || !o.result || o.call == nil || o.frame != use.frame || w.inFlight(vv, o) {
			return
		}
		for _, l := range o.loops {
			if !containsNode(use.loops, l) {
				return
			}
		}
		set := map[string]bool{}
		if w.l.reaches(w.typeOf(o.call), set, map[check.Type]bool{}) || set[own] {
			return
		}
		calls = append(calls, o.call)
	}
	var marked []*syntax.CallExpr
	for _, c := range calls {
		if !w.vf.clone[c] {
			w.vf.clone[c] = true
			marked = append(marked, c)
		}
	}
	if !w.fresh(e) {
		// Something else stops the move: the results stay uncopied.
		for _, c := range marked {
			delete(w.vf.clone, c)
		}
		return
	}
	w.l.stats.ResultCopies += len(marked)
}

// resliceOf reports the read e of the root vv as the value of
// `P = P[a:b]` on one place P: a variable, its fields or a pointee. The
// bound rule and the shortcut are reslices' (storeread.go), which asks
// this.
func (w *valueWalk) resliceOf(vv *vvar, e syntax.Expr) bool {
	s, ok := unparen(e).(*syntax.SliceExpr)
	if !ok {
		return false
	}
	base := syntax.Expr(s)
	for {
		x, ok := unparen(base).(*syntax.SliceExpr)
		if !ok {
			break
		}
		base = x.X
	}
	if !w.placeOf(base, vv.v) {
		return false
	}
	parent, child := w.unparenParent(e)
	stmt, ok := parent.(*syntax.AssignStmt)
	if !ok || stmt.Tok != syntax.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 || syntax.Node(stmt.Rhs[0]) != child {
		return false
	}
	return w.placeOf(stmt.Lhs[0], vv.v) && samePlace(stmt.Lhs[0], base)
}
