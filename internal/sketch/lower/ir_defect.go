package lower

// IR builder: the prelude's Unreachable(reason) (defect.go). A call raises
// a defect that holds the reason and the call's site, as a Go panic, as the
// emitter writes it:
//   - a statement, or the last statement of a body or branch, is
//     panic(ergo.UnreachableAt(reason, "file:line:col")), which Go counts
//     as terminating, so a branch that ends in it needs no value;
//   - a value position is ergo.Never[T](reason, "file:line:col"), T the
//     type the checker gave the call.
// Its Go body (runtime/v1 Unreachable) is never called, so the call needs
// no runtime binding.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unreachableCall is x as a call of the prelude's Unreachable, or nil.
func (b *irBuilder) unreachableCall(x syntax.Expr) *syntax.CallExpr {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok || !check.IsUnreachable(b.l.info.Calls[call]) {
		return nil
	}
	return call
}

// unreachableArgs are the defect's Go arguments: the reason, then the
// call's site as file:line:column (the emitter's unreachableArgs).
func (b *irBuilder) unreachableArgs(x *syntax.CallExpr) ([]*irStmt, []*irExpr) {
	if len(x.Args) != 1 || x.Trailing != nil {
		irNo("Unreachable form")
	}
	pre, reason := b.value(x.Args[0])
	p := b.pos(x, x.Pos())
	site := &irExpr{K: "const", Pos: p, T: "string", V: b.l.siteText(x)}
	b.keepImport("var _ = " + b.l.rt() + ".UnreachableAt")
	return pre, []*irExpr{reason, site}
}

// unreachableStmt is the call x as the Go panic statement.
func (b *irBuilder) unreachableStmt(x *syntax.CallExpr) []*irStmt {
	pre, args := b.unreachableArgs(x)
	p := b.pos(x, x.Pos())
	defect := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "UnreachableAt", Args: args}
	return append(pre, &irStmt{K: "panic", Pos: p, X: defect})
}

// unreachableValue is the call x where its value is used: Never of the
// type the checker gave it there.
func (b *irBuilder) unreachableValue(x *syntax.CallExpr) ([]*irStmt, *irExpr, bool) {
	t := b.l.info.Types[x]
	if t == nil || t == check.NoValue {
		irNo("Unreachable in a value position with no type")
	}
	pre, args := b.unreachableArgs(x)
	p := b.pos(x, x.Pos())
	return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "Never", V: b.goType(t, x), Args: args}, false
}

// dieStmt lowers `die <expr>` to panic(ergo.DieAt(val, "file:line:col"))
// (NO-UNREACHABLE §3). IR-only: evaluates s.X once under ordinary failure/needs
// rules and packages the owned nominal payload only on the cold exit edge.
func (b *irBuilder) dieStmt(s *syntax.DieStmt) []*irStmt {
	pre, val := b.value(s.X)
	p := b.pos(s, s.Pos())
	boxed := &irExpr{K: "toiface", Pos: p, T: "any", X: val}
	site := &irExpr{K: "const", Pos: p, T: "string", V: b.l.siteText(s)}
	b.keepImport("var _ = " + b.l.rt() + ".DieAt")
	defect := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "DieAt", Args: []*irExpr{boxed, site}}
	return append(pre, &irStmt{K: "panic", Pos: p, X: defect})
}
