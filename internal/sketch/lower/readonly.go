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
//   - a comparison x == y, x < y and the rest.
//
// Nothing runs between the call and the read: the index and the other
// operand of a comparison make no call (callFree), and no assignment or
// & takes the indexed place. So the result is read before any write
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
	switch p := parent.(type) {
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		return c != nil && c.Kind == check.CallLen && syntax.Node(p.Fun) != child
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

// callFree reports an expression that makes no call but len and
// conversions, so no write runs while it is evaluated.
func (w *valueWalk) callFree(x syntax.Expr) bool {
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

// placeTaken reports an index expression that & takes (set(&part(*r)[1])
// writes the result's element). An assignment never writes through a
// call's result: the checker refuses it (S108, 196v).
func (w *valueWalk) placeTaken(ix *syntax.IndexExpr) bool {
	parent, _ := w.unparenParent(ix)
	u, ok := parent.(*syntax.UnaryExpr)
	return ok && u.Op == syntax.AND
}
