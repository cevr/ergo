package check

import "ergo.local/prototype/internal/sketch/syntax"

// reportGoCopyWrites reports the write a Go method would lose on a copy
// (interop.md, Lowering 3; H5). Go's own Map or slice (a Go call's result,
// or a field read through a go.Ref) bound by an immutable let stays Go's
// while every use of the let is a whole operand of a Go call. A let that
// the program also uses in another way is an Ergo value, copied where it
// is bound; then a Go method with no result called on it can only write
// that copy, and Go never sees the write. That call is S108, at its
// receiver: the program must keep the let Go's or write through Go's value.
// A Go method with a result (h.Get) reads, and keeps the value rule.
func (c *checker) reportGoCopyWrites(body *syntax.BlockStmt) {
	if body == nil {
		return
	}
	held := map[*Var]bool{}
	operand := map[*syntax.Ident]bool{}
	var writes []*syntax.Ident
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.DeclStmt:
			for _, spec := range n.Decl.Specs {
				if n.Decl.Mut.IsValid() || spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
					continue
				}
				if v, ok := c.pkg.Info.Defs[spec.Names[0]].(*Var); ok && c.goHeld(spec.Values[0]) {
					held[v] = true
				}
			}
		case *syntax.CallExpr:
			call := c.pkg.Info.Calls[n]
			if call == nil || !c.goCall(call) {
				return true
			}
			list := n.Args
			if call.Recv != nil {
				list = append([]syntax.Expr{call.Recv}, list...)
			}
			for _, a := range list {
				if id, ok := unparen(a).(*syntax.Ident); ok {
					operand[id] = true
				}
			}
			if id, ok := unparen(call.Recv).(*syntax.Ident); ok && call.Recv != nil && len(c.goResults(call)) == 0 {
				writes = append(writes, id)
			}
		}
		return true
	})
	if len(held) == 0 {
		return
	}
	ergoUse := map[*Var]bool{}
	syntax.Inspect(body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && !operand[id] && c.pkg.Info.Defs[id] == nil {
			if v, ok := c.pkg.Info.Uses[id].(*Var); ok && held[v] {
				ergoUse[v] = true
			}
		}
		return true
	})
	for _, id := range writes {
		if v, ok := c.pkg.Info.Uses[id].(*Var); ok && held[v] && ergoUse[v] {
			c.errorf(id, CodeType, "%s is a copy of the Map or slice Go gave it, because the program also uses %s as an Ergo value; this Go method writes only the copy, so Go never sees the write: pass %s only to Go calls, or call the method on Go's value directly (interop.md, Lowering 3)", id.Name, id.Name, id.Name)
		}
	}
}

// goHeld reports x as Go's own Map or slice: a Go call's result, or a
// field read through a go.Ref (lower/values.go, goHeld).
func (c *checker) goHeld(x syntax.Expr) bool {
	x = unparen(x)
	if t, ok := x.(*syntax.TryExpr); ok {
		x = unparen(t.X)
	}
	switch under(c.pkg.Info.Types[x]).(type) {
	case *Map, *Slice:
	default:
		return false
	}
	switch x := x.(type) {
	case *syntax.CallExpr:
		call := c.pkg.Info.Calls[x]
		return call != nil && c.goCall(call)
	case *syntax.SelectorExpr:
		_, ref := GoRefElem(c.pkg.Info.Types[x.X])
		return ref
	}
	return false
}

// goCall reports a call of import go: a Go function or a Go type's method.
func (c *checker) goCall(call *Call) bool {
	switch call.Kind {
	case CallFunc:
		return call.Func != nil && call.Func.Pkg != nil && call.Func.Pkg.Host()
	case CallOp:
		return call.Recv != nil && call.Op != nil && goNamedRecv(c.pkg.Info.Types[call.Recv])
	}
	return false
}

// goResults are the results of the Go call.
func (c *checker) goResults(call *Call) []Type {
	if call.Kind == CallOp {
		return call.Op.Sig.Results
	}
	return call.Func.Sig.Results
}

// goNamedRecv reports a receiver of a Go type: a value, a go.Ref or a
// borrow.
func goNamedRecv(t Type) bool {
	if e, ok := GoRefElem(t); ok {
		t = e
	}
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	return ok && n.Obj.Pkg != nil && n.Obj.Pkg.Host()
}
