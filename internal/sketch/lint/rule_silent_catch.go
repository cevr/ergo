package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// silentCatch is the justifier rule for a catch arm whose failure leaves
// no trace (plan §3; tsgo catchToIgnore, effect-oxlint noSilentCatchAll).
var silentCatch = &Rule{
	Name:    "silent-catch",
	Code:    "L101",
	Doc:     "A catch arm that never reads the failure, never returns or fails, and calls nothing with an effect drops the failure without a trace.",
	Default: Warning,
	Check:   checkSilentCatch,
}

func checkSilentCatch(p *Pass) {
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CatchExpr)
		if !ok || p.Info.Matches[x] == nil {
			return true
		}
		for _, arm := range x.Arms {
			if !readsFailure(p.Info, arm) && !leavesTrace(p.Info, arm.Body) {
				p.Report(arm.Case, arm.Colon+1, "this arm drops the failure: it does not read it, return, fail, or call anything with an effect; handle it, or say why with // "+DirectiveWord+" silent-catch -- REASON")
			}
		}
		return true
	})
}

// readsFailure reports whether the arm's body reads a variable its
// patterns bind: the failure, or a part of it.
func readsFailure(info *check.Info, arm *syntax.CatchArm) bool {
	bound := map[*check.Var]bool{}
	var collect func(*check.Pattern)
	collect = func(pt *check.Pattern) {
		if pt == nil {
			return
		}
		if pt.Var != nil {
			bound[pt.Var] = true
		}
		for _, e := range pt.Elems {
			collect(e)
		}
		for _, f := range pt.Fields {
			collect(f.Pattern)
		}
	}
	for _, te := range arm.Types {
		collect(info.Patterns[te])
	}
	if len(bound) == 0 {
		return false
	}
	reads := false
	for _, s := range arm.Body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok {
				if v, isVar := info.Uses[id].(*check.Var); isVar && bound[v] {
					reads = true
				}
			}
			return !reads
		})
	}
	return reads
}

// leavesTrace reports whether the statements, in their own frame, return,
// fail through a try, or make a call that adds to the frame's footprint
// (check.CallAddsFootprint). A function literal and a call's trailing
// block other than a with's are frames of their own and are not read.
func leavesTrace(info *check.Info, body []syntax.Stmt) bool {
	found := false
	var visit func(syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ReturnStmt, *syntax.TryExpr:
			found = true
			return false
		case *syntax.CallExpr:
			if check.CallAddsFootprint(info, n) {
				found = true
				return false
			}
			if n.Trailing != nil {
				syntax.Inspect(n.Fun, visit)
				for _, a := range n.Args {
					syntax.Inspect(a, visit)
				}
				if call := info.Calls[n]; call != nil && call.Kind == check.CallWith {
					syntax.Inspect(n.Trailing, visit)
				}
				return false
			}
		}
		return true
	}
	for _, s := range body {
		syntax.Inspect(s, visit)
	}
	return found
}
