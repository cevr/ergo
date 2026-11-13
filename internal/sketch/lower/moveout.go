package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The entry move-out (V9; values.go, header). A Map entry has no write
// path of its own (V8), so a program changes one by reading it into a let
// mut, writing that and storing it back:
//
//	let mut kids = Map[int, int]{}
//	match x.kids.Get(parent) {
//	case Some[Map[int, int]] as m:
//		kids = m.Value
//	case None:
//	}
//	kids[id] = slot
//	x.kids[parent] = kids
//
// Under V8 alone the bind kids = m.Value copies the entry, since another
// binding may share it. The move-out takes the entry itself instead, and
// the writes reach it in place, as Swift's _modify accessor and Lean's
// Array.modify do on a unique value. That is sound when nothing else
// reaches the entry's storage, which V9 makes true of every Map of a type
// the program moves out of (an entry-written type, markEntryWrites):
//
//   - a copy of such a Map copies its entries (cloneOf), as a slice of
//     slices copies its elements;
//   - a value Put stores in one is unshared (freshened), as Append's
//     element is (an index write's already is: V1);
//   - a Get of one whose result is kept copies the entry it gives
//     (getMatch), or its receiver when the Option is kept whole; a Get
//     whose result keeps nothing (== None) copies nothing;
//
// and when, at the site, nothing reads the entry between the read and the
// store: the store R[K] = X follows the match in the same statement list;
// the statements between do not name R's root, cannot fail or leave
// (return, break, continue, try, a function literal), and use X only in
// uses that keep nothing or write through X; X is a let mut of that list,
// dead after the store; K is a literal or an immutable variable, and R a
// path of fields from a let mut or a borrow. Should a statement between
// panic, the program ends (no recover), so no reader sees a half-written
// entry.
//
// The reference is copyLowering "whole" (no V8, no V9). The mutants of
// the lower ledger named mapcopy-move-* switch each condition off.

// moveOut is one read-modify-write of an entry: the match on R.Get(K)
// whose Some arm is X = v.Value (or X = x), and the store R[K] = X.
type moveOut struct {
	list  []syntax.Stmt
	at    int // the match's index in list
	back  int // the store's index in list
	match syntax.Stmt
	get   *syntax.CallExpr
	recv  syntax.Expr // R
	key   syntax.Expr // K
	bind  syntax.Expr // v.Value or x, the value of X's bind in Some's arm
	some  *check.Var  // v or x
	x     *check.Var  // X
	store *syntax.AssignStmt
}

// stmtLists are the statement lists under n: blocks and case bodies.
func stmtLists(n syntax.Node) [][]syntax.Stmt {
	var out [][]syntax.Stmt
	syntax.Inspect(n, func(c syntax.Node) bool {
		switch c := c.(type) {
		case *syntax.BlockStmt:
			out = append(out, c.List)
		case *syntax.CaseClause:
			out = append(out, c.Body)
		}
		return true
	})
	return out
}

// fieldPath reports x as a path of fields from a variable: v, v.f, v.f.g.
func fieldPath(x syntax.Expr) bool {
	for {
		switch e := unparen(x).(type) {
		case *syntax.Ident:
			return true
		case *syntax.SelectorExpr:
			x = e.X
		default:
			return false
		}
	}
}

// sameKey reports two keys that are the same literal, or the same path of
// fields from the same variable (k, m.Thread).
func sameKey(info *check.Info, a, b syntax.Expr) bool {
	switch x := unparen(a).(type) {
	case *syntax.Ident:
		y, ok := unparen(b).(*syntax.Ident)
		return ok && info.Uses[x] != nil && info.Uses[x] == info.Uses[y]
	case *syntax.SelectorExpr:
		y, ok := unparen(b).(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && sameKey(info, x.X, y.X)
	case *syntax.BasicLit:
		y, ok := unparen(b).(*syntax.BasicLit)
		return ok && x.Kind == y.Kind && x.Value == y.Value
	}
	return false
}

// matchOf is the match statement s, written alone or as an expression
// statement.
func matchOf(s syntax.Stmt) *syntax.TypeSwitchStmt {
	switch s := s.(type) {
	case *syntax.TypeSwitchStmt:
		return s
	case *syntax.ExprStmt:
		if m, ok := unparen(s.X).(*syntax.TypeSwitchStmt); ok {
			return m
		}
	}
	return nil
}

// moveOutsIn are the shapes of a move-out in list, by syntax and types
// alone; decideMoveOuts checks the rest.
func moveOutsIn(info *check.Info, list []syntax.Stmt) []moveOut {
	var out []moveOut
	for j, s := range list {
		m := matchOf(s)
		if m == nil || len(m.Subjects) != 1 {
			continue
		}
		call, ok := unparen(m.Subjects[0].X).(*syntax.CallExpr)
		if !ok || call.Trailing != nil {
			continue
		}
		c := info.Calls[call]
		if c == nil || c.Kind != check.CallFunc || !preludeGet(c.Func) {
			continue
		}
		ops := callOperands(call, c)
		if len(ops) != 2 || !fieldPath(ops[0]) {
			continue
		}
		some, none, ok := getArmsOf(info, m.Clauses)
		if !ok || some.clause == none.clause || len(some.clause.Body) != 1 {
			continue
		}
		bind, ok := some.clause.Body[0].(*syntax.AssignStmt)
		if !ok || bind.Tok != syntax.ASSIGN || len(bind.Lhs) != 1 || len(bind.Rhs) != 1 {
			continue
		}
		xid, ok := unparen(bind.Lhs[0]).(*syntax.Ident)
		if !ok {
			continue
		}
		x, _ := info.Uses[xid].(*check.Var)
		var bound *check.Var
		switch r := unparen(bind.Rhs[0]).(type) {
		case *syntax.SelectorExpr:
			// Some has one field: any selector on its binding is its value.
			if id, ok := unparen(r.X).(*syntax.Ident); ok && some.as != nil && info.Uses[id] == some.as {
				bound = some.as
			}
		case *syntax.Ident:
			if some.value != nil && info.Uses[r] == some.value {
				bound = some.value
			}
		}
		if x == nil || bound == nil {
			continue
		}
		for k := j + 1; k < len(list); k++ {
			st, ok := list[k].(*syntax.AssignStmt)
			if !ok || st.Tok != syntax.ASSIGN || len(st.Lhs) != 1 || len(st.Rhs) != 1 {
				continue
			}
			ix, ok := unparen(st.Lhs[0]).(*syntax.IndexExpr)
			rid, ok2 := unparen(st.Rhs[0]).(*syntax.Ident)
			if !ok || !ok2 || len(ix.Indices) != 1 || info.Uses[rid] != x || !samePlace(ix.X, ops[0]) || !sameKey(info, ix.Indices[0], ops[1]) {
				continue
			}
			out = append(out, moveOut{list: list, at: j, back: k, match: s, get: call, recv: ops[0], key: ops[1], bind: bind.Rhs[0], some: bound, x: x, store: st})
			break
		}
	}
	return out
}

// markEntryWrites records, in the naming pass, the Map types the program
// moves an entry out of (V9): every shape moveOutsIn finds in a function
// body of pkgs. A shape decideMoveOuts refuses still marks its type, which
// only adds copies.
func (l *lowerer) markEntryWrites(pkgs []*check.Package) {
	vf := l.valueFacts()
	vf.entryWrites = map[string]bool{}
	if !valueRule() || !entryRule() {
		return
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for _, f := range l.canon.funcs(p) {
			if f.Decl == nil || f.Decl.Body == nil {
				continue
			}
			for _, list := range stmtLists(f.Decl.Body) {
				for _, mo := range moveOutsIn(p.Info, list) {
					t, _ := typedIn(p.Info, mo.recv)
					if t == nil || len(freeTypeParams(t)) > 0 || !l.holdsValue(elemOf(t)) {
						continue
					}
					vf.entryWrites[t.String()] = true
					vf.entryWrites[underEqual(t).String()] = true
				}
			}
		}
	}
}

// entryWritten reports a Map type whose entries the program writes in
// place (V9): its copies copy the entries, Put's values are unshared and
// a kept Get copies what it gives.
func (l *lowerer) entryWritten(t check.Type) bool {
	if t == nil || !entryRule() {
		return false
	}
	ew := l.valueFacts().entryWrites
	if len(ew) == 0 {
		return false
	}
	if _, ok := underEqual(t).(*check.Map); !ok {
		return false
	}
	return ew[t.String()] || ew[underEqual(t).String()]
}

// keepsNothing reports a Get whose Option only a comparison reads
// (m.Get(k) == None): it keeps no entry.
func (w *valueWalk) keepsNothing(get *syntax.CallExpr) bool {
	parent, _ := w.unparenParent(get)
	b, ok := parent.(*syntax.BinaryExpr)
	return ok && (b.Op == syntax.EQL || b.Op == syntax.NEQ)
}

// matchedGet reports a Get that getMatch lowers: the subject of a match
// whose arms take the comma-ok shapes, so the entry the Get gives can be
// copied where the match binds it.
func (w *valueWalk) matchedGet(get *syntax.CallExpr) bool {
	if getLowering == "switch" || get.Trailing != nil {
		return false
	}
	parent, _ := w.unparenParent(get)
	g, ok := parent.(*syntax.TypeGuard)
	if !ok || unparen(g.X) != syntax.Expr(get) {
		return false
	}
	m, ok := w.parent[g].(*syntax.TypeSwitchStmt)
	if !ok || len(m.Subjects) != 1 {
		return false
	}
	_, _, ok = getArmsOf(w.info, m.Clauses)
	return ok
}

// matchBinds are the bindings the arms of the match on get make of
// Some's value: `Some[V] as v` and `Some{x}`.
func (w *valueWalk) matchBinds(get *syntax.CallExpr) []*check.Var {
	parent, _ := w.unparenParent(get)
	m, ok := w.parent[parent].(*syntax.TypeSwitchStmt)
	if !ok {
		return nil
	}
	some, _, ok := getArmsOf(w.info, m.Clauses)
	if !ok {
		return nil
	}
	var out []*check.Var
	for _, v := range []*check.Var{some.as, some.value} {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

// copiedOnce reports a binding of a matched Get whose entry is copied
// (V9), used once: that use takes the copy, which nothing else holds.
func (w *valueWalk) copiedOnce(v *check.Var) bool {
	return w.vf.copied[v] && len(w.varOf(v).occs) == 1
}

// decideMoveOuts finds the move-outs of the body (V9) that hold, once its
// uses are classified, and marks them: the Get's receiver is a read, and
// the bind of X takes the entry with no copy.
func (w *valueWalk) decideMoveOuts() {
	vf := w.vf
	if !entryRule() || len(vf.entryWrites) == 0 {
		return
	}
	occ := map[*syntax.Ident]*vocc{}
	for _, vv := range w.order {
		for _, o := range vv.occs {
			occ[o.id] = o
		}
	}
	for _, list := range stmtLists(w.root.node) {
		for _, mo := range moveOutsIn(w.info, list) {
			if w.moveOutHolds(mo, occ) {
				vf.moved[mo.bind] = true
				vf.moveRead[mo.get] = true
				rid := pathRoot(mo.recv)
				if o := occ[rid]; o != nil {
					o.kind, o.entry, o.call = occRead, false, mo.get
				}
			}
		}
	}
}

// moveOutHolds checks a move-out's conditions (above) on the classified
// uses.
func (w *valueWalk) moveOutHolds(mo moveOut, occ map[*syntax.Ident]*vocc) bool {
	if !w.l.entryWritten(w.typeOf(mo.recv)) {
		return false
	}
	rid := pathRoot(mo.recv)
	rv, ok := w.info.Uses[rid].(*check.Var)
	if !ok {
		return false
	}
	root := w.varOf(rv)
	ro := occ[rid]
	if !root.root() || ro == nil {
		return false
	}
	frame := ro.frame
	xv := w.varOf(mo.x)
	if !xv.local || !xv.mut || xv.frame != frame {
		return false
	}
	// X is declared in the list, before the match.
	declared := false
	for _, s := range mo.list[:mo.at] {
		if d, ok := s.(*syntax.DeclStmt); ok {
			for _, spec := range d.Decl.Specs {
				for _, id := range spec.Names {
					if w.info.Defs[id] == mo.x {
						declared = true
					}
				}
			}
		}
	}
	if !declared {
		return false
	}
	// The key is a literal, or a path of fields from an immutable variable.
	switch k := unparen(mo.key).(type) {
	case *syntax.BasicLit:
	case *syntax.Ident, *syntax.SelectorExpr:
		id := pathRoot(k)
		kv, ok := w.info.Uses[id].(*check.Var)
		if !ok || !fieldPath(k) || w.varOf(kv).mut || w.varOf(kv).ptr {
			return false
		}
	default:
		return false
	}
	// The binding of the entry is used once: in X's bind.
	if len(w.varOf(mo.some).occs) != 1 {
		return false
	}
	lo, hi := w.post[mo.match], w.pre[mo.store]
	end := w.post[mo.store]
	for _, o := range root.occs {
		if o.pre >= lo && o.pre < hi {
			return false // the root is named between the read and the store
		}
	}
	for _, o := range xv.occs {
		switch {
		case o.pre >= end:
			return false // X lives on past the store
		case o.pre >= lo && o.pre < hi:
			if o.frame != frame || o.kind != occRead && o.kind != occWrite && o.kind != occPart {
				return false // X is kept or shared between
			}
		}
	}
	leaves := false
	for _, s := range mo.list[mo.at+1 : mo.back] {
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.ReturnStmt, *syntax.BranchStmt, *syntax.TryExpr, *syntax.FuncLit:
				leaves = true
			case *syntax.CallExpr:
				if c := w.info.Calls[n]; c == nil || failsHere(c) || n.Trailing != nil {
					leaves = true
				}
			}
			return !leaves
		})
	}
	return !leaves
}
