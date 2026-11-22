package lower

// IR builder: InForce (IR-INFORCE; rows.md, Context values, InForce). The
// prelude's InForce[R]() has no Go body to call (decls.go intrinsic writes
// a panic): each call is written in place, as the emitter's inForce
// (inforce.go) writes it. The rule is that function's; this is its IR form.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// inForce is the call x of InForce[R](): a copy of the environment taken
// at the call, and the Context[R] value over it, a function of the uniform
// shape that checks the innermost owning with in force at the call has not
// ended (v1.HeldLive) and copies R's fields from the copy into the
// environment it is given (copyEnv of R's copier type, heldArg).
func (b *irBuilder) inForce(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr, bool) {
	if len(c.TypeArgs) != 1 {
		irNo("InForce form")
	}
	if b.env < 0 {
		irNo("InForce in a pure function") // InForce needs R: the checker gives its caller an environment
	}
	copier := b.spelling(b.fc.heldArg(c.TypeArgs[0]))
	env := b.l.env()
	snap := b.temp("held", env, p)
	pre := []*irStmt{{K: "def", Pos: p, Local: snap, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}}}
	e := b.temp("e", "*"+env, p)
	r := b.temp("r", copier, p)
	held := &irExpr{K: "local", Pos: p, I: snap}
	lit := &irExpr{K: "func", Pos: p, T: b.sigType(&check.Signature{}, x), Params: []int{e}}
	lit.Body = []*irStmt{
		{K: "expr", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "HeldLive", Args: []*irExpr{{K: "field", Pos: p, Name: envWith, X: held}}}},
		{K: "var", Pos: p, Local: r},
		{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "copyEnv", X: &irExpr{K: "local", Pos: p, I: r}, Args: []*irExpr{{K: "local", Pos: p, I: e}, held}}},
		{K: "ret", Pos: p, X: &irExpr{K: "zero", Pos: p, T: "struct{}"}},
	}
	end := p
	lit.End = &end
	return pre, lit, false
}
