package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The prelude's Unreachable(reason) (owner q29, 2026-11-08). A call raises
// a defect that holds the reason and the call's source position, as a Go
// panic: never a returned error, so no catch, a type switch over returned
// errors, sees it (rows.md, Catch 6; runtime.md, Exit causes 7). Its Go
// body is never called, so the site is passed in and no call frame is
// added:
//   - a statement, or the last statement of a body or branch, is
//     panic(ergo.UnreachableAt(reason, "file:line:col")), the statement
//     hand-written Go writes and one Go counts as terminating, so a branch
//     that ends in it needs no value (unreachableStmt);
//   - a value position is ergo.Never[T](reason, "file:line:col"), T the
//     type the checker gave the call, which panics the same way
//     (unreachableValue).

// unreachableCall is x as a call of the prelude's Unreachable, or nil.
func (fc *funcCtx) unreachableCall(x syntax.Expr) *syntax.CallExpr {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok || !check.IsUnreachable(fc.l.info.Calls[call]) {
		return nil
	}
	return call
}

// unreachableArgs are the Go arguments of the defect of x: the reason, then
// the call's position as file:line:column.
func (fc *funcCtx) unreachableArgs(x *syntax.CallExpr) string {
	if len(x.Args) != 1 || x.Trailing != nil {
		ice(x, "Unreachable with %d arguments", len(x.Args))
	}
	return fc.value(x.Args[0]) + ", " + strconv.Quote(fc.l.siteText(x))
}

// unreachableStmt writes the call x as the Go panic statement.
func (fc *funcCtx) unreachableStmt(x *syntax.CallExpr) {
	fc.line("panic(" + fc.atSite(x, x.Pos(), fc.l.rt()+".UnreachableAt("+fc.unreachableArgs(x)+")") + ")")
}

// unreachableValue is the call x in a position that uses its value, of the
// type the checker gave it there.
func (fc *funcCtx) unreachableValue(x *syntax.CallExpr) operand {
	t := fc.typeOf(x)
	if t == nil || t == check.NoValue {
		ice(x, "Unreachable in a value position with no type")
	}
	args := fc.unreachableArgs(x)
	return operand{val: fc.atSite(x, x.Pos(), fc.l.rt()+".Never["+fc.goType(t)+"]("+args+")")}
}
