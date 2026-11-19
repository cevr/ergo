package check

import "ergo.local/prototype/internal/sketch/syntax"

// The vet class (VET-CHECK): source that builds and is always wrong, which
// Go leaves to go vet and staticcheck. Each rule here is decided by facts
// the checker has, so it is an error, not a lint finding (findings.md of
// VET-GAP; diagnostics.md).

// sameStorage reports whether a and b name the same storage with no work
// between: one local, a field of it, or an element at one name or literal
// index. No shadowing (196v rule 2) makes a name one variable in a
// function, so equal spellings are one place. A call, a slice or any other
// form is never the same storage, since it may compute a new place.
func sameStorage(a, b syntax.Expr) bool {
	a, b = unparen(a), unparen(b)
	switch x := a.(type) {
	case *syntax.Ident:
		y, ok := b.(*syntax.Ident)
		return ok && x.Name == y.Name && x.Name != "_"
	case *syntax.SelectorExpr:
		y, ok := b.(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && sameStorage(x.X, y.X)
	case *syntax.IndexExpr:
		y, ok := b.(*syntax.IndexExpr)
		if !ok || len(x.Indices) != 1 || len(y.Indices) != 1 || !sameStorage(x.X, y.X) {
			return false
		}
		if lx, ok := unparen(x.Indices[0]).(*syntax.BasicLit); ok {
			ly, ok := unparen(y.Indices[0]).(*syntax.BasicLit)
			return ok && lx.Kind == ly.Kind && lx.Value == ly.Value
		}
		return sameStorage(x.Indices[0], y.Indices[0])
	}
	return false
}

// selfAssignment refuses `x = x` (S110; go vet's assign): it stores a
// place's own value back, so it does nothing. An arm with nothing to do is
// written with no statements.
func (c *checker) selfAssignment(s *syntax.AssignStmt) bool {
	if s.Tok != syntax.ASSIGN || !sameStorage(s.Lhs[0], s.Rhs[0]) {
		return false
	}
	text := storageText(s.Lhs[0])
	c.errorf(s, CodeUnused, "%s = %s assigns %s its own value, so it does nothing; delete it (an arm with nothing to do has no statements: case X:)", text, text, text)
	return true
}

// storageText spells a place sameStorage accepts: a name, a field or an
// element at a name or a literal.
func storageText(x syntax.Expr) string {
	switch x := unparen(x).(type) {
	case *syntax.SelectorExpr:
		return storageText(x.X) + "." + x.Sel.Name
	case *syntax.IndexExpr:
		if lit, ok := unparen(x.Indices[0]).(*syntax.BasicLit); ok {
			return storageText(x.X) + "[" + lit.Value + "]"
		}
		return storageText(x.X) + "[" + storageText(x.Indices[0]) + "]"
	}
	return exprText(x)
}
