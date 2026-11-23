// Go package types and calls on the IR (FLIP F7 FORK-GOCALL): a type of
// an import go package is Go's own type, which the IR names by its import
// path (PATH.NAME, ir_pkg.go spelling), and go.Ref[T] is Go's *T, as the
// emitter's goType writes them (docs/spec/interop.md, Lowering). A call
// of a Go package function is its Go target's "pcall" (ir_tuple.go
// hostCall), here widened to the emitter's goCall boundary forms the
// decoder spells with gc's own nodes.

package lower

import (
	"strconv"
	"strings"

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
// (v1.GoCallbackError's tunnel) has its own adapter (hostCallback,
// ir_golib.go). A go.Error operand is Go's error (goErrorArg). A Go
// method's receiver (c.Recv) is recv, evaluated before the operands
// (FORK-GOLIB). Any other operand keeps the source body.
func (b *irBuilder) hostArgs(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, []*irExpr) {
	list := x.Args
	receiver := c.Recv
	if c.RecvIsParam() {
		list = append([]syntax.Expr{receiver}, list...)
		receiver = nil
	}
	shapes := make([]*v1.Body, len(f.Sig.Params))
	fails := make([]*check.Signature, len(f.Sig.Params))
	for i, q := range f.Sig.Params {
		if cb, ok := q.Type.(*check.Signature); ok {
			if b.fc.hostFuncRef(list[i]) != nil {
				irNo("Go function passed to a Go package function")
			}
			if cb.Fallible {
				fails[i] = cb // ir_golib.go hostCallback
				continue
			}
			shapes[i] = b.callbackShape(cb, x)
			continue
		}
		if !b.hostValue(q.Type) && !isGoErrorArg(q.Type) && !basicSlice(q.Type) && !goAnyArg(q.Type) && !goPointerArg(q.Type) { // ir_golib.go
			irNo("call of a Go package function")
		}
	}
	operands := list
	if receiver != nil {
		b.hostRecvOf(f, receiver) // ir_golib.go
		operands = append([]syntax.Expr{receiver}, list...)
	}
	pre, args := b.operands(operands) // ir_fail.go
	var recv *irExpr
	if receiver != nil {
		recv, args = args[0], args[1:]
	}
	for i, q := range f.Sig.Params {
		if e, ok := b.goErrorArg(q.Type, args[i], p); ok { // ir_golib.go
			args[i] = e
		}
		if check.IsGoAny(q.Type) {
			args[i] = b.assignedTo(list[i], args[i], q.Type) // ir_union.go: Go's any
		}
		if wpre, w, ok := b.goWiden(b.l.info.Types[list[i]], q.Type, args[i], list[i], p); ok { // ir_govar.go
			pre, args[i] = append(pre, wpre...), w
		}
	}
	for i, shape := range shapes {
		if cb, ok := f.Sig.Params[i].Type.(*check.Signature); ok && !cb.NonEscaping {
			// goCallbacks' v1.GoHanded: an escaping callback may run on a
			// goroutine of Go's (rt.Alone).
			b.keepImport("var _ = " + b.l.rt() + ".GoHanded")
			pre = append(pre, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoHanded"}})
		}
		if fails[i] != nil {
			t := b.l.info.Types[list[i]]
			if n, ok := t.(*check.Named); ok {
				t = n.Underlying()
			}
			sig, _ := t.(*check.Signature)
			bind, lit := b.hostCallback(sig, fails[i], list[i], args[i], p)
			pre = append(pre, bind...)
			args[i] = lit
			continue
		}
		if shape == nil {
			continue
		}
		t := b.l.info.Types[list[i]]
		if n, ok := t.(*check.Named); ok {
			t = n.Underlying()
		}
		sig, _ := t.(*check.Signature)
		bind, e, _ := b.adaptOperand(sig, list[i], args[i], v1.Param{Func: shape}, p)
		pre, args[i] = append(pre, bind...), e
	}
	return pre, recv, args
}

// callbackShape is the Go function a Go package function's callback
// parameter cb stands for, as goCallback writes it, when the IR adapts
// it: basic parameters, at most one basic result, no fails row (a
// callback that fails is hostCallback's).
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

// hostCloseCall is the release of a Go value at slot (the emitter's
// hostRelease): Go's Close inside v1.Release's boundary with the caller's
// task, its error, when Close returns one, a defect naming the site where
// the slot took the value (v1.CloseFailed; staticOwn's site). A Go value that is not a pointer (a value whose
// Close may need Go's implicit address) keeps the Go-text release.
func (b *irBuilder) hostCloseCall(cl *check.Close, s irSlot, env, slot int, p irPos) (*irExpr, bool) {
	if !strings.HasPrefix(b.fc.goType(s.t), "*") {
		return nil, false
	}
	call := &irExpr{K: "mcall", Pos: p, Name: "Close", X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: slot}}}
	if cl.HostFallible() {
		site := s.site
		src, line, column := b.l.locate(site, site.Pos())
		at := src + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(column)
		b.keepImport("var _ = " + b.l.rt() + ".CloseFailed")
		call = &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "CloseFailed", Args: []*irExpr{{K: "const", Pos: p, T: "string", V: at}, call}}
	}
	end := p
	lit := &irExpr{K: "func", Pos: p, T: "func()", Body: []*irStmt{{K: "expr", Pos: p, X: call}}, End: &end}
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: env}}
	b.keepImport("var _ = " + b.l.rt() + ".Release")
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "Release", Args: []*irExpr{task, lit}}, true
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
	if a, ok := t.(*check.Array); ok && hostBasic(a.Elem) {
		return true // a Go array of basic values ([32]byte, sha256.Sum256): a value (PORT-CALLS-2)
	}
	n, ok := t.(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Host() && !check.IsGoError(n) && !isGoRef(n) && len(n.TArgs) == 0 && !b.l.holdsValue(n)
}

// admitHost admits t, a type of a Go package or a go.Ref of one (gocall.go
// isGoRef): Go's own type, with nothing of the program's to declare.
// go.Ref[T]'s T is admitted as any type. go.Error is the runtime's
// v1.GoError (FORK-GOLIB; decls.go goErrorDecl).
func (b *irBuilder) admitHost(t *check.Named, depth int) {
	if check.IsGoError(t) {
		return
	}
	if elem, ok := check.GoRefElem(t); ok {
		b.admit(elem, depth+1)
		return
	}
	if len(t.TArgs) > 0 {
		irNo("generic Go package type")
	}
}
