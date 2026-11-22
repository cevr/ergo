package lower

// IR builder: operands that run statements inside an expression, in a
// condition and in a range (FLIP F7 GP, the G2 operation-call families).
// See ir.go.
//
// The emitter writes each call of an expression as a statement before
// the expression, in source order, and spills an earlier operand to a
// temporary when a later one writes statements (expr.go operands,
// elements, binary; interp.go). The IR builder does the same. A value
// (value) collects the statements its operands run first (pending); each
// operand list goes through inOrder, which spills as operands does; and a
// && or || whose right operand runs statements becomes the emitter's
// temporary and if (shortCircuit).

import "ergo.local/prototype/internal/sketch/syntax"

// collect runs f with a fresh pending list: the statements the
// expression's operands run first come before f's own (a call's operands,
// then the call).
func (b *irBuilder) collect(f func() ([]*irStmt, *irExpr)) ([]*irStmt, *irExpr) {
	var pend []*irStmt
	saved := b.pending
	b.pending = &pend
	defer func() { b.pending = saved }()
	pre, e := f()
	return append(pend, pre...), e
}

// runFirst queues pre to run before the expression being built. Where no
// value collects statements, the body keeps its source body.
func (b *irBuilder) runFirst(pre []*irStmt) {
	if len(pre) == 0 {
		return
	}
	if b.pending == nil {
		irNo("operation call inside an expression")
	}
	*b.pending = append(*b.pending, pre...)
}

// inOrder is the operands of list, left to right, each after its implicit
// conversion (expr): their statements run first, in order, with an earlier
// operand spilled when a later one runs statements (ordered).
func (b *irBuilder) inOrder(list []syntax.Expr) []*irExpr {
	pres := make([][]*irStmt, len(list))
	vals := make([]*irExpr, len(list))
	for i, a := range list {
		pres[i], vals[i] = b.collect(func() ([]*irStmt, *irExpr) { return nil, b.expr(a) })
	}
	b.runFirst(b.ordered(list, pres, vals))
	return vals
}

// ordered (operands' too, ir_fail.go) joins the statements of operands built apart (pres, their
// values vals) in source order: an operand before one that runs
// statements is spilled to a temporary, so it is evaluated first (the
// emitter's operands). A constant needs no spill, nor a local unless a
// later operand is an if or a match, whose arm may assign it (isBranch).
func (b *irBuilder) ordered(list []syntax.Expr, pres [][]*irStmt, vals []*irExpr) []*irStmt {
	var pre []*irStmt
	for i, a := range list {
		pre = append(pre, pres[i]...)
		later, branch := false, false
		for k, p := range pres[i+1:] {
			later = later || len(p) > 0
			branch = branch || isBranch(list[i+1+k])
		}
		if later && vals[i].K != "const" && (vals[i].K != "local" || branch) {
			p := vals[i].Pos
			// The temporary has the operand's type after its implicit
			// conversion, which operand applied.
			t := b.temp("arg", b.goType(b.operandType(a), a), p)
			pre = append(pre, &irStmt{K: "def", Pos: p, Local: t, X: vals[i]})
			vals[i] = &irExpr{K: "local", Pos: p, I: t}
		}
	}
	return pre
}

// binaryOf is the binary expression x: its operands in order, or for a
// && or || whose right operand runs statements, the emitter's temporary
// (binary): r := X; if r (or !r) { Y's statements; r = Y }.
func (b *irBuilder) binaryOf(x *syntax.BinaryExpr, op string) *irExpr {
	p := b.pos(x, x.OpPos)
	if op != "&&" && op != "||" {
		xs := b.inOrder([]syntax.Expr{x.X, x.Y})
		return b.rounded(x, x, b.l.info.Types[x], &irExpr{K: "bin", Pos: p, Op: op, X: xs[0], Y: xs[1]}) // ir_float.go
	}
	l := b.expr(x.X)
	pre, r := b.collect(func() ([]*irStmt, *irExpr) { return nil, b.expr(x.Y) })
	if len(pre) == 0 {
		return &irExpr{K: "bin", Pos: p, Op: op, X: l, Y: r}
	}
	t := b.temp("b", b.goType(b.l.info.Types[x], x), p)
	test := &irExpr{K: "local", Pos: p, I: t}
	if op == "||" {
		test = &irExpr{K: "un", Pos: p, Op: "!", X: test}
	}
	then := append(pre, &irStmt{K: "set", Pos: p, Local: t, X: r})
	b.runFirst([]*irStmt{
		{K: "def", Pos: p, Local: t, X: l},
		{K: "if", Pos: p, End: past(p, pre), X: test, Then: then},
	})
	return &irExpr{K: "local", Pos: p, I: t}
}

// condOf is a condition and the statements it runs first.
func (b *irBuilder) condOf(x syntax.Expr) (pre []*irStmt, cond *irExpr) {
	b.part(func() { pre, cond = b.value(x) })
	return pre, cond
}

// ifStmts is the if s after the statements its condition runs first; an
// else-if's run inside the else block (ifStmt).
func (b *irBuilder) ifStmts(s *syntax.IfStmt, ret bool, to *yieldTo) []*irStmt {
	pre, cond := b.condOf(s.Cond)
	st := b.ifWith(s, cond, ret, to)
	st.Pos = past(st.Pos, pre) // ir_match.go: DWARF scope positions must not decrease
	return append(pre, st)
}

// whileStmt is the loop s. A condition that runs statements runs them at
// the top of each iteration, as the emitter's loop does:
//
//	for { pre; if !cond { break }; body }
//
// A continue reaches the top, so it runs them again.
func (b *irBuilder) whileStmt(s *syntax.ForStmt) []*irStmt {
	var pre []*irStmt
	var cond *irExpr
	if s.Cond != nil {
		pre, cond = b.condOf(s.Cond)
	}
	loop := &irStmt{K: "while", Pos: b.pos(s, s.Pos()), X: cond, End: b.pos(s.Body, s.Body.Rbrace)}
	// The condition's facts hold in the body (bounds.go; ir_bounds.go);
	// the loop's head killed what the body writes (stmt).
	mark := b.fc.factMark()
	if s.Cond != nil {
		b.fc.assume(s.Cond)
	}
	ctr := b.loopBody(loop, s, s.Body) // ir_stmt.go
	b.fc.factReset(mark)
	if len(pre) > 0 {
		p := past(b.pos(s.Cond, s.Cond.Pos()), pre)
		loop.X = nil
		exit := &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: cond}, Then: []*irStmt{{K: "break", Pos: p}}}
		loop.Body = append(append(pre, exit), loop.Body...)
	}
	return append(ctr, loop)
}

// rangeValue is a range's domain and the statements it runs first, before
// the loop: Go evaluates the domain once.
func (b *irBuilder) rangeValue(x syntax.Expr) (pre []*irStmt, e *irExpr) {
	b.part(func() { pre, e = b.value(x) })
	return pre, e
}
