// Go package types and calls on the IR (FLIP F7 FORK-GOCALL): a type of
// an import go package is Go's own type, which the IR names by its import
// path (PATH.NAME, ir_pkg.go spelling), and go.Ref[T] is Go's *T, as the
// emitter's goType writes them (docs/spec/interop.md, Lowering). A call
// of a Go package function is its Go target's "pcall" (ir_tuple.go
// hostCall), here widened to the emitter's goCall boundary forms the
// decoder spells with gc's own nodes.

package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// hostArgs are the IR operands of x, a call of the Go package function f,
// after their statements: each in Go's own type (a basic type, a Go
// package's type), and an Ergo function value for a Go function
// parameter through goCallback's adapter (ir_adapt.go adaptOperand): the
// value runs with the caller's environment, its Go parameters and result
// basic, its error dropped (a callback type with no fails row never
// fails). A Go function passed as is (goDirect), a callback that fails
// (v1.GoCallbackError's tunnel) and any other operand keep the source
// body.
func (b *irBuilder) hostArgs(x *syntax.CallExpr, f *check.Func, p irPos) ([]*irStmt, []*irExpr) {
	shapes := make([]*v1.Body, len(f.Sig.Params))
	for i, q := range f.Sig.Params {
		if cb, ok := q.Type.(*check.Signature); ok {
			shapes[i] = b.callbackShape(cb, x)
			if b.fc.hostFuncRef(x.Args[i]) != nil {
				irNo("Go function passed to a Go package function")
			}
			continue
		}
		if !b.hostValue(q.Type) {
			irNo("call of a Go package function")
		}
	}
	pre, args := b.operands(x.Args) // ir_fail.go
	for i, shape := range shapes {
		if shape == nil {
			continue
		}
		t := b.l.info.Types[x.Args[i]]
		if n, ok := t.(*check.Named); ok {
			t = n.Underlying()
		}
		sig, _ := t.(*check.Signature)
		bind, e := b.adaptOperand(sig, x.Args[i], args[i], v1.Param{Func: shape}, p)
		pre, args[i] = append(pre, bind...), e
	}
	return pre, args
}

// callbackShape is the Go function a Go package function's callback
// parameter cb stands for, as goCallback writes it, when the IR adapts
// it: basic parameters, at most one basic result, no fails row.
func (b *irBuilder) callbackShape(cb *check.Signature, site syntax.Node) *v1.Body {
	if cb.Fallible || len(cb.Results) > 1 {
		irNo("Go callback that fails or has several results")
	}
	shape := &v1.Body{}
	for _, q := range cb.Params {
		if !hostBasic(q.Type) {
			irNo("Go callback of another shape")
		}
		shape.Params = append(shape.Params, v1.Param{Type: b.goType(q.Type, site)})
	}
	for _, r := range cb.Results {
		if !hostBasic(r) {
			irNo("Go callback of another shape")
		}
		shape.Results = append(shape.Results, b.goType(r, site))
	}
	return shape
}

// hostGeneric reports a Go package function with a type parameter Go
// sees (slices.IndexFunc's E): a row parameter (sort.Search's CR) is
// Ergo's alone, erased from Go's call as typeArgs erases it.
func hostGeneric(f *check.Func) bool {
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			return true
		}
	}
	return false
}

// hostValue reports a type a Go package function's operand or result
// passes as is (goCall with nothing to adapt): a basic type, or a Go
// package's own named type (time.Month) that is not go.Error (goResult's
// go.Error, IG5), go.Ref (its nil check) or a value that holds a slice or
// a Map (the value rule's copy at the boundary).
func (b *irBuilder) hostValue(t check.Type) bool {
	if hostBasic(t) {
		return true
	}
	n, ok := t.(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Host() && !check.IsGoError(n) && !isGoRef(n) && len(n.TArgs) == 0 && !b.l.holdsValue(n)
}

// admitHost admits t, a type of a Go package or a go.Ref of one (gocall.go
// isGoRef): Go's own type, with nothing of the program's to declare.
// go.Ref[T]'s T is admitted as any type.
func (b *irBuilder) admitHost(t *check.Named, depth int) {
	if elem, ok := check.GoRefElem(t); ok {
		b.admit(elem, depth+1)
		return
	}
	if len(t.TArgs) > 0 {
		irNo("generic Go package type")
	}
}
