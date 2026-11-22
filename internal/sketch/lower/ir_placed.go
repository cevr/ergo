package lower

import "fmt"

// irPlaced reports the first node of f's bodies that Go's compile would
// place nowhere, as an internal compiler error that names the node and
// its function. The IR decoder makes a Go node at each statement's,
// expression's and clause's position, and a type switch's default clause
// (the decoder adds one when the IR has none) at its ElsePos, as an
// else's at its ElsePos. A position with no line is unknown to Go: its
// compile then stops with "unknown scope position" at a DWARF scope, or
// files an error at no place (JOBS-ICE). This check stops the lowering
// first, with the node's name.
func irPlaced(f *irFile) error {
	unplaced := func(p irPos) bool { return p[1] <= 0 }
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		var bad string
		var stmts func(where string, list []*irStmt)
		var expr func(where string, x *irExpr)
		expr = func(where string, x *irExpr) {
			if x == nil || bad != "" {
				return
			}
			if unplaced(x.Pos) {
				bad = fmt.Sprintf("expression %q in %s", x.K, where)
				return
			}
			where = x.K + " at " + irPosText(x.Pos)
			expr(where, x.X)
			expr(where, x.Y)
			for _, a := range x.Args {
				expr(where, a)
			}
			for _, e := range x.Elems {
				expr(where, e)
			}
			stmts(where, x.Body)
		}
		stmts = func(where string, list []*irStmt) {
			for _, s := range list {
				if bad != "" {
					return
				}
				if unplaced(s.Pos) {
					bad = fmt.Sprintf("statement %q in %s", s.K, where)
					return
				}
				at := s.K + " at " + irPosText(s.Pos)
				if (s.K == "tswitch" && !irHasDef(s.Cases) || len(s.Else) > 0) && unplaced(s.ElsePos) {
					bad = fmt.Sprintf("the default or else of %s", at)
					return
				}
				expr(at, s.X)
				expr(at, s.Y)
				stmts(at, s.Then)
				stmts(at, s.Else)
				stmts(at, s.Body)
				for _, c := range s.Cases {
					if bad == "" && unplaced(c.Pos) {
						bad = fmt.Sprintf("a clause of %s", at)
					}
					for _, v := range c.Vals {
						expr(at, v)
					}
					stmts(at, c.Body)
				}
			}
		}
		stmts("the body", fn.Body)
		if bad != "" {
			return fmt.Errorf("lower: internal compiler error: %s of %s has no position", bad, fn.Name)
		}
	}
	return nil
}

// irHasDef reports a default clause in its source place among cases: the
// decoder then adds none.
func irHasDef(cases []*irCase) bool {
	for _, c := range cases {
		if c.Def {
			return true
		}
	}
	return false
}

// irPosText is p as the IR file names it: file index, line, column.
func irPosText(p irPos) string { return fmt.Sprintf("[%d,%d,%d]", p[0], p[1], p[2]) }
