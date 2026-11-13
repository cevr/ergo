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
//   - an operand of a pure call or method call whose result is basic
//     (total(x), x.Sum(); readingCall).
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

// readingCall reports a call that only reads its operand child: a pure
// function (an Ergo body or a pure std body) whose result holds no Map or
// slice, so it keeps nothing of its operands, and whose other operands
// make no call and are basic values, so nothing writes the root between
// the result's call and this one, nor in it: a pure function still writes
// through a pointer it is given (total3(part(*r), r)) or calls a callback
// (sum(part(*r)), nums(*r).Sum()).
func (w *valueWalk) readingCall(p *syntax.CallExpr, c *check.Call, child syntax.Node) bool {
	if c.Kind != check.CallFunc || c.Func == nil || !(w.l.isPure(c.Func) || pureStdBody(c.Func)) || !basicResult(w.typeOf(p)) {
		return false
	}
	found := false
	for _, op := range callOperands(p, c) {
		if syntax.Node(op) == child {
			found = true
		} else if !w.callFree(op) || !basicResult(w.typeOf(op)) {
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
