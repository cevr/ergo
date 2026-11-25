package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// vacuousTest is the strict rule for a test that cannot fail (anti-slop
// test-cannot-fail; writing-tests "tautological"): a test with no `try`
// and no assertion whose condition reads a call or a local built from
// one (assert stands only in a test or law, so no helper asserts for it). Such a test checks only values
// it wrote itself (`assert 2 == 2`, a constant against its literal), or
// nothing. A law is out of scope: its body is its check, over generated
// inputs.
var vacuousTest = &Rule{
	Name:    "vacuous-test",
	Code:    "L138",
	Doc:     "A test whose assertions read no result of the code under test, and that calls nothing that can fail it, cannot fail.",
	Instead: "assert on what the code under test returns, or delete the test",
	Default: Warning,
	Check:   checkVacuousTest,
}

func checkVacuousTest(p *Pass) {
	for _, d := range p.File.Decls {
		t, ok := d.(*syntax.TestDecl)
		if !ok || t.Body == nil {
			continue
		}
		if !testCanFail(p.Info, t.Body) {
			p.Report(t.Pos(), t.Name.End(), "the test "+t.Name.Name+" reads no result of a call in any assertion, and nothing in it can fail it")
		}
	}
}

// testCanFail reports whether the body tries, or asserts on a call's
// result: read directly, through a local built from one, or by the
// assertion standing in a branch, an arm or a loop that a call's result
// decides (an `assert false` in the arm of a catch on a call fails when
// the call fails as it should not). A local is built from a call when a
// value it is given reads one, or it is set in such a branch, or bound by
// such a loop or catch.
func testCanFail(info *check.Info, body *syntax.BlockStmt) bool {
	built := map[*check.Var]bool{}
	readsCall := func(xs ...syntax.Expr) bool {
		found := false
		for _, x := range xs {
			if x == nil {
				continue
			}
			syntax.Inspect(x, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.CallExpr:
					if c := info.Calls[n]; c != nil && c.Kind != check.CallConversion {
						found = true
					}
				case *syntax.Ident:
					if v, ok := info.Uses[n].(*check.Var); ok && built[v] {
						found = true
					}
				}
				return !found
			})
		}
		return found
	}
	changed, can := false, false
	mark := func(ids []*syntax.Ident) {
		for _, id := range ids {
			v, ok := info.Defs[id].(*check.Var)
			if !ok {
				v, ok = info.Uses[id].(*check.Var)
			}
			if ok && !built[v] {
				built[v] = true
				changed = true
			}
		}
	}
	var visit func(root syntax.Node, ctl bool)
	visit = func(root syntax.Node, ctl bool) {
		syntax.Inspect(root, func(m syntax.Node) bool {
			if m != root {
				if g, ok := guards(m); ok {
					visit(m, ctl || readsCall(g...))
					return false
				}
			}
			switch m := m.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.TryExpr:
				can = true
			case *syntax.AssertStmt:
				if ctl || readsCall(m.X) {
					can = true
				}
			case *syntax.VarSpec:
				if ctl || readsCall(m.Values...) {
					mark(append(definedIdents(m.Pattern), m.Names...))
				}
			case *syntax.AssignStmt:
				if ctl || readsCall(m.Rhs...) {
					var ids []*syntax.Ident
					for _, l := range m.Lhs {
						if id := rootIdent(l); id != nil {
							ids = append(ids, id)
						}
					}
					mark(ids)
				}
			case *syntax.RangeStmt:
				if ctl {
					mark(append(definedIdents(m.Key), definedIdents(m.Value)...))
				}
			case *syntax.CatchExpr:
				if ctl {
					for _, arm := range m.Arms {
						for _, te := range arm.Types {
							mark(definedIdents(te))
						}
					}
				}
			}
			return true
		})
	}
	for changed = true; changed; {
		changed = false
		visit(body, false)
	}
	return can
}

// guards is the expressions that decide which of a node's parts run: an
// if's condition, a match's subjects, a loop's condition or range, the
// expression a catch catches; ok is false for any other node.
func guards(n syntax.Node) ([]syntax.Expr, bool) {
	switch n := n.(type) {
	case *syntax.IfStmt:
		return []syntax.Expr{n.Cond}, true
	case *syntax.SwitchStmt:
		return []syntax.Expr{n.Tag}, true
	case *syntax.TypeSwitchStmt:
		var out []syntax.Expr
		for _, s := range n.Subjects {
			out = append(out, s.X)
		}
		return out, true
	case *syntax.ForStmt:
		return []syntax.Expr{n.Cond}, true
	case *syntax.RangeStmt:
		return []syntax.Expr{n.X}, true
	case *syntax.CatchExpr:
		return []syntax.Expr{n.X}, true
	}
	return nil, false
}

// definedIdents is the names in a pattern or a range variable, nil for
// none.
func definedIdents(x syntax.Expr) []*syntax.Ident {
	if x == nil {
		return nil
	}
	var out []*syntax.Ident
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			out = append(out, id)
		}
		return true
	})
	return out
}
