package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// scriptLit makes one field per operation, in the derived instance's order.
// As in the text emitter, each field is initialized by its checked Ergo
// constructor or by the runtime constructor named by its Go binding.
func (b *irBuilder) scriptLit(n *check.Named, x *syntax.CompositeLit, lit *irExpr) *irExpr {
	for _, f := range scriptFields(n, x) {
		lit.Keys = append(lit.Keys, memberName(f.Name))
		lit.Elems = append(lit.Elems, b.scriptField(n, f, x, lit.Pos))
	}
	return lit
}

func (b *irBuilder) scriptField(n *check.Named, f *check.Field, x syntax.Node, p irPos) *irExpr {
	if pc := f.ProviderCalls(); pc != nil {
		// The checker requires a plain generic function with no arguments,
		// failures, capability requirements or witnesses (check/provider.go).
		fd := b.l.useFunc(pc.New, x)
		call := &irExpr{K: "call", Pos: p, Name: fd.name, T: b.sigTypeArgs(pc.New.Sig, pc.TypeArgs, x)}
		if !fd.envFree {
			if b.env < 0 {
				irNo("provider constructor without an environment")
			}
			call.Args = []*irExpr{{K: "local", Pos: p, I: b.env}}
		}
		return call
	}
	op, ok := f.Type.(*check.Named)
	if !ok || len(op.TArgs) != 2 {
		ice(x, "the field %s of %s is not a provider field", f.Name, n)
	}
	g, ok := check.GoTypeOf(op)
	if !ok {
		ice(x, "the field %s of %s names no runtime Go type", f.Name, n)
	}
	// The type arguments may name the enclosing function's type parameters,
	// so keep the import with a nongeneric declaration (as in ir_pkg.go).
	b.keepImport("var _ " + b.l.rt() + ".Site")
	targs := b.goType(op.TArgs[0], x) + ", " + b.goType(op.TArgs[1], x)
	args := []*irExpr{{K: "const", Pos: p, T: "string", V: n.String()}, {K: "const", Pos: p, T: "string", V: f.Name}}
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: check.ProviderFieldNew(g), V: targs, Args: args}
}

// wrapProviderLit builds a wrapping derived provider instance T[S](inner) on
// the IR path: sets the unexported inner field and initializes each operation
// field via its Ergo NewF(op, faults) constructor.
func (b *irBuilder) wrapProviderLit(x *syntax.CallExpr, n *check.Named, p irPos) ([]*irStmt, *irExpr, bool) {
	pre, innerVal := b.value(x.Args[0])
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(n, x)}
	lit.Keys = append(lit.Keys, "inner")
	lit.Elems = append(lit.Elems, innerVal)
	for _, f := range scriptFields(n, x) {
		pc := f.ProviderCalls()
		if pc == nil || !pc.Wrapping {
			irNo("wrapping provider field without Ergo constructor")
		}
		op, ok := f.Type.(*check.Named)
		if !ok || len(op.TArgs) != 2 {
			ice(x, "the field %s of %s is not a provider field", f.Name, n)
		}
		sig, ok := op.TArgs[1].(*check.Signature)
		if !ok {
			ice(x, "the field %s of %s has no operation signature", f.Name, n)
		}
		errTypes := check.ErrorRowTypes(sig.Errors)
		faultElems := make([]*irExpr, len(errTypes))
		for i, et := range errTypes {
			name := et.String()
			if en, ok := et.(*check.Named); ok && en.Obj != nil {
				name = en.Obj.Name
			}
			faultElems[i] = &irExpr{K: "const", Pos: p, T: "string", V: name}
		}
		faultSlice := &irExpr{K: "lit", Pos: p, T: "[]string", Elems: faultElems}
		fd := b.l.useFunc(pc.New, x)
		call := &irExpr{K: "call", Pos: p, Name: fd.name, T: b.sigTypeArgs(pc.New.Sig, pc.TypeArgs, x)}
		if !fd.envFree {
			if b.env < 0 {
				irNo("wrapping provider constructor without an environment")
			}
			call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: b.env})
		}
		call.Args = append(call.Args, &irExpr{K: "const", Pos: p, T: "string", V: f.Name}, faultSlice)
		lit.Keys = append(lit.Keys, memberName(f.Name))
		lit.Elems = append(lit.Elems, call)
	}
	return pre, lit, false
}
