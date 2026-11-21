package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Consume by parts (REUSE RU1; types.md, Bindings 2). A function owns a
// parameter when every use of it in the body keeps nothing past the
// function, or moves a part of it into the result or into a position
// that consumes it, on paths that do not overlap. The rows here widen
// V5's owned parameters (ownedParams) from one bind or one consumed
// operand to:
//
//   - own-return: a whole `return p` before the one bind of p: that
//     route ends the frame before the bind, so on it p goes back to the
//     caller whole, and on the other p is the bind's alone (row g4 of the
//     REUSE plan, `if … { return s }; let mut t = s; …`).
//
// Prior art: Lean 4 infers each parameter owned or borrowed and reuses a
// destructured value's cell for the value built from it; Rust tracks
// moves out of a struct field by field (partial moves); Erlang/OTP 27
// updates a record in place when its alias analysis finds one reference,
// a pass that was wrong once when two extracts of one value had separate
// lifetimes (GH-9014), which is why the paths here must be disjoint.

// partsTaken is own-parts' admission: a field path of a parameter at a
// position that consumes it (an owned parameter of an Ergo body, an
// owned form's first operand, the source of an Append) takes the
// parameter over as a let mut of that field does (fieldOwned): the body
// would copy the field there anyway, so the copy moves to a caller that
// shares, and a caller that hands its value over pays none. Rows r1, r2
// and r3 of the REUSE plan.
func (w *valueWalk) partsTaken() bool {
	if !w.l.takes("own-parts") {
		return false
	}
	w.l.took("own-parts")
	return true
}

// baseMoved reports whether the occurrence o is the base of a record
// update, T{..p, f: …}, that the body's own frame returns: p's fields
// other than the ones the update names move into the result, so the
// update keeps no other part of p (fieldOwned). baseCovers keeps the
// fields the update names apart from the base.
func (w *valueWalk) baseMoved(o *vocc) bool {
	lit, ok := w.parentOf(o.id).(*syntax.CompositeLit)
	if !ok || lit.Base != syntax.Expr(o.id) || o.frame != w.root || w.returnNode(o, o.id) == nil {
		return false
	}
	return w.partsTaken()
}

// baseCovers is own-move's admission: whether the use base, the base of
// a record update T{..p, g: e}, and the use other, a field path of p
// under a field f, share no storage. The base gives the result only the
// fields the update does not name, so a write through other (p.f
// consumed in place) cannot reach the result through the base, nor the
// base's fields through other, when no field the update keeps can hold
// a Map or slice that f's value holds (mayShare, by type: two fields of
// one record may hold one array). A kept f that holds one is such a
// field itself; a kept f that holds none keeps nothing.
func (w *valueWalk) baseCovers(base, other *syntax.Ident) bool {
	lit, ok := w.parentOf(base).(*syntax.CompositeLit)
	if !ok || lit.Base != syntax.Expr(base) {
		return false
	}
	cur := syntax.Expr(other)
	for {
		sel, ok := w.parentOf(cur).(*syntax.SelectorExpr)
		if !ok || syntax.Expr(sel.X) != cur || w.dotCall(cur) != nil {
			break
		}
		cur = sel
	}
	names := w.fieldNames(cur)
	if len(names) == 0 {
		return false
	}
	named := map[string]bool{}
	for _, el := range lit.Elts {
		if kv, ok := el.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok {
				named[id.Name] = true
			}
		}
	}
	st, ok := underlying(w.typeOf(base)).(*check.Struct)
	if !ok {
		return false
	}
	var moved check.Type
	for _, f := range st.Fields {
		if f.Name == names[0] {
			moved = f.Type
		}
	}
	if moved == nil {
		return false
	}
	for _, f := range st.Fields {
		if !named[f.Name] && w.l.holdsValue(f.Type) && w.l.mayShare(f.Type, moved) {
			return false
		}
	}
	if !w.l.takes("own-move") {
		return false
	}
	w.l.took("own-move")
	return true
}

// selfBases hands over the base of each update stored back into its own
// let mut, `p = T{..p, f: e}` (LWR-SPREAD-SELF), as a rebinding hands its
// argument to a pure body (V7): the old value of p is dead once the
// statement stores, so the result may keep its storage, with no copy.
// Every other use of p in the statement must keep nothing the result
// keeps: a read of a field path whose value holds no Map or slice
// (p.n + 1), or a path under a field the update names (p.xs.Append(v)),
// whose old value the update replaces (baseCovers). A Map or slice read
// from a field the update keeps (`p = T{..p, ys: p.xs}`) would leave two
// fields of the result on one array, and refuses.
func (w *valueWalk) selfBases() {
	if !valueOwned() {
		return
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		s, ok := n.(*syntax.AssignStmt)
		if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 {
			return true
		}
		lit, ok := unparen(s.Rhs[0]).(*syntax.CompositeLit)
		if !ok || lit.Base == nil {
			return true
		}
		lhs, ok := unparen(s.Lhs[0]).(*syntax.Ident)
		base, ok2 := unparen(lit.Base).(*syntax.Ident)
		if !ok || !ok2 {
			return true
		}
		v, ok := w.info.Uses[lhs].(*check.Var)
		if !ok || w.info.Uses[base] != check.Object(v) {
			return true
		}
		vv := w.varOf(v)
		if vv == nil || !vv.mut || vv.ptr {
			return true
		}
		var frame *vframe
		for _, o := range vv.occs {
			if o.id == lhs {
				frame = o.frame
			}
		}
		for _, o := range vv.occs {
			if o.pre < w.preOf(s) || o.pre >= w.postOf(s) || o.id == lhs || o.id == base {
				continue
			}
			if o.frame != frame || !w.scalarRead(o.id) && !w.baseCovers(base, o.id) {
				return true
			}
		}
		w.vf.self[base] = true
		w.vf.handed[base] = true
		return true
	})
}

// scalarRead reports whether the use id heads a field path whose value
// holds no Map or slice: reading it keeps no storage of id's value.
func (w *valueWalk) scalarRead(id *syntax.Ident) bool {
	cur := syntax.Expr(id)
	for {
		sel, ok := w.parentOf(cur).(*syntax.SelectorExpr)
		if !ok || syntax.Expr(sel.X) != cur || w.dotCall(cur) != nil {
			break
		}
		cur = sel
	}
	return cur != syntax.Expr(id) && w.fieldPath(cur) != nil && !w.l.holdsValue(w.typeOf(cur))
}

// returnsBefore reports whether the occurrence o of the parameter that
// bind takes is a whole `return p` of the body's own frame that comes
// before the bind (own-return): that route never reaches the bind.
func (w *valueWalk) returnsBefore(o, bind *vocc) bool {
	if o.frame != w.root || o.pre >= bind.pre {
		return false
	}
	r, ok := w.returnNode(o, o.id).(*syntax.ReturnStmt)
	if !ok || len(r.Results) != 1 || unparen(r.Results[0]) != syntax.Expr(o.id) {
		return false
	}
	if !w.l.takes("own-return") {
		return false
	}
	w.l.took("own-return")
	return true
}
