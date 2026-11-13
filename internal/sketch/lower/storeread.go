package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A read of a store element into a local let (OWN1, own-read; PA3's
// mdhtml gap). `let b = p.blocks[id]` through a borrow p, or through a let
// mut root, is copied (values.go, needsCopy) because a write through the
// root may follow the read and b keeps its slices: the write could be seen
// through b. When b is a plain let that keeps nothing (every use of b is a
// read that keeps nothing, in b's own frame), b's value never outlives the
// body, so only the root's own writes in the body can reach it; and when
// none of those can write the storage of a slice or Map type b's value
// reaches, the read takes the element as it is. A write is judged by the
// storage it can write:
//
//   - an assignment to a place writes the array or table of each index
//     step of its path (p.blocks[id].kind = v writes p.blocks's []block),
//     and, when the place holds a slice or Map, every type its value
//     reaches (a rebinding p.xs = p.xs.Append(x) may write in place);
//   - &place and a call whose receiver a pointer method borrows write
//     every type the place's value reaches, and the borrow itself passed on
//     (helper(p), p.m()) every type its target reaches;
//   - the source of an in-place Append, Put or Delete writes its own type.
//
// A borrow is the only path to its target's storage: two borrows of a call
// never overlap (types.md, Borrows 2), and each binding holds its own
// slices and Maps (Bindings 1), so no other name of the body writes them.
// Types are compared by their slice and Map types (refown.go, reaches), so
// a write of another []string than b's counts too. A test build copies, as
// the reference does.

// keptRead reports whether the read o of the root vv may take its value
// with no copy: o is the whole initializer of a plain let local that keeps
// nothing, and no use of vv can write the storage that value reaches.
func (w *valueWalk) keptRead(vv *vvar, o *vocc) bool {
	if o.kind != occEscape || !valueOwned() {
		return false
	}
	var local *vvar
	for _, cand := range w.order {
		if cand.init != nil && unparen(cand.init) == unparen(o.path) {
			local = cand
			break
		}
	}
	if local == nil || !local.local || local.mut || local.ptr || local.handed || local.frame != o.frame {
		return false
	}
	for _, u := range local.occs {
		if u.kind != occRead || u.frame != local.frame {
			return false
		}
	}
	read := map[string]bool{}
	if w.l.reaches(w.typeOf(o.path), read, map[check.Type]bool{}) {
		return false
	}
	written := map[string]bool{}
	for _, u := range vv.occs {
		if u == o || w.writesNothing(u) {
			continue
		}
		if w.storageWritten(u, written) {
			return false
		}
	}
	for k := range read {
		if written[k] {
			return false
		}
	}
	if !w.l.takes("own-read") {
		return false
	}
	w.l.took("own-read")
	return true
}

// writesNothing reports a use that writes no storage: a read that keeps
// nothing of a part that is no borrow, or a read kept elsewhere (a value
// entering a root is copied there unless fresh, Bindings 2).
func (w *valueWalk) writesNothing(u *vocc) bool {
	if _, ok := w.typeOf(u.path).(*check.Pointer); ok {
		return false
	}
	switch u.kind {
	case occRead, occEscape, occReturn:
		return !w.ptrRecv(u)
	}
	return false
}

// ptrRecv reports whether u's path is the receiver of a call of a pointer
// method, which borrows it.
func (w *valueWalk) ptrRecv(u *vocc) bool {
	call := w.dotCall(u.path)
	if call == nil {
		return false
	}
	c := w.info.Calls[call]
	return c != nil && c.Kind == check.CallFunc && c.Func != nil && c.Func.Ptr
}

// storageWritten adds to set the slice and Map types the use u can write
// (above), and reports whether it can write storage the types do not name
// (a part that hides its parts).
func (w *valueWalk) storageWritten(u *vocc, set map[string]bool) bool {
	t := w.typeOf(u.path)
	if p, ok := t.(*check.Pointer); ok {
		return w.l.reaches(p.Elem, set, map[check.Type]bool{})
	}
	if w.containers(u.path, set) {
		return true
	}
	if u.kind == occPart {
		return w.l.reaches(t, set, map[check.Type]bool{})
	}
	parent, child := w.unparenParent(u.path)
	if as, ok := parent.(*syntax.AssignStmt); ok && u.kind == occWrite {
		for _, l := range as.Lhs {
			if l == child && !w.l.holdsValue(t) {
				return false
			}
		}
	}
	return w.l.reaches(t, set, map[check.Type]bool{})
}

// containers adds the type of the container of each index step of the
// path x (p.blocks[id].kind: p.blocks's), and reports whether a step's
// container hides its parts.
func (w *valueWalk) containers(x syntax.Expr, set map[string]bool) bool {
	for e := unparen(x); ; {
		switch s := e.(type) {
		case *syntax.SelectorExpr:
			e = unparen(s.X)
			continue
		case *syntax.IndexExpr:
			switch u := underEqual(w.typeOf(s.X)).(type) {
			case *check.Slice, *check.Map:
				set[w.l.typeKey(u)] = true
			case *check.Basic:
			default:
				return true
			}
			e = unparen(s.X)
			continue
		case *syntax.SliceExpr:
			if w.l.reaches(w.typeOf(s.X), set, map[check.Type]bool{}) {
				return true
			}
			e = unparen(s.X)
			continue
		case *syntax.StarExpr:
			e = unparen(s.X)
			continue
		case *syntax.CallExpr:
			if len(s.Args) == 1 {
				e = unparen(s.Args[0]) // a conversion
				continue
			}
		}
		return false
	}
}
