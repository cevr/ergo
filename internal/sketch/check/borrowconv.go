package check

import (
	"slices"

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
//	  3. as an argument of a call into Go, where the Go argument rule
//	     (ownership.go) admits a reviewed lent argument and refuses any
//	     other, so a Go interface takes a borrow as an operation type does;
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
// A borrow instantiates a type parameter only as a lend (Borrows 7;
// v1-audit-3 G4; borrowtargs.go): a call whose type argument holds a *T,
// written or inferred, is S108 when the generic body may keep a value of
// that parameter, and a literal of a generic type built over one is S108.
// A function type that takes a borrow holds none (typeArgBorrow).
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
	borrowTypeArgText       = "%s cannot take a borrow as its %s (%s): %s may keep its %s, since %s; a borrow instantiates a type parameter only where the body keeps no value of it (types.md, Borrows 7)"
	borrowValueText         = "this value of %s holds a borrow (*T), and a record or a collection never holds a borrow (types.md, Borrows 7)"
)

// typeArgBorrow reports whether t, a type argument or the type of a value
// that is not a borrow itself, holds a borrow: a *T anywhere in it but in
// a function type's parameters and results (a function value that takes a
// borrow holds none).
func typeArgBorrow(t Type) bool {
	seen := map[Type]bool{}
	var walk func(Type) bool
	walk = func(t Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		switch t.(type) {
		case *Pointer:
			return true
		case *Signature, *RowArg:
			return false
		}
		_, _, _, _, kids := shape(t)
		return slices.ContainsFunc(kids, walk)
	}
	return walk(t)
}

// checkBorrowInstance applies the type argument rule at x: a generic
// function instantiated with a borrow, and a generic value (a record or
// union literal, a map) built over one.
func (w *escWalk) checkBorrowInstance(x syntax.Expr) {
	if inst, tp, a, why := w.borrowInstance(x); why != "" && !w.refusedArgument(x) {
		w.c.errorf(x, CodeType, borrowTypeArgText, inst.Func.Name, tp.Name, a, inst.Func.Name, tp.Name, why)
		return
	}
	if _, ok := x.(*syntax.CompositeLit); ok {
		if t := w.info.Types[x]; t != nil && typeArgBorrow(t) {
			w.c.errorf(x, CodeType, borrowValueText, t)
		}
	}
}

// borrowInstance is the instance at x whose type argument a for tp holds
// a borrow that the body keeps, with why; why is "" when there is none.
func (w *escWalk) borrowInstance(x syntax.Expr) (*Instance, *TypeParam, Type, string) {
	inst := w.info.Instances[x]
	if inst == nil {
		return nil, nil, nil, ""
	}
	for i, a := range inst.TypeArgs {
		if !typeArgBorrow(a) || i >= len(inst.Func.Sig.TParams) {
			continue
		}
		tp := inst.Func.Sig.TParams[i]
		if why := w.keepsTParam(inst.Func, tp); why != "" {
			return inst, tp, a, why
		}
	}
	return nil, nil, nil, ""
}

// refusedArgument reports whether an argument of the call x is itself a
// refused instance: its result holds the borrow only because of that
// refusal, which is the one diagnostic of the root cause.
func (w *escWalk) refusedArgument(x syntax.Expr) bool {
	call, ok := x.(*syntax.CallExpr)
	if !ok {
		return false
	}
	for _, a := range call.Args {
		if _, _, _, why := w.borrowInstance(unparen(a)); why != "" {
			return true
		}
	}
	return false
}

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
	w.checkBorrowInstance(x)
	if to, b := w.borrowConversion(x); to != nil {
		parent, _ := w.parentOf(x)
		if _, entry := parent.(*syntax.ContextEntry); entry {
			return // the entry's own rule
		}
		if w.goArgument(x) {
			return // the Go argument rule (ownership.go): lent or refused
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

// goArgument reports whether x, the node being visited, is an argument of
// a call into Go: a Go function or a method of a Go value. The Go argument
// rule judges a borrow there (ownership.go): a reviewed lent argument
// takes it for the call only, and any other is refused.
func (w *escWalk) goArgument(x syntax.Expr) bool {
	parent, child := w.parentOf(x)
	call, ok := parent.(*syntax.CallExpr)
	if !ok || call.Fun == child {
		return false
	}
	k := w.info.Calls[call]
	if k == nil {
		return false
	}
	host := k.Kind == CallFunc && k.Func != nil && k.Func.Pkg.Host() ||
		k.Kind == CallOp && k.Recv != nil && isHostNamed(w.info.Types[k.Recv])
	return host && slices.Contains(call.Args, child.(syntax.Expr))
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
