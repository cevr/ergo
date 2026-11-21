package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// concatInLoop is the speed hint for a string built by + in a loop
// (catalog PF13, examples/jobs/api/routes.ergo and
// examples/confparse/config/encode.ergo): inside a for or for-in body, an
// assignment `s = s + e`, `s = e + s` or `s += e` to a string variable declared
// outside that loop. Each step copies all of s, so n steps copy O(n²)
// bytes.
var concatInLoop = &Rule{
	Name:    "concat-in-loop",
	Code:    "L147",
	Doc:     "A string grown by + on every turn of a loop is copied whole each time; collect the parts and join them once.",
	Instead: "append the parts to a []string and strings.Join them after the loop, or write them to a strings.Builder",
	Default: Hint,
	Check:   checkConcatInLoop,
}

func checkConcatInLoop(p *Pass) {
	inLoops(p.File, func(n syntax.Node, loop syntax.Stmt) {
		a, ok := n.(*syntax.AssignStmt)
		if !ok || loop == nil || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return
		}
		x, ok := a.Lhs[0].(*syntax.Ident)
		if !ok {
			return
		}
		v, ok := p.Info.Uses[x].(*check.Var)
		if !ok || v.Pos >= loop.Pos() && v.Pos < loop.End() {
			return
		}
		if b, isBasic := underOf(v.Type).(*check.Basic); !isBasic || b.Kind != check.String {
			return
		}
		grows := a.Tok == syntax.ADD_ASSIGN
		if a.Tok == syntax.ASSIGN {
			grows = sumHolds(p.Info, a.Rhs[0], v)
		}
		if grows {
			p.Report(a.Pos(), a.End(), "this grows a string by + on every turn of the loop, copying it whole each time")
		}
	})
}
