package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Field flow (OWN1 OWN-BIND, fk-domains' check). A let mut holds its value
// unshared (V1): each part entered it fresh, so two of its fields share no
// storage (update.go, apart). A use of the root through one field cannot
// reach another field's storage, and a rebinding p = T{..p, …} or
// p = h(…, p, …), where h's body uses only some fields of its parameter,
// leaves every other field's storage where it was. Two rules follow, each
// under the value rule's reference (valueLowering=copy):
//
//   - an apart read (apartRead): `let s = p.f` of a let s that keeps
//     nothing needs no copy when no use of p can write f's storage: every
//     other use of p is through another field, a read of f that writes
//     nothing, a return of p from s's frame, a rebinding, the base of a
//     record update p takes, or an operand of a function whose
//     parameter's uses leave f alone;
//   - a late take (lateTake): `let got = p.f` in the body's own block, with
//     a later `p = T{..p, f: e}` there, takes f's value with no copy when
//     every use of p between them carries f unobserved: a use through
//     another field, a rebinding p = T{..p, …} that does not set f, or
//     p = h(…, p, …) where h's parameter leaves f alone. f's old storage
//     sits in p and in got until the update replaces it, and nothing reads
//     it through p, so got holds it alone (moves.go, a take).
//
// What a function's body does with a parameter is touchedFields: the
// fields it selects, when every other use returns the parameter whole or
// is the base of a record update the body returns.
//
// Prior art: Rust's borrow checker splits borrows by disjoint fields of a
// struct, and Swift's exclusivity checking accesses stored properties of
// a local struct independently; both take this disjointness from the
// declared type. Gleam, Elixir and ReScript update records by copying
// the record's top level and share every field they do not set, which
// the rebinding forms here do without the copy.

// touchKey names a parameter of a function by its index (a dot call's
// receiver first).
type touchKey struct {
	fn *check.Func
	i  int
}

// touched is the set of fields a function's body selects of a parameter;
// ok is false when a use of the parameter is not one touchedFields reads.
type touched struct {
	fields map[string]bool
	ok     bool
}

// touchedFields reports the fields of h's parameter i that h's body
// selects: every use of the parameter in the body's own frame is a field
// path, a return of the parameter whole, or the base of a record literal
// the body returns. ok is false for any other use, a use in a closure,
// or a function with no Ergo body.
func (l *lowerer) touchedFields(h *check.Func, i int) (map[string]bool, bool) {
	vf := l.valueFacts()
	key := touchKey{h, i}
	if t, ok := vf.touches[key]; ok {
		return t.fields, t.ok
	}
	vf.touches[key] = touched{}
	fields, ok := l.touchedOf(h, i)
	vf.touches[key] = touched{fields, ok}
	return fields, ok
}

func (l *lowerer) touchedOf(h *check.Func, i int) (map[string]bool, bool) {
	if h == nil || h.Decl == nil || h.Decl.Body == nil || h.Decl.Extern != nil || h.Pkg == nil || h.Pkg.Info == nil || h.Decl.Type == nil || h.Decl.Type.Params == nil {
		return nil, false
	}
	d, info := h.Decl, h.Pkg.Info
	var param *check.Var
	n := 0
	for _, field := range d.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			names = []*syntax.Ident{nil}
		}
		for _, id := range names {
			if n == i && id != nil {
				param, _ = info.Defs[id].(*check.Var)
			}
			n++
		}
	}
	if param == nil {
		return nil, false
	}
	saved := l.info
	l.info = info
	defer func() { l.info = saved }()
	w := newValueWalk(l, info, l.valueFacts(), d)
	w.root = &vframe{node: d, tails: map[syntax.Expr]bool{}}
	tailsOf(d.Body.List, w.root.tails)
	w.walk(d, nil)
	vv := w.vars[param]
	if vv == nil {
		return map[string]bool{}, true
	}
	fields := map[string]bool{}
	for _, o := range vv.occs {
		if o.frame != w.root {
			return nil, false
		}
		if f := w.pathField(o.id); f != "" {
			fields[f] = true
			continue
		}
		if !w.returned(o.id) && !w.returnedBase(o.id) {
			return nil, false
		}
	}
	return fields, true
}

// pathField is the field a use id of a record variable selects first
// (id.f…) as a field path, or "" when the use takes the variable whole.
// storefield.go's firstField also reads through a dereference and does
// not ask for a field path.
func (w *valueWalk) pathField(id *syntax.Ident) string {
	x := syntax.Expr(id)
	for {
		p, ok := w.parentOf(x).(*syntax.ParenExpr)
		if !ok {
			break
		}
		x = p
	}
	sel, ok := w.parentOf(x).(*syntax.SelectorExpr)
	if !ok || sel.X != x || w.dotCall(x) != nil || w.fieldPath(sel) == nil {
		return ""
	}
	return sel.Sel.Name
}

// returned reports x as a value the body's own frame returns: a return's
// result or a tail.
func (w *valueWalk) returned(x syntax.Expr) bool {
	parent, child := w.unparenParent(x)
	if r, ok := parent.(*syntax.ReturnStmt); ok {
		return w.frameOfReturn(r) == w.root && len(r.Results) == 1
	}
	e, ok := child.(syntax.Expr)
	return ok && w.root.tails[e]
}

// returnedBase reports x as the base of a record literal T{..x, …} the
// body's own frame returns.
func (w *valueWalk) returnedBase(x syntax.Expr) bool {
	parent, _ := w.unparenParent(x)
	lit, ok := parent.(*syntax.CompositeLit)
	return ok && lit.Base != nil && unparen(lit.Base) == x && w.returned(lit)
}

// setsField reports whether the record literal lit sets the field f.
func setsField(lit *syntax.CompositeLit, f string) bool {
	for _, el := range lit.Elts {
		if kv, ok := el.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok && id.Name == f {
				return true
			}
		}
	}
	return false
}

// rebinding is the statement p = e that rebinds the root of the whole use
// id, with the form of e that keeps p's fields where they were: a record
// update of p (lit), or a call that takes p as an operand (call, at
// operand i). ok is false for any other use.
type rebinding struct {
	stmt *syntax.AssignStmt
	lit  *syntax.CompositeLit
	call *syntax.CallExpr
	i    int
}

// rebinds reports the whole use id of the root vv as a part of
// vv = T{..vv, …} or vv = h(…, vv, …): the left side, the literal's base
// or the call's operand. The call's other operands do not name vv whole.
func (w *valueWalk) rebinds(vv *vvar, id *syntax.Ident) (rebinding, bool) {
	stmt := w.assignOf(id)
	if stmt == nil || stmt.Tok != syntax.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return rebinding{}, false
	}
	lhs, ok := unparen(stmt.Lhs[0]).(*syntax.Ident)
	if !ok || w.info.Uses[lhs] != check.Object(vv.v) {
		return rebinding{}, false
	}
	switch rhs := unparen(stmt.Rhs[0]).(type) {
	case *syntax.CompositeLit:
		if base, ok := unparen(rhs.Base).(*syntax.Ident); ok && w.info.Uses[base] == check.Object(vv.v) {
			if id == lhs || id == base {
				return rebinding{stmt: stmt, lit: rhs}, true
			}
		}
	case *syntax.CallExpr:
		c := w.info.Calls[rhs]
		if c == nil || c.Kind != check.CallFunc || c.Func == nil || rhs.Trailing != nil {
			break
		}
		at := -1
		for i, op := range callOperands(rhs, c) {
			if x, ok := unparen(op).(*syntax.Ident); ok && w.info.Uses[x] == check.Object(vv.v) {
				if at >= 0 {
					return rebinding{}, false
				}
				at = i
				if id != lhs && x != id {
					return rebinding{}, false
				}
			}
		}
		if at >= 0 && (id == lhs || w.operandOf(rhs, id)) {
			return rebinding{stmt: stmt, call: rhs, i: at}, true
		}
	}
	return rebinding{}, false
}

// assignOf is the assignment statement whose left side is id, or whose
// right side holds id as a literal's base or a call's operand.
func (w *valueWalk) assignOf(id *syntax.Ident) *syntax.AssignStmt {
	x := syntax.Node(id)
	for i := 0; i < 3; i++ {
		switch p := w.parentOf(x).(type) {
		case *syntax.AssignStmt:
			return p
		case *syntax.ParenExpr, *syntax.CompositeLit, *syntax.CallExpr:
			x = p
		default:
			return nil
		}
	}
	return nil
}

// operandOf reports id as a direct operand of call (a dot call's receiver
// or an argument).
func (w *valueWalk) operandOf(call *syntax.CallExpr, id *syntax.Ident) bool {
	c := w.info.Calls[call]
	if c == nil {
		return false
	}
	for _, op := range callOperands(call, c) {
		if unparen(op) == syntax.Expr(id) {
			return true
		}
	}
	return false
}

// keepsField reports whether the rebinding r leaves the storage of p's
// field f where it was, unread: a record update that does not set f, or a
// call whose parameter's uses do not select f.
func (w *valueWalk) keepsField(r rebinding, f string) bool {
	if r.lit != nil {
		return !setsField(r.lit, f)
	}
	fields, ok := w.l.touchedFields(w.info.Calls[r.call].Func, r.i)
	return ok && !fields[f]
}

// apartRead reports whether the kept read o of the let mut vv, the whole
// initializer of a let that keeps nothing, may take its value with no
// copy: o's path goes through vv's field f, and no other use of vv can
// write f's storage.
func (w *valueWalk) apartRead(vv *vvar, o *vocc) bool {
	if o.kind != occEscape || !valueOwned() || !vv.mut || vv.ptr || o.frame != vv.frame || !w.readOnlyLet(o) {
		return false
	}
	f := w.pathField(o.id)
	if f == "" {
		return false
	}
	for _, u := range vv.occs {
		if u == o {
			continue
		}
		if u.frame != vv.frame {
			return false
		}
		switch g := w.pathField(u.id); {
		case g == f:
			if !w.writesNothing(u) {
				return false
			}
		case g != "":
			// Another field's storage (apart).
		case u.kind == occRead && !w.ptrRecv(u):
		case u.kind == occReturn:
			// The frame ends there, and the let with it.
		default:
			r, ok := w.rebinds(vv, u.id)
			if !ok || r.call != nil && !w.keepsField(r, f) {
				return false
			}
		}
	}
	return true
}

// lateTake reports whether o, the read p.f of the let mut vv (or of the
// owned parameter alias its bind takes into vv), may take f's value with
// no copy: o is the value of a one-name let in the body's own block, a
// later statement of that block is vv = T{..vv, f: e}, and every use of
// vv or alias from the let to that update carries f unobserved
// (keepsField): the update's own uses are its base, its left side and
// other fields, so e reads no f either. An alias is read only before
// vv's first use (ownedBind), so it appears in no update of vv.
func (w *valueWalk) lateTake(vv *vvar, o *vocc, alias *vvar) bool {
	d, ok := w.root.node.(*syntax.FuncDecl)
	if !ok || d.Body == nil || vv.frame != w.root || len(o.loops) > 0 || !w.letValue(o) {
		return false
	}
	sel, ok := unparen(o.path).(*syntax.SelectorExpr)
	if !ok || unparen(sel.X) != syntax.Expr(o.id) || w.pathField(o.id) == "" {
		return false
	}
	f := sel.Sel.Name
	stmt := syntax.Node(o.id)
	for stmt != nil && w.parentOf(stmt) != syntax.Node(d.Body) {
		stmt = w.parentOf(stmt)
	}
	if stmt == nil {
		return false
	}
	var over *syntax.AssignStmt
	for _, s := range d.Body.List {
		as, ok := s.(*syntax.AssignStmt)
		if !ok || w.preOf(s) < w.postOf(stmt) || over != nil {
			continue
		}
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		lhs, ok := unparen(as.Lhs[0]).(*syntax.Ident)
		lit, isLit := unparen(as.Rhs[0]).(*syntax.CompositeLit)
		if !ok || !isLit || w.info.Uses[lhs] != check.Object(vv.v) || lit.Base == nil || !setsField(lit, f) {
			continue
		}
		if base, ok := unparen(lit.Base).(*syntax.Ident); !ok || w.info.Uses[base] != check.Object(vv.v) {
			continue
		}
		over = as
	}
	if over == nil {
		return false
	}
	lit := unparen(over.Rhs[0]).(*syntax.CompositeLit)
	from, to := w.postOf(stmt), w.preOf(over)
	for _, root := range []*vvar{vv, alias} {
		if root == nil {
			continue
		}
		for _, u := range root.occs {
			if u.pre < from || u.pre > w.postOf(over) {
				continue
			}
			if u.frame != w.root {
				return false
			}
			if u.pre > to && root == vv {
				// The update itself: its base, its left side and the
				// other fields it sets.
				if g := w.pathField(u.id); g == f || g == "" && !w.isBaseOrLHS(over, lit, u.id) {
					return false
				}
				continue
			}
			switch g := w.pathField(u.id); {
			case g == f:
				return false
			case g != "":
			case root == alias:
				return false
			default:
				r, ok := w.rebinds(vv, u.id)
				if !ok || !w.keepsField(r, f) {
					return false
				}
			}
		}
	}
	return true
}

// isBaseOrLHS reports id as the left side of the update over or the base
// of its literal lit.
func (w *valueWalk) isBaseOrLHS(over *syntax.AssignStmt, lit *syntax.CompositeLit, id *syntax.Ident) bool {
	return unparen(over.Lhs[0]) == syntax.Expr(id) || unparen(lit.Base) == syntax.Expr(id)
}
