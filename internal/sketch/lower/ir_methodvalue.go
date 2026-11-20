package lower

// IR builder: method values (CV1; types.md, Methods 2; check.Info's
// MethodValues). x.f, the function f on x's type taken as a value, binds
// x's value where the expression is evaluated, as Go's method value binds
// its receiver: the receiver is evaluated once, into a local of its value
// type (a borrow's value is copied, as a call's argument is), and the
// closure calls f with that local first. The closure has the uniform shape
// (declaredLit, ir_fnname.go), or, when a call passes the value to a
// known-callback variant (knownSite: pureMethodValue), the plain func(…)
// R that calls f directly (plainMethodValue), as the emitter's
// methodValue196 writes both.
//
// Prior art: Go's method value t.M evaluates and copies t at the
// expression; OCaml and Haskell partially apply the function to its first
// argument. Gleam, ReScript and Elixir have no method values: a capture
// (`fn(x) { t.m(x) }`, `&Mod.f(t, &1)`) is written by hand.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// methodValue is the method value x: the receiver's statements and its
// local first, then the closure over that local.
func (b *irBuilder) methodValue(x *syntax.SelectorExpr) ([]*irStmt, *irExpr) {
	f := b.l.info.MethodValues[x]
	sig, ok := b.l.info.Types[x].(*check.Signature)
	if f == nil || !ok || len(f.Sig.Params) == 0 {
		irNo("method value without a function type")
	}
	p := b.pos(x, x.Pos())
	pre, recv := b.value(x.X)
	if _, ptr := b.l.info.Types[x.X].(*check.Pointer); ptr {
		recv = &irExpr{K: "deref", Pos: p, X: recv} // a borrow's value, copied as a call's argument is
	}
	r := b.temp("r", b.goType(f.Sig.Params[0].Type, x), p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: r, X: recv})
	local := &irExpr{K: "local", Pos: p, I: r}
	if b.fc.plainMVs[x] {
		return pre, b.plainMethodValue(f, sig, x, p, local)
	}
	return pre, b.declaredLit(f, sig, x, p, local, nil)
}

// stableMethodValue is the method value x where no statement can run
// before it (exprOf): its receiver must be a local that is never assigned
// (not a let mut) and holds a value, so the closure's read of it is the
// value it has here.
func (b *irBuilder) stableMethodValue(x *syntax.SelectorExpr, p irPos) *irExpr {
	f := b.l.info.MethodValues[x]
	sig, ok := b.l.info.Types[x].(*check.Signature)
	id, isID := unparen(x.X).(*syntax.Ident)
	if f == nil || !ok || !isID {
		irNo("method value of a receiver that needs evaluating, outside a value position")
	}
	v, isVar := b.l.info.Uses[id].(*check.Var)
	if _, ptr := b.l.info.Types[x.X].(*check.Pointer); !isVar || v.Mutable() || ptr {
		irNo("method value of a receiver that needs evaluating, outside a value position")
	}
	recv := b.expr(x.X)
	if b.fc.plainMVs[x] {
		return b.plainMethodValue(f, sig, x, p, recv)
	}
	return b.declaredLit(f, sig, x, p, recv, nil)
}

// plainMethodValue is the known pure method value x as the plain closure
// func(params) R over recv: a direct call of f, which takes no environment
// and cannot fail (pureMethodValue), its value returned as it is.
func (b *irBuilder) plainMethodValue(f *check.Func, sig *check.Signature, x *syntax.SelectorExpr, p irPos, recv *irExpr) *irExpr {
	fd := b.l.useFunc(f, x)
	lit := &irExpr{K: "func", Pos: p, T: b.plainSigType(sig, x)}
	args := []*irExpr{recv}
	for _, prm := range sig.Params {
		i := b.temp("p", b.goType(prm.Type, x), p)
		lit.Params = append(lit.Params, i)
		args = append(args, &irExpr{K: "local", Pos: p, I: i})
	}
	call := &irExpr{K: "call", Pos: p, Name: fd.name, Args: args}
	if fd.method != nil {
		call = &irExpr{K: "mcall", Pos: p, Name: fd.name, X: args[0], Args: args[1:]}
	}
	if len(sig.Results) == 1 {
		lit.Body = []*irStmt{{K: "ret", Pos: p, X: call}}
	} else {
		lit.Body = []*irStmt{{K: "expr", Pos: p, X: call}}
	}
	end := p
	lit.End = &end
	return lit
}
