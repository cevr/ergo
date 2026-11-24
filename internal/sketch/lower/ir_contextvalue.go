package lower

// IR builder: Context values (rows.md, Context values; the emitter's
// contextvalues.go, whose rule this is). A conversion of a context to a
// Context[R] builds the providers once, where the value is made, into a
// snapshot of the environment (a named context's builder, or an inline or
// a local context's providers in place, publish), and the value is a
// function of the uniform shape that copies R's fields from the snapshot
// into the environment it is given. `with v { b }` copies the
// environment, calls v on the copy and runs b against it (installWith).
//
// Prior art: Scala 3's given instances and Koka's handlers pass a
// dictionary of providers as a value; Ergo's value is the closure that
// writes them, one call per with.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// contextValue is the Context[R] value of x, whose fact is w
// (Info.ContextValues): the snapshot's statements run before the
// expression that holds it (runFirst, ir_order.go).
func (b *irBuilder) contextValue(x syntax.Expr, w *check.With, p irPos) *irExpr {
	if len(w.Contexts) == 0 {
		irNo("context value")
	}
	env, envp := b.envValue(), b.envPointer()
	snap := b.temp("ctx", env, p)
	ptr := b.temp("ctxp", envp, p)
	// The snapshot copies the environment; a pure function binds none, so
	// its providers see only what the snapshot binds (the emitter's
	// `var snap Env`).
	pre := []*irStmt{{K: "var", Pos: p, Local: snap}}
	if b.env >= 0 {
		pre = []*irStmt{{K: "def", Pos: p, Local: snap, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}}}
	}
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: ptr, X: &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: snap}}})
	if w.Contexts[0].Lit != nil {
		b.l.localContext(w.Contexts[0], x)
		outer := b.env
		if outer < 0 {
			outer = ptr // the emitter's outer (&snap) in a pure function
		}
		pre = append(pre, b.publish(w, ptr, outer, -1, x)...) // ir_with.go
	} else {
		// A named context has a declaration, whichever package holds it;
		// its builder is emitted with it (contextBuilder). The IR asks
		// nothing about where it is declared (no std membership).
		if w.Contexts[0].Decl == nil {
			irNo("context value of an undeclared context")
		}
		build := b.l.contextBuilder(w, x)
		pre = append(pre, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "call", Pos: p, Name: build, Args: []*irExpr{{K: "local", Pos: p, I: ptr}}}})
	}
	e := b.temp("e", envp, p)
	lit := &irExpr{K: "func", Pos: p, T: b.sigType(&check.Signature{}, x), Params: []int{e}}
	for _, c := range w.Discharged {
		n, ok := c.(*check.Named)
		if !ok {
			irNo("context value of a row element that is no capability")
		}
		f := b.l.envField(n)
		lit.Body = append(lit.Body, &irStmt{K: "store", Pos: p,
			X: &irExpr{K: "field", Pos: p, Name: f, X: &irExpr{K: "local", Pos: p, I: e}},
			Y: &irExpr{K: "field", Pos: p, Name: f, X: &irExpr{K: "local", Pos: p, I: snap}}})
	}
	lit.Body = append(lit.Body, &irStmt{K: "ret", Pos: p, X: &irExpr{K: "zero", Pos: p, T: "struct{}"}})
	end := p
	lit.End = &end
	b.runFirst(pre)
	return lit
}

// installWith is operand i of the with x, arg, whose value is a
// Context[R] (With.Installs): the value, then a copy of the environment
// it is called on, and what the operand holds against the copy (rest).
func (b *irBuilder) installWith(x *syntax.CallExpr, i int, w *check.With, arg syntax.Expr, ret bool, to *yieldTo) []*irStmt {
	pre, v := b.value(arg)
	p := past(b.pos(arg, arg.Pos()), pre)
	env, envp := b.envValue(), b.envPointer()
	cp := b.temp("env", env, p)
	ptr := b.temp("envp", envp, p)
	unit, errv := b.temp("unit", "struct{}", p), b.temp("err", "error", p)
	out := append(pre,
		&irStmt{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}},
		&irStmt{K: "def", Pos: p, Local: ptr, X: &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: cp}}},
		// The value never fails (its error is nil): its pair is bound and
		// dropped, as the emitter's statement drops it (irUseLocals).
		&irStmt{K: "def2", Pos: p, Local: unit, Val: &errv, X: &irExpr{K: "vcall", Pos: p, X: v, Args: []*irExpr{{K: "local", Pos: p, I: ptr}}}})
	return append(out, b.withRest(x, i, w, ptr, ret, to)...)
}
