package check

import "ergo.local/prototype/internal/sketch/syntax"

// A declared root owns its handles (resources R17, R39; audit CSX §4.8
// P3). A declared root is a value of a type that implements Closer. A
// handle is a value of an operation type (an actor, a sync.Ref, a queue, a
// provider value), or a value that holds one, that is not itself a
// resource: plain data copies, a resource is tracked by the ownership
// pass, and a handle is shared by every copy of it, so the root's release
// reaches every copy. A handle read from a declared root is therefore a
// lend of that root, as a resource field is (R17, R39): it is called and
// passed on, and not returned, stored, bound by let or handed to a taking
// call; it leaves the root only with the whole root (`T{..b, …}` of a
// taken b), or by a write back to its own place (`b.xs = b.xs.Append(x)`).
// A for-in or a match over a handle of a root binds a lend of that root.
//
// Prior art: Rust's borrow of a field (a `&b.f` cannot outlive b, and a
// field moves out of b only when b is taken); Swift's borrowing parameter
// projections. Gleam, Elixir and ReScript share handles freely: a BEAM
// port belongs to its process, not to a value.

// holdsHandle reports whether t is or holds an operation type's value.
// A function value and a type parameter hold none (a function's captures
// are R33's).
func (c *checker) holdsHandle(t Type) bool {
	if t == nil || isInvalid(t) {
		return false
	}
	id, infer := c.types.intern(t)
	if h, ok := c.handleMemo[id]; ok {
		return h
	}
	if c.handleMemo == nil {
		c.handleMemo = map[int32]bool{}
	}
	c.handleMemo[id] = false // a recursive type: its cycle adds nothing
	h := c.holdsHandle0(t)
	if infer {
		delete(c.handleMemo, id)
	} else {
		c.handleMemo[id] = h
	}
	return h
}

func (c *checker) holdsHandle0(t Type) bool {
	switch t := t.(type) {
	case *Named:
		if t.foreign {
			return false
		}
		o := t.origin()
		switch o.underlying().(type) {
		case *Interface:
			return true
		case *Signature:
			return false
		}
		for _, a := range t.TArgs {
			if c.holdsHandle(a) {
				return true
			}
		}
		return c.holdsHandle(o.underlying())
	case *Slice:
		return c.holdsHandle(t.Elem)
	case *Array:
		return c.holdsHandle(t.Elem)
	case *Map:
		return c.holdsHandle(t.Key) || c.holdsHandle(t.Elem)
	case *Tuple:
		for _, e := range t.Types {
			if c.holdsHandle(e) {
				return true
			}
		}
	case *Struct:
		for _, f := range t.Fields {
			if c.holdsHandle(f.Type) {
				return true
			}
		}
	case *Union:
		for _, v := range t.Variants {
			if c.holdsHandle(v) {
				return true
			}
		}
	case *Interface:
		return true
	}
	return false
}

// declaredRoot reports a type that implements Closer, through a borrow too.
func (c *checker) declaredRoot(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	return ok && c.implementsCloser(n)
}

// declaredHandle reports a place x (a field or an element, through a
// borrow too) that holds a handle and is no resource, and the nearest
// declared root that owns it: a local of a declared root type, a place of
// one, or a binding lent from such a handle.
func (w *ownWalk) declaredHandle(x syntax.Expr) (syntax.Expr, bool) {
	t := w.info.Types[x]
	if t == nil || isInvalid(t) || w.tracked(t) || !w.c.holdsHandle(t) {
		return nil, false
	}
	e := unparen(x)
	for {
		switch p := e.(type) {
		case *syntax.SelectorExpr:
			if !w.isField(p) {
				return nil, false
			}
			e = unparen(p.X)
		case *syntax.IndexExpr:
			pt := w.info.Types[p.X]
			if q, ok := pt.(*Pointer); ok {
				pt = q.Elem
			}
			switch under(pt).(type) {
			case *Slice, *Array, *Map:
			default:
				return nil, false
			}
			e = unparen(p.X)
		default:
			return nil, false
		}
		if id, ok := e.(*syntax.Ident); ok {
			if v, _ := w.info.Uses[id].(*Var); v != nil {
				if i, ok := w.slot[v]; ok && w.vars[i].handle {
					return id, true
				}
			}
		}
		if w.c.declaredRoot(w.info.Types[e]) {
			return e, true
		}
	}
}

// isField reports a selector of a record's field, through a borrow too.
func (w *ownWalk) isField(x *syntax.SelectorExpr) bool {
	t := w.info.Types[x.X]
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := under(t).(*Struct)
	if !ok {
		return false
	}
	for _, f := range s.Fields {
		if f.Name == x.Sel.Name {
			return true
		}
	}
	return false
}

// handleMove refuses a hand-off of a declared root's handle (P3), except
// the write back to its own place that the assignment being evaluated
// makes. It reports whether x is such a handle.
func (w *ownWalk) handleMove(x syntax.Expr, h hand) bool {
	root, ok := w.declaredHandle(x)
	if !ok {
		return false
	}
	if w.writeBack != "" && exprText(x) == w.writeBack {
		return true
	}
	how := h.how
	if how == "" {
		how = "it is handed off"
	}
	if id, ok := root.(*syntax.Ident); ok {
		if v, _ := w.info.Uses[id].(*Var); v != nil {
			if i, ok := w.slot[v]; ok && w.vars[i].handle {
				w.report(x, "%s cannot be handed off (%s): it holds a handle of %s, which %s is lent from, and a handle leaves its declared root only with the whole root (resources R17, R35, R39); lend it, or hand off the root whole", exprText(x), how, w.vars[i].from, id.Name)
				return true
			}
		}
	}
	w.report(x, "%s cannot be handed off (%s): it holds a handle that %s owns, and %s implements Closer, so its release reaches every copy; a handle leaves its root only with the whole root (resources R17, R39); take %s whole, write %s back in place, or lend it", exprText(x), how, exprText(root), exprText(root), exprText(root), exprText(x))
	return true
}

// handleFrom names what a binding of x is lent from: the place x, or what
// the binding x is lent from.
func (w *ownWalk) handleFrom(x syntax.Expr) string {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		if v, _ := w.info.Uses[id].(*Var); v != nil {
			if i, ok := w.slot[v]; ok && w.vars[i].handle {
				return w.vars[i].from
			}
		}
	}
	return exprText(x)
}

// handleSource reports a match subject whose handles are a declared
// root's: a handle place of one, or a binding lent from one.
func (w *ownWalk) handleSource(x syntax.Expr) bool {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		v, _ := w.info.Uses[id].(*Var)
		i, ok := w.slot[v]
		return v != nil && ok && w.vars[i].handle
	}
	_, ok := w.declaredHandle(x)
	return ok
}
