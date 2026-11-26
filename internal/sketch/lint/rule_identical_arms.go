package lint

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// identicalArms is the rule of the strict preset for a branch that does
// not change the contract (catalog CR26): two arms of one match or catch
// whose bodies do the same, by shapeOf. An arm that reads its own binding
// differs from every other arm, since the binding is its own object. Each
// group of such arms is reported once, at its second arm, naming every
// arm of the group by its patterns in sorted order: arms are disjoint, so
// their order is free, and a reorder keeps the finding (spec §[R2] 4).
var identicalArms = &Rule{
	Name:    "identical-arms",
	Code:    "L119",
	Doc:     "Two arms of one match or catch with the same body are one case written twice; list both patterns in one arm.",
	Instead: "list their patterns in one arm",
	Default: Warning,
	Check:   checkIdenticalArms,
}

// arm is one arm of a match or catch: where it starts, its patterns and
// its body.
type arm struct {
	at       syntax.Pos
	colon    syntax.Pos
	patterns []syntax.Expr
	body     []syntax.Stmt
}

func checkIdenticalArms(p *Pass) {
	for _, n := range p.fileIndex().switches {
		var arms []arm
		switch x := n.(type) {
		case *syntax.SwitchStmt:
			arms = clauseArms(x.Clauses)
		case *syntax.TypeSwitchStmt:
			arms = clauseArms(x.Clauses)
		case *syntax.CatchExpr:
			for _, a := range x.Arms {
				arms = append(arms, arm{a.Case, a.Colon, writtenOr(a.Written, a.Types), a.Body})
			}
		default:
			continue
		}
		if len(arms) < 2 {
			continue
		}
		groups := map[string][]arm{}
		var order []string
		for _, a := range arms {
			shape := shapeOf(p.Info, a.body...)
			if groups[shape] == nil {
				order = append(order, shape)
			}
			groups[shape] = append(groups[shape], a)
		}
		for _, shape := range order {
			g := groups[shape]
			if len(g) < 2 {
				continue
			}
			var names []string
			for _, a := range g {
				names = append(names, patternsText(p.File, a.patterns))
			}
			slices.Sort(names)
			list := names[0]
			for _, n := range names[1:] {
				list += " and " + n
			}
			p.Report(g[1].at, g[1].colon+1, fmt.Sprintf("the arms for %s do the same", list))
		}
	}
}

func clauseArms(clauses []*syntax.CaseClause) []arm {
	var out []arm
	for _, c := range clauses {
		out = append(out, arm{c.Case, c.Colon, writtenOr(c.Written, c.List), c.Body})
	}
	return out
}

func writtenOr(written, list []syntax.Expr) []syntax.Expr {
	if written != nil {
		return written
	}
	return list
}

// patternsText is the source text of patterns, comma-separated, for a
// message; "_" for a default arm.
func patternsText(f *syntax.File, patterns []syntax.Expr) string {
	if len(patterns) == 0 {
		return "_"
	}
	text := ""
	for i, e := range patterns {
		if i > 0 {
			text += ", "
		}
		text += f.Src[e.Pos():e.End()]
	}
	return text
}
