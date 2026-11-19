package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Read-only uses of a call's result (MAPCOPY round 4; types.md,
// Bindings). A call's result that may share storage another binding
// writes (a pure call's result that depends on an argument read from a
// root, alias.go; a generic result through a borrow, borrowResult; a Go
// call's result, gocall.go goResult) is copied where it is kept. A use
// that reads it at once keeps nothing:
//   - len(x);
//   - an index read x[i], whose element holds no Map or slice or is
//     itself used read-only (len(x[0]));
//   - a comparison x == y, x < y and the rest;
//   - a field read x.f, whose field holds no Map or slice or is itself
//     used read-only (len(x.xs)) (round 5);
//   - a for loop over x whose body makes no call, takes no address and
//     assigns only locals that hold no Map or slice (readingLoop);
//   - an operand of a call or method call whose result keeps nothing of
//     it (total(x), x.Sum(), syntax.KindOf(tree(k, t), id); readingCall).
//
// Nothing runs between the call and the read: the index, the other
// operand of a comparison or a reading call, and a loop's body make no
// call (callFree), and no assignment or & takes the indexed place. So the result is read before any write
// through the root, and no copy is made, as hand Go writes len(p.part()).
// A write a later operand of the call itself makes is still caught where
// the argument is read (inFlight). Under the reference
// (copyLowering=whole) the result is copied as before;
// TestReadOnlyUsesAgree holds the two equal.
//
// Prior art: Swift's optimizer drops the retain and copy of a value only
// read inside one access (its OSSA "guaranteed" borrow scopes); Rust lets
// a temporary borrowed for len() die at the end of the statement.

// readAtOnce makes o, an operand whose call's result would be copied for
// it, a read when the call's result is used read-only. o.call stays set,
// so o is still copied when a later operand of the call writes the root
// (inFlight).
func (w *valueWalk) readAtOnce(o *vocc, call *syntax.CallExpr) {
	if w.readOnlyUse(call) {
		o.kind = occRead
	}
}

// readOnlyUse reports whether the value x is used only by a read that
// runs at once, under the entry rule.
func (w *valueWalk) readOnlyUse(x syntax.Expr) bool {
	if !entryRule() {
		return false
	}
	parent, child := w.unparenParent(x)
	if call := w.dotCall(child); call != nil {
		// x.f(…): x is the call's first operand.
		parent = call
	}
	switch p := parent.(type) {
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c == nil || syntax.Node(p.Fun) == child {
			return false
		}
		if c.Kind == check.CallLen {
			return true
		}
		return w.readingCall(p, c, child)
	case *syntax.SelectorExpr:
		// A field read x.f: a method value is not one (dotCall above).
		// &x.f writes only the struct, a value: x's fields that share
		// storage are slices and Maps, which the read-only use covers.
		if syntax.Node(p.X) != child {
			return false
		}
		return !w.l.holdsValue(w.typeOf(p)) || w.readOnlyUse(p) // field
	case *syntax.RangeStmt:
		return syntax.Node(p.X) == child && w.readingLoop(p)
	case *syntax.IndexExpr:
		if syntax.Node(p.X) != child || len(p.Indices) != 1 || !w.callFree(p.Indices[0]) || w.placeTaken(p) {
			return false
		}
		return !w.l.holdsValue(w.typeOf(p)) || w.readOnlyUse(p)
	case *syntax.BinaryExpr:
		switch p.Op {
		case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
			other := p.Y
			if syntax.Node(p.Y) == child {
				other = p.X
			}
			return w.callFree(other)
		}
	}
	return false
}

// reachless reports a type whose values reach no storage a callee could
// write: scalars, and slices, Maps, arrays, structs and tuples of them.
// A pointer, a function, an interface, a union or a type parameter may
// reach a root, and so may any kind this does not name. seen holds the
// types under way: a recursive type reaches what its other parts reach.
func reachless(t check.Type, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return true
	}
	seen[t] = true
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Slice:
		return reachless(u.Elem, seen)
	case *check.Array:
		return reachless(u.Elem, seen)
	case *check.Map:
		return reachless(u.Key, seen) && reachless(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if !reachless(e, seen) {
				return false
			}
		}
		return true
	case *check.Struct:
		for _, f := range u.Fields {
			if !reachless(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// callFree reports an expression or a block that makes no call but len
// and conversions, so no write runs while it is evaluated.
func (w *valueWalk) callFree(x syntax.Node) bool {
	free := true
	syntax.Inspect(x, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			c := w.info.Calls[call]
			if c == nil || c.Kind != check.CallLen && c.Kind != check.CallConversion {
				free = false
			}
		}
		return free
	})
	return free
}

// readingCall reports a call that only reads its operand child
// (sum(part(*r)), nums(*r).Sum()): a function whose result keeps nothing
// of the operand, and whose other operands make no call and reach no
// root, so nothing writes the root between the result's call and this
// one, nor in it.
//
// The result keeps nothing when it is basic or does not depend on the
// operand by the result-alias summary (syntax.BlockStmtOf(tree(k, t), b),
// a struct of indices; syntax.Children(tree(k, t), x, []uint32{}), whose
// result is its out; OWN1 BORROW-RET, lane C's typer).
//
// Only a pointer or a callback operand reaches the root (total3(part(*r),
// r) writes r.s first, then reads the slice; a struct holds no borrow,
// and a function literal stored in one escapes, so it cannot write a
// frame it captures, S108); a value parameter is never written
// through, as an Ergo body copies where it writes and a Go call copies its
// operands (gocall.go). So another operand may be any value whose type
// holds no pointer, function, interface or type parameter (reachless:
// dense.ID[Node], a []uint32 literal), and the callee need not be pure
// (round 5: a purity check here was an equivalent mutant, sort.Ints(part(*r))
// and an Ergo wrapper of it agree without it).
func (w *valueWalk) readingCall(p *syntax.CallExpr, c *check.Call, child syntax.Node) bool {
	if c.Kind != check.CallFunc || c.Func == nil {
		return false
	}
	if !basicResult(w.typeOf(p)) {
		j := operandIndex(p, c, child)
		if deps := w.l.resultDeps(c.Func); j < 0 || j >= len(deps) || deps[j] {
			return false
		}
	}
	found := false
	for _, op := range callOperands(p, c) {
		if syntax.Node(op) == child {
			found = true
		} else if !w.callFree(op) || !reachless(w.typeOf(op), map[check.Type]bool{}) {
			return false
		}
	}
	return found
}

// readingLoop reports a for over a slice, an array or a Map whose body
// writes no storage the result may share and keeps nothing of it: it
// makes no call but len and conversions, takes no address, holds no
// function literal or return, and assigns only whole variables that hold
// no Map or slice (t = t + v). A binding that holds a slice dies with its
// iteration. The loop reads the result as Go ranges over it, element by
// element, with no write between.
func (w *valueWalk) readingLoop(p *syntax.RangeStmt) bool {
	l := w.info.Loops[p]
	if l == nil {
		return false
	}
	switch underEqual(w.typeOf(p.X)).(type) {
	case *check.Slice, *check.Array, *check.Map:
	default:
		return false
	}
	ok := w.callFree(p.Body)
	syntax.Inspect(p.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.UnaryExpr:
			if n.Op == syntax.AND {
				ok = false
			}
		case *syntax.FuncLit, *syntax.ReturnStmt:
			ok = false
		case *syntax.AssignStmt:
			for _, lhs := range n.Lhs {
				id, isId := unparen(lhs).(*syntax.Ident)
				if !isId || w.l.holdsValue(w.typeOf(id)) {
					ok = false
				}
			}
		}
		return ok
	})
	return ok
}

// placeTaken reports an index expression that & takes (set(&part(*r)[1])
// writes the result's element). An assignment never writes through a
// call's result: the checker refuses it (S108, 196v).
func (w *valueWalk) placeTaken(ix *syntax.IndexExpr) bool {
	parent, _ := w.unparenParent(ix)
	u, ok := parent.(*syntax.UnaryExpr)
	return ok && u.Op == syntax.AND
}
