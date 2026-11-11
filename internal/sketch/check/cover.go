package check

import "ergo.local/prototype/internal/sketch/syntax"

// A try may start any operand (q105; grammar.md, Expressions 1). It wraps
// its operand only and adds no grouping; it covers every call evaluated
// after it, to the end of the enclosing expression. Within one expression
// the operators evaluate left to right, so what follows a try on its
// operator spine (the binary, unary and dereference operators above it, up
// to the first parenthesis, argument or other expression boundary) is the
// right operand of each binary expression whose left operand holds the
// try. Each such right operand is a covered root: every fallible value
// evaluated in it propagates, as under the try itself (Info.TryCovers).
//
// A try with nothing fallible in its operand or in a covered root is the
// redundant try (S108, rows.md Error rows 6), decided when the whole spine
// is checked.

// tryCover is the checker's state for operand trys.
type tryCover struct {
	// inner holds every binary expression below the top of a planned
	// spine; the top is the one binary of a spine that is not inner.
	inner map[*syntax.BinaryExpr]bool
	// spine is the operand trys of a top's spine, planned once.
	spine map[*syntax.BinaryExpr][]*syntax.TryExpr
	// roots maps a covered root to the trys that cover it.
	roots map[syntax.Expr][]*syntax.TryExpr
	// right holds the trys that cover at least one root; used, the trys
	// that covered something fallible while the top was last checked.
	right, used map[*syntax.TryExpr]bool
}

// spineTop reports whether x is the top of an operator spine that holds an
// operand try, planning the spine on its first visit.
func (c *checker) spineTop(x syntax.Expr) bool {
	b, ok := x.(*syntax.BinaryExpr)
	if !ok || c.cover.inner[b] {
		return false
	}
	if trys, planned := c.cover.spine[b]; planned {
		return trys != nil
	}
	cv := &c.cover
	if cv.spine == nil {
		cv.inner = map[*syntax.BinaryExpr]bool{}
		cv.spine = map[*syntax.BinaryExpr][]*syntax.TryExpr{}
		cv.roots = map[syntax.Expr][]*syntax.TryExpr{}
		cv.right = map[*syntax.TryExpr]bool{}
		cv.used = map[*syntax.TryExpr]bool{}
	}
	var walk func(e syntax.Expr) []*syntax.TryExpr
	walk = func(e syntax.Expr) []*syntax.TryExpr {
		switch e := e.(type) {
		case *syntax.BinaryExpr:
			if e != b {
				cv.inner[e] = true
			}
			l := walk(e.X)
			r := walk(e.Y)
			if len(l) > 0 {
				cv.roots[e.Y] = append([]*syntax.TryExpr(nil), l...)
				c.pkg.Info.TryCovers[e.Y] = l[0]
				for _, t := range l {
					cv.right[t] = true
				}
			}
			return append(l, r...)
		case *syntax.UnaryExpr:
			return walk(e.X)
		case *syntax.StarExpr:
			return walk(e.X)
		case *syntax.TryExpr:
			return append([]*syntax.TryExpr{e}, walk(e.X)...)
		}
		return nil
	}
	trys := walk(b)
	cv.spine[b] = trys
	return trys != nil
}

// coveredExpr checks x, a spine's top or a covered root (expr).
func (c *checker) coveredExpr(x syntax.Expr, hint Type, top bool) operand {
	cv := &c.cover
	if top {
		for _, t := range cv.spine[x.(*syntax.BinaryExpr)] {
			delete(cv.used, t)
		}
	}
	trys := cv.roots[x]
	var t *target
	if c.fn != nil {
		t = c.fn.t
	}
	saved := false
	if trys != nil && t != nil {
		saved = t.tried
		t.tried = false
		t.tries++
	}
	op := c.expr0(x, hint)
	if trys != nil && t != nil {
		t.tries--
		if t.tried {
			c.useTrys(trys[len(trys)-1:])
		}
		t.tried = saved
	}
	if top {
		for _, tr := range cv.spine[x.(*syntax.BinaryExpr)] {
			if cv.right[tr] && !cv.used[tr] {
				c.errorf(tr, CodeType, "try on an expression that cannot fail (rows.md, Error rows 6)")
			}
		}
	}
	return op
}

// covering is whether expressions are checked under coverage: some spine
// has a try in operand position.
func (c *checker) covering() bool { return c.cover.spine != nil }

// coveredRoot reports whether x is a covered root, marking its trys used.
func (c *checker) coveredRoot(x syntax.Expr) bool {
	trys := c.cover.roots[x]
	if trys == nil {
		return false
	}
	c.useTrys(trys[len(trys)-1:])
	return true
}

func (c *checker) useTrys(trys []*syntax.TryExpr) {
	for _, t := range trys {
		c.cover.used[t] = true
	}
}
