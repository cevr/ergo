package lint

import (
	"go/constant"
	"go/token"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The weak-test facts of SKILL-RULES SR-ERGO-7 (SR-T07, T11), read
// from tests and laws as the checker resolved them (docs/testing.md:
// `assert` stands only in a test or a law; an escaping typed failure
// refutes a test).

// restated reports an assert of `x == y` whose two sides are constants of
// one value (SR-T07): a constant beside the literal it was declared as, a
// literal beside itself, or two constant expressions the checker folded
// to one value (constValue). It passes by construction.
func restated(info *check.Info, a *syntax.AssertStmt) bool {
	b, ok := unparen(a.X).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.EQL {
		return false
	}
	x, y := constValue(info, b.X), constValue(info, b.Y)
	if x == nil || y == nil || x.Kind() != y.Kind() {
		return false
	}
	return constant.Compare(x, token.EQL, y)
}

// constValue is e's constant value: the checker's, or, for the name of a
// package-level let of this package that is never written, the value of
// its constant initializer; nil for any other expression.
func constValue(info *check.Info, e syntax.Expr) constant.Value {
	e = unparen(e)
	if v := info.Values[e]; v != nil {
		return v
	}
	id, ok := e.(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := info.Uses[id].(*check.Var)
	if !ok || v.Mut() {
		return nil
	}
	for _, in := range info.InitOrder {
		if len(in.Vars) == 1 && in.Vars[0] == v {
			return info.Values[in.Value]
		}
	}
	return nil
}

// repeatedAsserts is each assert of a block that repeats an earlier one of
// it (SR-T11): the same expression, by shapeOf, with only asserts between
// them, and every call in it pure, so it reads what the first read and
// gives what it gave. Nested blocks are read on their own.
func repeatedAsserts(info *check.Info, body *syntax.BlockStmt) []*syntax.AssertStmt {
	var out []*syntax.AssertStmt
	syntax.Inspect(body, func(n syntax.Node) bool {
		b, ok := n.(*syntax.BlockStmt)
		if !ok {
			return true
		}
		asserts := 0
		for _, s := range b.List {
			if _, isAssert := s.(*syntax.AssertStmt); isAssert {
				asserts++
				if asserts >= 2 {
					break
				}
			}
		}
		if asserts < 2 {
			return true
		}
		seen := map[string]bool{}
		for _, s := range b.List {
			a, ok := s.(*syntax.AssertStmt)
			if !ok {
				seen = map[string]bool{}
				continue
			}
			if !pureExpr(info, a.X) {
				continue
			}
			shape := shapeOf(info, a.X)
			if seen[shape] {
				out = append(out, a)
			}
			seen[shape] = true
		}
		return true
	})
	return out
}

// pureExpr reports an expression none of whose calls adds a footprint.
func pureExpr(info *check.Info, e syntax.Expr) bool {
	pure := true
	syntax.Inspect(e, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok && check.CallAddsFootprint(info, c) {
			pure = false
		}
		return pure
	})
	return pure
}
