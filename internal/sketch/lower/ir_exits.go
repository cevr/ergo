package lower

// IR builder: the static exit form (FLIP F7 STATIC-EXIT; runtime/v1
// exits.go). A frame scope that exists only for exit actions, none of
// which starts a task or registers an action of its own, in a frame that
// owns no value, keeps its actions in a v1.Exits local, not a Scope:
//
//	var x v1.Exits
//	x.Begin(env.task)
//	defer x.End(nil | &<error result>)
//	…
//	v1.ExitsOnExit(&x, func() { … })   // scope.OnExit() { … }
//
// Exits.End runs the actions as Scope.End runs them (last first, masked,
// each defect kept, a stop winning at each action's end and the frame's),
// with no heap Scope, list entry or route per action. exitLowering
// "scope" keeps the general frame scope (ir_own.go openScope): the
// reference the property (irspike TestIRStaticExitsMatchSource) and the
// probe's receipt compare against.
//
// Prior art: Go open-codes a frame's defers in its stack frame (Go 1.14);
// Swift's defer and Rust's drop guards are scoped at compile time;
// Effect's Scope.addFinalizer keeps a runtime list, as the general form
// does.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// exitLowering selects the static exit form: "" builds it where it
// applies, "scope" keeps the general frame scope. Set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.exitLowering=scope.
var exitLowering string

// SetExitLoweringForTest selects the exit lowering ("" or "scope") and
// returns the previous one.
func SetExitLoweringForTest(mode string) string {
	prev := exitLowering
	exitLowering = mode
	return prev
}

// staticExits reports a body list whose frame scope the static exit form
// can hold: every call that needs the frame is scope.OnExit, whose block
// needs no frame of its own; no owning with or context value with; and no
// owned value (a taken parameter is checked by the caller).
func (l *lowerer) staticExits(list []syntax.Stmt) bool {
	if exitLowering == "scope" || l.bodyOwns(list) {
		return false
	}
	ok := true
	var visit func(n syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CallExpr:
			if l.needsFrame(n) {
				ok = l.isOnExit(n) && !l.exitBlockNeedsFrame(n)
				return false
			}
			for w := l.info.Withs[n]; w != nil; w = w.Next {
				if ownsResources(w) || w.Value != nil {
					ok = false
					return false
				}
			}
			if c := l.info.Calls[n]; n.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
				syntax.Inspect(n.Fun, visit)
				for _, a := range n.Args {
					syntax.Inspect(a, visit)
				}
				return false
			}
		}
		return true
	}
	for _, s := range list {
		syntax.Inspect(s, visit)
	}
	return ok
}

// isOnExit reports a call of the std scope.OnExit.
func (l *lowerer) isOnExit(x *syntax.CallExpr) bool {
	c := l.info.Calls[x]
	return c != nil && c.Func != nil && c.Func.Name == "OnExit" && c.Func.Pkg != nil && c.Func.Pkg.Std() && c.Func.Pkg.Path == "scope"
}

// exitBlockNeedsFrame reports an OnExit call x whose block (trailing, or a
// function literal operand) needs a frame scope of its own: it starts a
// task, registers an action, or owns a value. Any other operand form (a
// function value) is unknown and counts as needing one.
func (l *lowerer) exitBlockNeedsFrame(x *syntax.CallExpr) bool {
	var body *syntax.BlockStmt
	switch {
	case x.Trailing != nil && len(x.Args) == 0:
		body = x.Trailing
	case x.Trailing == nil && len(x.Args) == 1:
		lit, ok := unparen(x.Args[0]).(*syntax.FuncLit)
		if !ok {
			return true
		}
		body = lit.Body
	default:
		return true
	}
	return body == nil || l.bodyNeedsFrame(body.List)
}

// openExits opens the static exit form of a frame (see the file comment):
// the Exits local x, its Begin on the frame's task and its deferred End.
func (b *irBuilder) openExits(p irPos, task, errArg *irExpr) []*irStmt {
	x := b.temp("exits", b.spelling(b.l.rt()+".Exits"), p)
	b.frame, b.exits = x+1, true
	b.keepImport("var _ " + b.l.rt() + ".Exits")
	return []*irStmt{
		{K: "var", Pos: p, Local: x},
		{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Begin", X: b.addrOf(x, p), Args: []*irExpr{task}}},
		{K: "defer", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "End", X: b.addrOf(x, p), Args: []*irExpr{errArg}}},
	}
}

// exitsForm is the runtime body a std call at a frame in the static exit
// form calls in place of name: OnExit registers on the Exits.
func (b *irBuilder) exitsForm(name string) string {
	if b.exits && name == "OnExit" {
		return "ExitsOnExit"
	}
	return name
}
