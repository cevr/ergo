package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unboundedCollect is the justifier rule for reading an unbounded Seq to
// its end (plan §3; effect-oxlint noRunCollectOnUnboundedStream). A Seq is
// unbounded when its producer's doc comment has an Unbounded: paragraph
// (std marks queue's Values and actor's Changes). Collecting is a call of
// a std collector (check.RoleCollect) or a for-in whose body appends (the
// prelude's Append) and never leaves the loop.
var unboundedCollect = &Rule{
	Name:    "unbounded-collect",
	Code:    "L105",
	Doc:     "Collecting a Seq that ends only when another task closes its source waits for that close; take a bounded part, or loop over it and handle each value.",
	Default: Warning,
	Check:   checkUnboundedCollect,
}

func checkUnboundedCollect(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	bound := map[*check.Var]syntax.Expr{}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		d, ok := n.(*syntax.DeclStmt)
		if !ok {
			return true
		}
		for _, s := range d.Decl.Specs {
			if len(s.Names) != 1 || len(s.Values) != 1 {
				continue
			}
			if v, isVar := p.Info.Defs[s.Names[0]].(*check.Var); isVar && !v.Mut() {
				bound[v] = s.Values[0]
			}
		}
		return true
	})
	var unbounded func(e syntax.Expr, depth int) bool
	unbounded = func(e syntax.Expr, depth int) bool {
		for {
			paren, ok := e.(*syntax.ParenExpr)
			if !ok {
				break
			}
			e = paren.X
		}
		switch e := e.(type) {
		case *syntax.Ident:
			v, isVar := p.Info.Uses[e].(*check.Var)
			value, known := bound[v]
			return isVar && known && depth < 8 && unbounded(value, depth+1)
		case *syntax.CallExpr:
			doc, _ := check.OpDoc(p.Info, e)
			if call := p.Info.Calls[e]; call != nil && call.Func != nil {
				doc, _ = check.DocOf(call.Func)
			}
			_, ok := doc.Unbounded()
			return ok
		}
		return false
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			call := p.Info.Calls[n]
			if call != nil && check.RoleOf(call.Func) == check.RoleCollect && len(n.Args) == 1 && unbounded(n.Args[0], 0) {
				p.Report(n.Pos(), n.End(), "this collects a Seq that ends only when its source is closed, so it waits for that close; loop over it and handle each value, or say why with // "+DirectiveWord+" unbounded-collect -- REASON")
			}
		case *syntax.RangeStmt:
			if unbounded(n.X, 0) && appendsWithoutLeaving(p.Info, n.Body) {
				p.Report(n.For, n.Body.Lbrace, "this loop appends every value of a Seq that ends only when its source is closed and never leaves early; handle each value, or say why with // "+DirectiveWord+" unbounded-collect -- REASON")
			}
		}
		return true
	})
}

// appendsWithoutLeaving reports a loop body that calls the prelude's
// Append and has no break, return or try of its own loop: function
// literals are frames of their own, and a break inside a nested loop
// leaves only that loop.
func appendsWithoutLeaving(info *check.Info, body *syntax.BlockStmt) bool {
	appends, leaves := false, false
	var visit func(n syntax.Node, nested bool) bool
	visit = func(n syntax.Node, nested bool) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ForStmt, *syntax.RangeStmt:
			syntax.Inspect(n, func(m syntax.Node) bool {
				if m == n {
					return true
				}
				return visit(m, true)
			})
			return false
		case *syntax.BranchStmt:
			if !nested && n.Tok == syntax.BREAK {
				leaves = true
			}
		case *syntax.ReturnStmt, *syntax.TryExpr:
			leaves = true
		case *syntax.CallExpr:
			if call := info.Calls[n]; call != nil && check.RoleOf(call.Func) == check.RoleAppend {
				appends = true
			}
		}
		return true
	}
	syntax.Inspect(body, func(n syntax.Node) bool { return visit(n, false) })
	return appends && !leaves
}
