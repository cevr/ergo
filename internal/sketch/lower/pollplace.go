package lower

// Back-edge polls a callee already makes (FLIP P1; runtime.md, I1).
//
// A polling frame ends each loop iteration with its task's Check, so a
// long loop stays stoppable. When every iteration that reaches the back
// edge has already called a function that checks at its own entry, that
// check is the iteration's interruption point, and the back-edge poll adds
// a second one on the same task with nothing between them that a stop
// could be waiting on. The loop then takes no back-edge poll, and its
// continue is Go's own (no goto to the poll). The iteration still passes
// one Check, so a stop is seen as soon as before: within one iteration.
//
// The rule is not "a loop that cannot block needs no poll": a loop of
// pure arithmetic still runs unbounded, and its back-edge poll is the only
// point where a stop reaches it.
//
// A body polls through a callee (iterationPolls) when one of its
// top-level statements, before any statement that holds a continue,
// evaluates on every path a plain call of a declared function whose frame
// polls at entry: the checker's EntryPoll placement fact (recursive or
// opaque) for a frame that is not closed (knownClosed), the condition the
// emitter (decls.go) and the IR builder (ir.go) write the entry poll by.
// The call passes no function value and no trailing block, so it is never
// a known variant (which has no entry poll) and never inlined (an inlined
// body with a footprint is not admitted, inline.go). "Every path" admits
// the call's own operands, a receiver, a composite literal's elements, a
// try's or a catch's subject, and the left operand of && and ||; nothing
// under an if, a match, a catch arm or a function literal.
//
// Prior art: HotSpot's C2 keeps a safepoint poll on a loop's back edge
// only when the loop body has no call, since a call is itself a safepoint;
// the BEAM counts reductions per call, so a loop of calls is preempted
// through its calls; Go preempts a goroutine asynchronously instead.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// pollLowering selects the back-edge poll's placement: "" omits the poll
// of a loop whose every iteration polls through a callee; "every" is the
// reference, a back-edge poll on every loop of a polling frame (set by a
// test, or by -ldflags=-X=ergo.local/prototype/internal/sketch/lower.pollLowering=every).
var pollLowering string

// iterationPolls reports that every iteration of the loop body that
// reaches the back edge has called a function that polls at its entry.
func (fc *funcCtx) iterationPolls(body *syntax.BlockStmt) bool {
	if pollLowering == "every" || body == nil {
		return false
	}
	for _, s := range body.List {
		if fc.stmtPolls(s) {
			return true
		}
		if fc.containsContinue(&syntax.BlockStmt{List: []syntax.Stmt{s}}) {
			return false
		}
	}
	return false
}

// stmtPolls reports a statement that evaluates a polling call on every
// path through it.
func (fc *funcCtx) stmtPolls(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		return fc.exprPolls(s.X)
	case *syntax.DeclStmt:
		if s.Decl == nil {
			return false
		}
		for _, spec := range s.Decl.Specs {
			for _, v := range spec.Values {
				if fc.exprPolls(v) {
					return true
				}
			}
		}
	case *syntax.AssignStmt:
		for _, x := range s.Rhs {
			if fc.exprPolls(x) {
				return true
			}
		}
	}
	return false
}

// exprPolls reports an expression whose evaluation calls, on every path,
// a function that polls at its entry.
func (fc *funcCtx) exprPolls(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.CallExpr:
		if fc.callPolls(x) {
			return true
		}
		if sel, ok := x.Fun.(*syntax.SelectorExpr); ok && fc.exprPolls(sel.X) {
			return true // a receiver is evaluated before the call
		}
		for _, a := range x.Args {
			if fc.exprPolls(a) {
				return true
			}
		}
	case *syntax.TryExpr:
		return fc.exprPolls(x.X)
	case *syntax.CatchExpr:
		return fc.exprPolls(x.X)
	case *syntax.CompositeLit:
		for _, e := range x.Elts {
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				e = kv.Value
			}
			if fc.exprPolls(e) {
				return true
			}
		}
	case *syntax.BinaryExpr:
		// The left operand always runs; the right one of && and || may not.
		if fc.exprPolls(x.X) {
			return true
		}
		if x.Op != syntax.LAND && x.Op != syntax.LOR {
			return fc.exprPolls(x.Y)
		}
	case *syntax.UnaryExpr:
		return fc.exprPolls(x.X)
	case *syntax.SelectorExpr:
		return fc.exprPolls(x.X)
	}
	return false
}

// callPolls reports a plain call of a declared function whose frame
// checks for a stop at its entry, whatever the call site's lowering.
func (fc *funcCtx) callPolls(x *syntax.CallExpr) bool {
	c := fc.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Func.Decl == nil || c.Func.Decl.Body == nil || c.Func.Pkg == nil || c.Func.Decl.Extern != nil {
		return false
	}
	if x.Trailing != nil {
		return false
	}
	for _, a := range x.Args {
		if _, fn := fc.l.info.Types[a].(*check.Signature); fn {
			return false
		}
	}
	fp, ok := c.Func.Pkg.Info.Footprints[c.Func.Decl]
	return ok && fp.EntryPoll && !fp.Empty() && !fc.l.knownClosed(c.Func)
}

// containsContinue reports a continue in body, outside a function
// literal: a nested loop's counts too (conservative: a labelled continue
// may leave the nested loop).
func (fc *funcCtx) containsContinue(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.BranchStmt:
			if n.Tok == syntax.CONTINUE {
				found = true
			}
		}
		return !found
	})
	return found
}
