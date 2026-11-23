package lower

// IR builder: the row gaps (rowgap.go) at the IR's one place for a value,
// implicit (ir_union.go): what x reads is read as x's type holds it, then
// x's implicit conversion, then the value goes into its place. The
// adapters are the emitter's: a record into Go's error ("toiface"), a
// record out of it ("assert"), and a function wrapped in a literal that
// asserts or converts its parameters and calls it (rowFunc).

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) goTypeAt(site syntax.Node) func(check.Type) string {
	return func(t check.Type) string { return b.goType(t, site) }
}

// rowWrite is e, the value of x, as the place x goes into holds it.
func (b *irBuilder) rowWrite(x syntax.Expr, e *irExpr) *irExpr {
	decl := b.l.rowWriteDecl(x)
	if decl == nil {
		return e
	}
	have := b.l.haveOf(x)
	g := rowGapOf(b.goTypeAt(x), decl, have)
	switch {
	case g.whole:
		return &irExpr{K: "toiface", Pos: e.Pos, T: "error", X: e}
	case len(g.params) > 0:
		s := have.(*check.Signature)
		er := erasedSig(s, g.params)
		return b.rowFunc(s, er, b.sigType(s, x), b.sigType(er, x), g.params, e, x)
	}
	return e
}

// rowRead is e, the value x reads from a declaration's place, as x's type
// holds it.
func (b *irBuilder) rowRead(x syntax.Expr, e *irExpr) *irExpr {
	decl := b.l.rowReadDecl(x)
	if decl == nil {
		return e
	}
	have := b.l.info.Types[x]
	g := rowGapOf(b.goTypeAt(x), decl, have)
	switch {
	case g.whole:
		return &irExpr{K: "assert", Pos: e.Pos, T: b.goType(have, x), X: e}
	case len(g.params) > 0:
		s := have.(*check.Signature)
		er := erasedSig(s, g.params)
		return b.rowFunc(er, s, b.sigType(er, x), b.sigType(s, x), nil, e, x)
	}
	return e
}

// rowFunc is e, a function value of from (Go type fromGo), as one of to
// (Go type toGo), as the emitter's rowFunc writes it:
//
//	func(f From) To { return func(env *Env, p error) (R, error) { return f(env, p.(Boom)) } }(e)
//
// A parameter at asserts is asserted to from's; another whose Go type is
// not from's is converted to it (a record into Go's error).
func (b *irBuilder) rowFunc(from, to *check.Signature, fromGo, toGo string, asserts []int, e *irExpr, site syntax.Node) *irExpr {
	if len(from.Params) != len(to.Params) || len(to.Results) > 1 {
		irNo("row adapter of another shape")
	}
	assert := map[int]bool{}
	for _, i := range asserts {
		assert[i] = true
	}
	p := e.Pos
	end := p
	f := b.temp("f", fromGo, p)
	// The environment's type is the first parameter's of to's function
	// type as a function value of Ergo spells it (sigType), so this body
	// names no Env type of its own (TestRuntime199EnvTypeReaders).
	sig := strings.TrimPrefix(b.sigType(to, site), "func(")
	cut := strings.IndexAny(sig, ",)")
	if cut < 0 {
		irNo("row adapter of another shape")
	}
	env := b.temp("e", sig[:cut], p)
	call := &irExpr{K: "vcall", Pos: p, X: &irExpr{K: "local", Pos: p, I: f}, Args: []*irExpr{{K: "local", Pos: p, I: env}}}
	inner := &irExpr{K: "func", Pos: p, T: toGo, Params: []int{env}, End: &end}
	for i, tp := range to.Params {
		gt, gf := b.goType(tp.Type, site), b.goType(from.Params[i].Type, site)
		local := b.temp("p", gt, p)
		inner.Params = append(inner.Params, local)
		arg := &irExpr{K: "local", Pos: p, I: local}
		switch {
		case assert[i]:
			arg = &irExpr{K: "assert", Pos: p, T: gf, X: arg}
		case gt != gf:
			arg = &irExpr{K: "toiface", Pos: p, T: gf, X: arg}
		}
		call.Args = append(call.Args, arg)
	}
	inner.Body = []*irStmt{{K: "ret", Pos: p, X: call}}
	outer := &irExpr{K: "func", Pos: p, T: "func(" + fromGo + ") " + toGo, Params: []int{f}, Body: []*irStmt{{K: "ret", Pos: p, X: inner}}, End: &end}
	return &irExpr{K: "vcall", Pos: p, X: outer, Args: []*irExpr{e}}
}
