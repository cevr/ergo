package check

import "ergo.local/prototype/internal/sketch/syntax"

// A fresh Go handle (interop.md, Checking 4; NP4f-URL): a field read
// through a go.Ref charges go.Foreign, since Go code may write that
// memory. A handle that a pure-tier Go call has just returned, bound by an
// immutable let and used only as the operand of a field read or a method
// call in the frame that binds it, is seen by no other Go code but the
// pure-tier methods the frame calls on it, so a field read through it
// charges nothing: `let u = try url.Parse(raw)` then `u.Scheme`. A use
// that passes the handle on (an argument, a result, a stored or captured
// value) leaves every field read through it charged.

// markFreshRef marks v a fresh Go handle when its let binds value, a
// call of a Go function or method whose binding needs nothing (the pure
// tier), and every use of its name in the frame's body is the operand of
// a selector outside any function literal.
func (c *checker) markFreshRef(v *Var, value syntax.Expr) {
	if v == nil || v.mut || c.fn == nil || c.fn.fn == nil || c.fn.fn.Decl == nil || c.fn.fn.Decl.Body == nil {
		return
	}
	if _, ok := GoRefElem(v.Type); !ok {
		return
	}
	x := unparen(value)
	for {
		switch e := x.(type) {
		case *syntax.TryExpr:
			x = unparen(e.X)
			continue
		case *syntax.CatchExpr:
			x = unparen(e.X)
			continue
		}
		break
	}
	ce, ok := x.(*syntax.CallExpr)
	if !ok {
		return
	}
	call := c.pkg.Info.Calls[ce]
	if call == nil || !c.goCall(call) {
		return
	}
	sig := call.Func.Sig
	if call.Kind == CallOp {
		sig = call.Op.Sig
	}
	if sig == nil || !sig.Uses.Empty() {
		return
	}
	if onlySelected(c.fn.fn.Decl.Body, v.Name, v.id) {
		v.freshRef = true
	}
}

// freshOperand reports x as a name of a fresh Go handle.
func (c *checker) freshOperand(x syntax.Expr) bool {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return false
	}
	v, ok := c.pkg.Info.Uses[id].(*Var)
	return ok && v.freshRef
}

// onlySelected reports whether every identifier name in body, other than
// the declaring decl, is the operand of a selector (x.F, x.M()) outside
// any function literal.
func onlySelected(body *syntax.BlockStmt, name string, decl *syntax.Ident) bool {
	operands := map[*syntax.Ident]bool{}
	sels := map[*syntax.Ident]bool{}
	ok := true
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			syntax.Inspect(n.Body, func(m syntax.Node) bool {
				if id, is := m.(*syntax.Ident); is && id.Name == name {
					ok = false
				}
				return ok
			})
			return false
		case *syntax.SelectorExpr:
			if id, is := unparen(n.X).(*syntax.Ident); is && id.Name == name {
				operands[id] = true
			}
			sels[n.Sel] = true
		case *syntax.Ident:
			if n.Name == name && n != decl && !operands[n] && !sels[n] {
				ok = false
			}
		}
		return ok
	})
	return ok
}
