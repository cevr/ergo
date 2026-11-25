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
	Instead: "handle it, or say why with // " + DirectiveWord + " silent-catch -- REASON",
	Default: Warning,
	Check:   checkSilentCatch,
}

func checkSilentCatch(p *Pass) {
	for _, x := range p.fileIndex().catches {
		if p.Info.Matches[x] == nil {
			continue
		}
		for _, arm := range x.Arms {
			if !readsFailure(p.Info, arm) && !leavesTrace(p.Info, arm.Body) {
				p.Report(arm.Case, arm.Colon+1, "this arm drops the failure: it does not read it, return, fail, or call anything with an effect")
			}
		}
	}
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
		walkNode(s, func(n syntax.Node) bool {
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
// fail through a try, make a call that adds to the frame's footprint
// (check.CallAddsFootprint), stop on a path that cannot run (the
// prelude's Unreachable, by identity), assert, or write a variable
// declared outside the statements: the failure then shows in state the
// rest of the function reads (LHP2, from the 2026-11-17 read sample). A
// function literal and a call's trailing block other than a with's are
// frames of their own and are not read.
func leavesTrace(info *check.Info, body []syntax.Stmt) bool {
	own := map[check.Object]bool{}
	for _, s := range body {
		walkNode(s, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok && info.Defs[id] != nil {
				own[info.Defs[id]] = true
			}
			return true
		})
	}
	found := false
	var visit func(syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ReturnStmt, *syntax.TryExpr, *syntax.AssertStmt:
			found = true
			return false
		case *syntax.AssignStmt:
			for _, lhs := range n.Lhs {
				if v, ok := info.Uses[rootIdent(lhs)].(*check.Var); ok && !own[v] {
					found = true
					return false
				}
			}
		case *syntax.CallExpr:
			if check.CallAddsFootprint(info, n) || check.IsUnreachable(info.Calls[n]) {
				found = true
				return false
			}
			if n.Trailing != nil {
				walkNode(n.Fun, visit)
				for _, a := range n.Args {
					walkNode(a, visit)
				}
				if call := info.Calls[n]; call != nil && call.Kind == check.CallWith {
					walkNode(n.Trailing, visit)
				}
				return false
			}
		}
		return true
	}
	for _, s := range body {
		walkNode(s, visit)
	}
	return found
}

// rootIdent is the variable an assignment target writes through: x of x,
// x.f, x[i] and their nestings; nil for anything else.
func rootIdent(e syntax.Expr) *syntax.Ident {
	for {
		switch x := e.(type) {
		case *syntax.Ident:
			return x
		case *syntax.SelectorExpr:
			e = x.X
		case *syntax.IndexExpr:
			e = x.X
		case *syntax.ParenExpr:
			e = x.X
		default:
			return nil
		}
	}
}
