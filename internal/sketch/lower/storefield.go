package lower

import (
	"maps"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A parameter that enters a let mut by a field nothing writes (OWN1,
// own-enter-field; lane C's S3d7 profile). The self-hosted checker's
// types.SolveFuncKinds runs, for each function declaration,
//
//	let mut st = solve{s: s, files: files, gens: []gen{}, …}
//	walk(&st, …)
//
// and a value entering a let mut is copied unless fresh (V1), so every
// declaration deep-copied every parsed file: the checker's cost grew with
// the square of the package. walk writes st.sv and st.gens, never
// st.files.
//
// Such a field's value now enters shared, when:
//   - the root is a local let mut whose initializer is the literal, and
//     the value is a by-value parameter of the function that nothing else
//     takes over (not owned, not read as an unshared local: freshFrom's or
//     freshAll's), or a path of one by fields and call-free indices
//     (stableSource; S3c's bind.bodyOf, binder{p: files[0].Parsed, …}, one
//     syntax tree copied per body task), so no root of the body but this
//     one shares its storage (every other value entering a root is copied,
//     Bindings 1);
//   - each use of the root in its own frame, through that field, is a
//     read that keeps nothing or an assignment of the whole field, whose
//     value enters copied unless it is such a source too (b.p =
//     files[t.file].Parsed enters shared); a use through another field is
//     anything,
//     since that field holds storage of its own; and a use of the whole
//     root is a read that keeps nothing or a borrow (&st, a pointer
//     method's receiver) handed to a function with an Ergo body whose
//     field summary (fieldsWritten) writes neither that field nor the
//     whole value.
//
// Then no write reaches the shared storage while the root lives: the
// field is the root's only path to it, and the root never escapes whole.
// The keeps and result-alias summaries (keeps.go, alias.go) follow a
// value into the let it initializes whatever is copied, so a caller that
// passed the argument unkept still writes nothing during the call.
//
// fieldsWritten is FIELD-BORROW's write summary (storeread_pass.go) by
// field, not by type: the first field each written path selects below the
// parameter, through the calls it passes the parameter on to, to a least
// fixpoint over recursion. A write of the whole value, or a use the walk
// cannot follow, is whole.
//
// The reference is valueLowering "copy"; a test build copies (takes).
// TestHandoverAgrees holds both lowerings to its model (enterBump,
// enterPoke).
//
// Prior art: Swift's OSSA lets a stored property be borrowed for the
// struct's lifetime when its access-path analysis proves no write to that
// path; Rust's borrow checker tracks disjoint fields (`&mut s.sv` beside
// `&s.files`). Gleam, Elixir and ReScript share every field: their data
// is immutable.

// fieldSum is what a function writes through a borrow parameter, by the
// first field below it.
type fieldSum struct {
	fields map[string]bool
	whole  bool
}

func (s fieldSum) same(o fieldSum) bool {
	return s.whole == o.whole && maps.Equal(s.fields, o.fields)
}

// fieldsWritten is the fields f writes through its parameter j (counting a
// dot call's receiver first): the least fixpoint of fieldsBody.
func (l *lowerer) fieldsWritten(f *check.Func, j int) fieldSum {
	vf := l.valueFacts()
	if vf.fieldWrites == nil {
		vf.fieldWrites = newBorrowWrites[fieldSum]()
	}
	return vf.fieldWrites.fix(borrowKey{f, j}, fieldSum{fields: map[string]bool{}}, fieldSum{fields: map[string]bool{}, whole: true}, fieldSum.same, func() fieldSum {
		return l.fieldsBody(f, j)
	})
}

// fieldsBody is one round of f's field summary for parameter j.
func (l *lowerer) fieldsBody(f *check.Func, j int) fieldSum {
	all := fieldSum{fields: map[string]bool{}, whole: true}
	v := declParam(f, j)
	if v == nil {
		return all
	}
	w := l.analyzeValues(f.Decl, f.Pkg.Info)
	if w.root == nil {
		return all
	}
	s := fieldSum{fields: map[string]bool{}}
	vv := w.vars[v]
	if vv == nil {
		return s
	}
	for _, u := range vv.occs {
		if w.writesNothing(u) {
			continue
		}
		if name := w.firstField(u.id); name != "" {
			s.fields[name] = true
			continue
		}
		if g, k := w.passedBorrow(u); g != nil {
			sub := l.fieldsWritten(g, k)
			s.whole = s.whole || sub.whole
			for n := range sub.fields {
				s.fields[n] = true
			}
			continue
		}
		s.whole = true
	}
	return s
}

// firstField is the field a path that starts at id selects first (through
// parentheses and a dereference), or "" for a use of the whole value: id
// alone, or the receiver of a method call.
func (w *valueWalk) firstField(id *syntax.Ident) string {
	var cur syntax.Node = id
	for {
		switch p := w.parentOf(cur).(type) {
		case *syntax.ParenExpr:
			cur = p
			continue
		case *syntax.StarExpr:
			if syntax.Node(p.X) != cur {
				return ""
			}
			cur = p
			continue
		case *syntax.SelectorExpr:
			if syntax.Node(p.X) != cur || w.dotCall(cur) != nil {
				return ""
			}
			return p.Sel.Name
		}
		return ""
	}
}

// borrowedTo is the function and parameter index a use u of a let mut
// hands a borrow of the whole value to: &x as an argument, or x as a
// pointer method's receiver. It is nil for any other use.
func (w *valueWalk) borrowedTo(u *vocc) (*check.Func, int) {
	if c := w.dotCall(u.id); c != nil {
		if g, k := w.passedBorrow(u); g != nil && isPointer(declParam(g, k)) {
			return g, k
		}
		return nil, 0
	}
	un, ok := w.parentOf(u.id).(*syntax.UnaryExpr)
	if !ok || un.Op != syntax.AND || syntax.Node(un.X) != syntax.Node(u.id) {
		return nil, 0
	}
	parent, child := w.unparenParent(un)
	call, ok := parent.(*syntax.CallExpr)
	if !ok || call.Fun == child {
		return nil, 0
	}
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil {
		return nil, 0
	}
	for j := 0; ; j++ {
		a := argOf(call, c, j)
		if a == nil {
			return nil, 0
		}
		if unparen(a) == syntax.Expr(un) {
			if declParam(c.Func, j) == nil {
				return nil, 0
			}
			return c.Func, j
		}
	}
}

func isPointer(v *check.Var) bool {
	if v == nil {
		return false
	}
	_, ok := v.Type.(*check.Pointer)
	return ok
}

// stableParam reports whether src is a by-value parameter of the body's
// function that no rule of the body takes over (above).
func (w *valueWalk) stableParam(src *check.Var) bool {
	d, ok := w.root.node.(*syntax.FuncDecl)
	if !ok || d.Name == nil || d.Type == nil || d.Type.Params == nil || isPointer(src) {
		return false
	}
	if src == w.owner || w.owners[src] {
		return false
	}
	if vv := w.vars[src]; vv == nil || vv.local || vv.mut || vv.ptr || vv.handed {
		return false
	}
	f, ok := w.info.Defs[d.Name].(*check.Func)
	if !ok {
		return false
	}
	for _, p := range w.l.ownedParams(f) {
		if p.v == src {
			return false
		}
	}
	for _, field := range d.Type.Params.List {
		for _, id := range field.Names {
			if w.info.Defs[id] == check.Object(src) {
				return true
			}
		}
	}
	return false
}

// stableSource reports whether val is a stable parameter (stableParam) or
// a path of one: field selections and index reads whose index makes no
// call (bodyOf's files[t.file].Parsed), so it names storage only the
// caller holds, and holds a Map or slice.
func (w *valueWalk) stableSource(val syntax.Expr) bool {
	if val == nil || !w.l.holdsValue(w.typeOf(val)) {
		return false
	}
	for x := val; ; {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			if w.dotCall(e.X) != nil {
				return false
			}
			x = e.X
		case *syntax.IndexExpr:
			if len(e.Indices) != 1 || !w.callFree(e.Indices[0]) {
				return false
			}
			x = e.X
		case *syntax.Ident:
			src, ok := w.info.Uses[e].(*check.Var)
			return ok && w.stableParam(src)
		default:
			return false
		}
	}
}

// fieldAssigned is the value u, a use of a root through one field,
// assigns to that whole field (x.f = e), or nil.
func (w *valueWalk) fieldAssigned(u *vocc) syntax.Expr {
	sel, ok := w.parentOf(u.id).(*syntax.SelectorExpr)
	if !ok || syntax.Node(sel.X) != syntax.Node(u.id) {
		return nil
	}
	s, ok := w.parentOf(sel).(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Lhs[0] != syntax.Expr(sel) {
		return nil
	}
	return s.Rhs[0]
}

// freshenLit freshens the literal lit that initializes the let mut root
// vv, field by field, leaving shared each field sharedField admits.
func (w *valueWalk) freshenLit(lit *syntax.CompositeLit, vv *vvar) {
	if !w.l.holdsValue(w.typeOf(lit)) || w.fresh(lit) {
		return
	}
	if lit.Base != nil {
		w.freshen(lit.Base, whyEnters)
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*syntax.KeyValueExpr)
		if !ok {
			w.freshen(el, whyEnters)
			continue
		}
		if key, ok := kv.Key.(*syntax.Ident); ok && w.sharedField(vv, key.Name, kv.Value) {
			continue
		}
		w.freshen(kv.Value, whyEnters)
	}
}

// sharedField reports whether the value val of the field name of the
// literal that initializes the let mut root vv enters it shared (above).
func (w *valueWalk) sharedField(vv *vvar, name string, val syntax.Expr) bool {
	if !valueOwned() || !vv.local || !vv.mut || vv.ptr || vv.handed {
		return false
	}
	if !w.stableSource(val) {
		return false
	}
	for _, u := range vv.occs {
		if u.frame != vv.frame {
			return false
		}
		if f := w.firstField(u.id); f != "" {
			// A whole-field assignment replaces the storage: its value is
			// copied in the owns loop unless it is a stable source too.
			if f == name && u.kind != occRead && w.fieldAssigned(u) == nil {
				return false
			}
			continue
		}
		if g, k := w.borrowedTo(u); g != nil {
			if s := w.l.fieldsWritten(g, k); s.whole || s.fields[name] {
				return false
			}
			continue
		}
		if u.kind != occRead {
			return false
		}
	}
	if !w.l.takes("own-enter-field") {
		return false
	}
	w.l.took("own-enter-field")
	return true
}
