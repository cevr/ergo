package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// Conversions of a borrow (types.md, Borrows 1 and 6).
//
// A borrow (*T) lives only as long as the call that lent it. Its type
// cannot leave a parameter (Borrows 1, placement), but an implicit
// conversion hides it: a *T whose functions provide an operation type's
// operations converts to that type, and the value of the operation type
// still points at the lender's place. So the value a conversion makes from
// a borrow keeps the borrow's lifetime, one rule over every conversion
// site:
//
//	a conversion from a borrow stands only
//	  1. as the value of a local let that binds one name (or _), whose
//	     local then holds the borrow;
//	  2. as a provider of a context literal that is the operand of a with,
//	     which installs it for the with's block only;
//	and every use of a local that holds one is
//	  1. the receiver of an operation call, c.Op(…);
//	  2. the value of a local let that binds one name (or _), whose local
//	     then holds the borrow too;
//	  3. a provider of a context literal that is the operand of a with;
//	with no escaping frame between the local's frame and the use. A local
//	that holds a borrow stands where its borrow may, and no further.
//
// Every other place is S108 at the borrow or the use: a return or block
// result, an argument (a parameter of an operation type may keep its
// value), a field, an element, an Option or union payload, a generic
// instantiation, an assignment, a context value, a conversion again, and a
// capture by a frame that may outlive the call or run on another task
// (task.Go's block). The rule is an allow-list over the parent of the use,
// as the non-escaping function-value rule is (escaping.go), so a position
// the checker gains later is refused until it is listed here.
//
// Prior art: Rust gives `&mut T as &mut dyn Trait` the lifetime of the
// borrow (`dyn Trait + 'a`), so the trait object cannot outlive it; Swift
// refuses to escape a non-escaping closure or an `inout` parameter, and a
// `~Escapable` value converted to an existential stays non-escapable.
// Gleam, Elixir and ReScript have no borrows; Go's interface conversion of
// a pointer escapes it to the heap and is always legal.

const (
	borrowConvText          = "%s is a borrow (*T), and as %s it keeps the borrow's lifetime: bind it with a let and call its operations, never return, store or pass it or convert it again (types.md, Borrows 6)"
	borrowHolderText        = "%s holds a borrow (*T) as %s, which lives only as long as the call that lent it: call its operations or bind it with a let, never return, store or pass it or convert it again (types.md, Borrows 6)"
	borrowHolderCaptureText = "%s holds a borrow (*T) as %s, and this function literal or block may outlive the call that lent it or run on another task; call its operations before it (types.md, Borrows 6)"
	borrowCtxText           = "%s is a borrow (*T), and a context that provides it keeps the borrow's lifetime: provide it only in the context of a with, never in a context value or a local context (types.md, Borrows 6)"
)

// borrowText names a borrow for a message: t, &x, &xs[…].
func borrowText(x syntax.Expr) string {
	if u, ok := unparen(x).(*syntax.UnaryExpr); ok && u.Op == syntax.AND {
		return "&" + placeText(u.X)
	}
	return placeText(x)
}

// borrowConversion is the conversion x makes from a borrow, or nil, with
// the borrow: x, or the element of a tuple x that converts.
func (w *escWalk) borrowConversion(x syntax.Expr) (Type, syntax.Expr) {
	if conv, ok := w.info.Implicit[x]; ok && isPointer(conv.From) {
		return conv.To, x
	}
	for i, conv := range w.info.ImplicitElems[x] {
		if conv.Kind != 0 && isPointer(conv.From) {
			if t, ok := unparen(x).(*syntax.TupleExpr); ok && i < len(t.Elts) {
				return conv.To, t.Elts[i]
			}
			return conv.To, x
		}
	}
	return nil, nil
}

// checkBorrowConv applies the rule at n, the node being visited, before
// its children: a conversion from a borrow, a context entry that provides
// one, and a use of a local that holds one.
func (w *escWalk) checkBorrowConv(n syntax.Node) {
	if w.collecting {
		return
	}
	if e, ok := n.(*syntax.ContextEntry); ok {
		if e.Value != nil && isPointer(w.info.Types[e.Value]) && !w.inWithOperand() {
			w.c.errorf(e.Value, CodeType, borrowCtxText, borrowText(e.Value))
		}
		return
	}
	x, ok := n.(syntax.Expr)
	if !ok {
		return
	}
	if to, b := w.borrowConversion(x); to != nil {
		parent, _ := w.parentOf(x)
		if _, entry := parent.(*syntax.ContextEntry); entry {
			return // the entry's own rule
		}
		if !w.bindsBorrow(x, to) {
			w.c.errorf(b, CodeType, borrowConvText, borrowText(b), to)
		}
		return
	}
	id, ok := x.(*syntax.Ident)
	if !ok {
		return
	}
	v, ok := w.info.Uses[id].(*Var)
	if !ok {
		return
	}
	to, ok := w.borrowHolders[v]
	if !ok {
		return
	}
	if !w.callsOperation(id) && !w.bindsBorrow(id, to) && !w.providesInWith(id) {
		w.c.errorf(id, CodeType, borrowHolderText, id.Name, to)
		return
	}
	if w.escapingFrom(v) != nil {
		w.c.errorf(id, CodeType, borrowHolderCaptureText, id.Name, to)
	}
}

// bindsBorrow reports whether x is the value of a local let of one name,
// and marks that local a holder of the borrow as to.
func (w *escWalk) bindsBorrow(x syntax.Expr, to Type) bool {
	parent, child := w.parentOf(x)
	p, ok := parent.(*syntax.VarSpec)
	if !ok || len(p.Names) != 1 || p.Pattern != nil || len(p.Values) != 1 || p.Values[0] != child {
		return false
	}
	if p.Names[0].Name == "_" {
		return true // a discard keeps nothing
	}
	v, ok := w.info.Defs[p.Names[0]].(*Var)
	if !ok || v.pkg {
		return false
	}
	w.borrowHolders[v] = to
	return true
}

// callsOperation reports whether id, the node being visited, is the
// receiver of an operation call: id.Op(…).
func (w *escWalk) callsOperation(id *syntax.Ident) bool {
	parent, child := w.parentOf(id)
	sel, ok := parent.(*syntax.SelectorExpr)
	if !ok || sel.X != child {
		return false
	}
	for j := len(w.stack) - 1; j > 0; j-- {
		if w.stack[j] != sel {
			continue
		}
		call, ok := w.stack[j-1].(*syntax.CallExpr)
		if !ok || call.Fun != syntax.Expr(sel) {
			return false
		}
		k := w.info.Calls[call]
		return k != nil && k.Kind == CallOp
	}
	return false
}

// providesInWith reports whether x, the node being visited, is the
// provider of a context entry of a with's context literal.
func (w *escWalk) providesInWith(x syntax.Expr) bool {
	parent, child := w.parentOf(x)
	e, ok := parent.(*syntax.ContextEntry)
	return ok && e.Value == child && w.inWithOperand()
}

// inWithOperand reports whether the context entry being visited belongs to
// a context literal that is the operand of a with.
func (w *escWalk) inWithOperand() bool {
	for j := len(w.stack) - 1; j >= 0; j-- {
		lit, ok := w.stack[j].(*syntax.ContextLit)
		if !ok {
			continue
		}
		for k := j - 1; k >= 0; k-- {
			if _, ok := w.stack[k].(*syntax.ParenExpr); ok {
				continue
			}
			call, ok := w.stack[k].(*syntax.CallExpr)
			if !ok {
				return false
			}
			if c := w.info.Calls[call]; c == nil || c.Kind != CallWith || call.Trailing == nil {
				return false
			}
			for _, a := range call.Args {
				if unparen(a) == syntax.Expr(lit) {
					return true
				}
			}
			return false
		}
		return false
	}
	return false
}
