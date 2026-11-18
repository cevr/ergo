package lint

import "ergo.local/prototype/internal/sketch/syntax"

// quadraticScan is the speed hint for a linear search inside a loop over
// the same collection (catalog PF7): in the body of a for-in over a
// variable or a field of one, a for-in over the same collection, or a call
// that passes the collection to a function whose body loops over that
// parameter (one level; a Go binding has no body, so slices.Contains and
// slices.Index are not seen yet). n turns of a loop that each walk n
// elements do n² work.
var quadraticScan = &Rule{
	Name:    "quadratic-scan",
	Code:    "L146",
	Doc:     "A walk of a collection inside a loop over the same collection does n² work; index it once before the loop.",
	Instead: "build a map (or a set) from the collection once, before the loop, and look each value up in it",
	Default: Hint,
	Check:   checkQuadraticScan,
}

func checkQuadraticScan(p *Pass) {
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		outer, ok := n.(*syntax.RangeStmt)
		if !ok {
			return true
		}
		v := rootVar(p.Info, outer.X)
		if v == nil {
			return true
		}
		shape := shapeOf(p.Info, outer.X)
		same := func(e syntax.Expr) bool { return rootVar(p.Info, e) == v && shapeOf(p.Info, e) == shape }
		syntax.Inspect(outer.Body, func(m syntax.Node) bool {
			switch m := m.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.RangeStmt:
				if same(m.X) {
					p.Report(m.For, m.Body.Lbrace, "this loop walks the collection the loop around it walks")
				}
			case *syntax.CallExpr:
				call := p.Info.Calls[m]
				if call == nil || call.Func == nil || call.Func.Decl == nil || call.Func.Decl.Body == nil || call.Func.Sig == nil || call.Func.Pkg == nil || call.Func.Pkg.Info == nil {
					return true
				}
				args := m.Args
				if call.Recv != nil {
					args = append([]syntax.Expr{call.Recv}, m.Args...)
				}
				params := call.Func.Sig.Params
				if len(args) != len(params) {
					return true
				}
				for i, a := range args {
					if same(a) && loopsOver(call.Func.Pkg.Info, call.Func.Decl.Body, params[i]) {
						p.Report(m.Pos(), m.End(), "this call walks the collection the loop around it walks")
						break
					}
				}
			}
			return true
		})
		return true
	})
}
