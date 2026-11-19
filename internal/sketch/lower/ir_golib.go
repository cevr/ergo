// The rest of goCall's boundary on the IR (FLIP F7 FORK-GOLIB): what a
// third-party Go library needs (examples/admit over golang.org/x/sync).
// A Go method on its receiver, a Go call that fails with go.Error, the
// task's context as a hidden first argument, a variadic call's spread, a
// go.Error crossing as Go's error, a callback that fails, and an
// Option of a go.Ref, Go's own pointer (the niche, flatunion.go). Each is
// the emitter's form (gocall.go, gocallback.go), built from IR nodes the
// decoder already spells with gc's own; the spread is the one decoder
// change (ergoir_call.go: a call's Op "..." is Go's HasDots).

package lower

import (
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goTarget is the Go call of x, a call of f of import go, after its
// operands' statements (hostTarget, ir_tuple.go): a "pcall" of a Go
// package function or an "mcall" of a Go method on its receiver, the
// task's context first when the binding hides one, and the last operand
// spread when Go's function is variadic. A fallible call is bound by
// hostBind, which converts its error; a Go result passes as is (a basic
// type, a Go type, or an Option of a go.Ref, whose pointer is the niche).
func (b *irBuilder) goTarget(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr) {
	switch {
	case f.Decl == nil || f.Decl.Extern == nil || f.Decl.Extern.Target == nil, hostGeneric(f), // ir_gocall.go
		len(x.Args) != len(f.Sig.Params):
		irNo("call of a Go package function")
	case f.Sig.Fallible && !failsHere(c):
		irNo("Go call whose site drops its failure")
	}
	ctx := check.GoContext(f)
	if ctx && b.env < 0 {
		irNo("Go context call from a pure function")
	}
	for _, r := range hostResults(f) { // ir_tuple.go
		if !b.hostValue(r) && !isOptionType(r) && !freshSlice(f, r) {
			irNo("Go call with a result of another type")
		}
	}
	pre, recv, args := b.hostArgs(x, c, f, p) // ir_gocall.go
	if ctx {
		task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
		b.keepImport("var _ = " + b.l.taskPkg() + ".GoContext")
		args = append([]*irExpr{{K: "pcall", Pos: p, T: taskImport, Name: "GoContext", Args: []*irExpr{task}}}, args...)
	}
	spread := ""
	if check.GoVariadic(f) {
		spread = "..."
	}
	if recv != nil && !f.Decl.Extern.FuncTarget() {
		_, _, m := f.Decl.Extern.SplitMethod()
		return pre, &irExpr{K: "mcall", Pos: p, Name: m, Op: spread, X: recv, Args: args}
	}
	if recv != nil {
		// A method whose target is a Go function (S2-FILE-B): the
		// receiver is its first argument, after a hidden context.
		at := 0
		if ctx {
			at = 1
		}
		args = append(append(append([]*irExpr{}, args[:at]...), recv), args[at:]...)
	}
	path, goName := f.Decl.Extern.Split()
	b.keepImport("var _ = " + b.l.hostPkg(path, x) + "." + goName)
	return pre, &irExpr{K: "pcall", Pos: p, T: path, Name: goName, Op: spread, Args: args}
}

// freshSlice reports r, a result of f that is a slice of basic values
// which Go made new (goFresh, a validated fresh row of internal/gofacts:
// strings.Split): the value rule copies nothing, as goResult copies
// nothing. (I-FLIP: LF2c moved the table freshGoResults into gofacts.)
func freshSlice(f *check.Func, r check.Type) bool {
	s, ok := r.(*check.Slice)
	if !ok || !hostBasic(s.Elem) || f.Decl == nil || f.Decl.Extern == nil || f.Decl.Recv != nil {
		return false
	}
	path, goName := f.Decl.Extern.Split()
	return goFresh(f.Decl, path+"."+goName)
}

// hostBound is hostCall's form of a Go call that cannot fail and takes
// the task's context (afterGoContext) or has a result goResult converts:
// Go's results into fresh locals, then the task's interruption point,
// then their Ergo values. ok is false for any other call.
func (b *irBuilder) hostBound(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	results := hostResults(f) // ir_tuple.go
	conv := false
	for _, r := range results {
		conv = conv || b.optionResult(r)
	}
	if f.Sig.Fallible || !check.GoContext(f) && !conv {
		return nil, nil, false
	}
	pre, call := b.goTarget(x, c, f, p)
	var ls []int
	switch len(results) {
	case 0:
		pre = append(pre, &irStmt{K: "expr", Pos: p, X: call})
	case 1:
		l := b.temp("r", b.rawGoType(results[0], x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: call})
		ls = []int{l}
	default:
		def := &irStmt{K: "tdef", Pos: p, X: call}
		for _, r := range results {
			def.Ls = append(def.Ls, b.temp("r", b.rawGoType(r, x), p))
		}
		pre, ls = append(pre, def), def.Ls
	}
	if check.GoContext(f) {
		pre = append(pre, b.ctxPoll(x)...)
	}
	vals := make([]*irExpr, len(ls))
	for i, l := range ls {
		var vpre []*irStmt
		vpre, vals[i] = b.hostResult(results[i], l, x, p)
		pre = append(pre, vpre...)
	}
	switch len(vals) {
	case 0:
		return pre, nil, true
	case 1:
		return pre, vals[0], true
	}
	t, ok := b.l.info.Types[x].(*check.Tuple)
	if !ok || len(t.Types) != len(vals) {
		irNo("call of a Go package function")
	}
	return pre, b.tupleLit(t, vals, x, p), true
}

// optionResult reports a Go result that goResult makes an Option: Go's
// pointer, nil for None, where the Option is not the pointer itself.
func (b *irBuilder) optionResult(t check.Type) bool {
	return isOptionType(t) && !b.nicheOption(t)
}

// rawGoType is the Go type of a Go call's result whose Ergo type is t: an
// Option's Go pointer (optionResult), else t's Go type.
func (b *irBuilder) rawGoType(t check.Type, site syntax.Node) string {
	if b.optionResult(t) {
		return b.goType(optionElem(t), site)
	}
	return b.goType(t, site)
}

// hostResult is the Ergo value of the Go result in the local r, of the
// Ergo type t (goResult): an optionResult is None for Go's nil and Some of
// the pointer otherwise; any other result is r itself.
func (b *irBuilder) hostResult(t check.Type, r int, site syntax.Node, p irPos) ([]*irStmt, *irExpr) {
	rv := &irExpr{K: "local", Pos: p, I: r}
	if !b.optionResult(t) {
		return nil, rv
	}
	if _, ref := check.GoRefElem(optionElem(t)); !ref {
		irNo("Go call with a result of another type")
	}
	some, _ := optionVariants(t, site)
	v := b.temp("v", b.goType(t, site), p)
	someV := b.variantInto(some, t, &irExpr{K: "lit", Pos: p, T: b.goType(some, site), Keys: []string{memberName("Value")}, Elems: []*irExpr{rv}}, site) // ir_union.go
	cond := &irExpr{K: "bin", Pos: p, Op: "!=", X: rv, Y: &irExpr{K: "zero", Pos: p, T: b.rawGoType(t, site)}}
	return []*irStmt{
		{K: "def", Pos: p, Local: v, X: b.machineNone(t, p, site)}, // ir_machine.go
		{K: "if", Pos: p, End: p, X: cond, Then: []*irStmt{{K: "set", Pos: p, Local: v, X: someV}}},
	}, &irExpr{K: "local", Pos: p, I: v}
}

// ctxPoll is the interruption point after a Go call that took the task's
// context, in a frame that polls (afterGoContext, fc.poll).
func (b *irBuilder) ctxPoll(x syntax.Node) []*irStmt {
	if !b.fc.frame.polls {
		return nil
	}
	return []*irStmt{b.poll(x)}
}

// hostCallOf is the Go function or method of import go that call calls,
// or nil.
func (b *irBuilder) hostCallOf(call *syntax.CallExpr) *check.Func {
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg == nil || !c.Func.Pkg.Host() || check.GoBuiltin(c.Func) != "" || userExtern(c.Func) {
		return nil
	}
	return c.Func
}

// hostBind is bind's form of a fallible Go call (ir_fail.go): Go's value
// and error into fresh locals, the task's interruption point when the
// call took its context (afterGoContext: a stop that ended Go's wait
// unwinds here, its context.Canceled never a go.Error), then the error as
// a failure (v1.GoErrorOf, or v1.GoUntunnel when a callback of the call
// may have tunnelled an Ergo failure through Go). ok is false for a call
// that is not one.
func (b *irBuilder) hostBind(call *syntax.CallExpr) (pre []*irStmt, v, e int, ok bool) {
	f := b.hostCallOf(call)
	if f == nil || !f.Sig.Fallible {
		return nil, -1, -1, false
	}
	results := hostResults(f)
	if len(results) > 1 {
		irNo("fallible Go call with several results")
	}
	pre, x := b.rawCall(call) // ir_call.go → hostCall → goTarget
	p := b.pos(call, call.Pos())
	e, v = b.temp("err", "error", p), -1
	if len(results) == 0 {
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: e, X: x})
	} else {
		v = b.temp("v", b.rawGoType(results[0], call), p)
		pre = append(pre, &irStmt{K: "def2", Pos: p, Local: v, Val: &e, X: x})
	}
	if check.GoContext(f) {
		pre = append(pre, b.ctxPoll(call)...)
	}
	conv := "GoErrorOf"
	if hostTunnels(f) {
		conv = "GoUntunnel"
	}
	b.keepImport("var _ = " + b.l.rt() + "." + conv)
	errL := &irExpr{K: "local", Pos: p, I: e}
	pre = append(pre, &irStmt{K: "set", Pos: p, Local: e, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: conv, Args: []*irExpr{errL}}})
	if v >= 0 {
		vpre, val := b.hostResult(results[0], v, call, p)
		pre, v = append(pre, vpre...), val.I
	}
	return pre, v, e, true
}

// hostTunnels reports a Go function with a callback parameter that fails:
// its error may carry an Ergo failure through Go (goCallbacks).
func hostTunnels(f *check.Func) bool {
	for _, q := range f.Sig.Params {
		if cb, ok := q.Type.(*check.Signature); ok && cb.Fallible {
			return true
		}
	}
	return false
}

// hostRet is retOf's form of a fallible Go call in a return (ir_fail.go):
// its failure leaves through hostBind's conversion, then the frame returns
// Go's value (none for a call with no value). ok is false for a call that
// is not one.
func (b *irBuilder) hostRet(call *syntax.CallExpr, p irPos) ([]*irStmt, bool) {
	if f := b.hostCallOf(call); f == nil || !f.Sig.Fallible {
		return nil, false
	}
	pre, v := b.try(call)
	if v == nil || b.result == nil {
		return append(pre, &irStmt{K: "ret", Pos: p}), true
	}
	return append(pre, &irStmt{K: "ret", Pos: p, X: v}), true
}

// hostRecvOf is the receiver operand's check: a Go type's value, go.Ref or
// borrow (hostRecv), whose Go method the decoder reaches with no implicit
// address: a pointer, or a value whose method has a value receiver.
func (b *irBuilder) hostRecvOf(f *check.Func, recv syntax.Expr) {
	t := b.l.info.Types[recv]
	if !hostRecv(t) || b.l.holdsValue(t) {
		irNo("Go method call on a receiver of another type")
	}
	if strings.HasPrefix(b.fc.goType(t), "*") {
		return
	}
	if r := f.Decl.Recv; r == nil || len(r.List) != 1 {
		irNo("Go method call on a receiver of another type")
	} else if _, ptr := r.List[0].Type.(*syntax.StarExpr); ptr {
		irNo("Go method call that takes its receiver's address")
	}
}

// goErrorArg is e, an operand of the go.Error parameter type t, as Go's
// error (goErrorArgs): a go.Error's native error, a []go.Error's
// []error (v1.GoErrorsNative, a new slice, so no copy is needed). ok is
// false for another type.
func (b *irBuilder) goErrorArg(t check.Type, e *irExpr, p irPos) (*irExpr, bool) {
	switch t := t.(type) {
	case *check.Named:
		if check.IsGoError(t) {
			return &irExpr{K: "mcall", Pos: p, Name: "Native", X: e}, true
		}
	case *check.Slice:
		if n, ok := t.Elem.(*check.Named); ok && check.IsGoError(n) {
			b.keepImport("var _ = " + b.l.rt() + ".GoErrorsNative")
			return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoErrorsNative", Args: []*irExpr{e}}, true
		}
	}
	return nil, false
}

// basicSlice reports a Go parameter of a slice of basic values ([]string:
// flag's Parse, a variadic strings.NewReplacer): Go's own type, its
// operand under the value rule's boundary copy, which expr applies
// (valueCopy, values.go goArgs) as the emitter's callArgs does.
func basicSlice(t check.Type) bool {
	s, ok := t.(*check.Slice)
	return ok && hostBasic(s.Elem)
}

// isGoErrorArg reports a parameter type goErrorArg converts.
func isGoErrorArg(t check.Type) bool {
	if s, ok := t.(*check.Slice); ok {
		t = s.Elem
	}
	n, ok := t.(*check.Named)
	return ok && check.IsGoError(n)
}

// hostCallback is the adapter of e, an Ergo function value of the
// signature sig, to the Go callback type cb of a Go function that may fail
// (goCallback with fails): a Go function literal of Go's type that runs
// the value in the caller's environment (capturedEnv) and returns its
// failure as Go's error (v1.GoCallbackError: a go.Error Go's own error,
// any other failure tunnelled). Its Go parameters are basic and it
// returns error alone.
func (b *irBuilder) hostCallback(sig *check.Signature, cb *check.Signature, site syntax.Node, e *irExpr, p irPos) ([]*irStmt, *irExpr) {
	if b.env < 0 {
		irNo("Go callback from a pure function")
	}
	if sig == nil || len(cb.Results) != 0 || len(sig.Results) != 0 || len(sig.Params) != len(cb.Params) {
		irNo("Go callback of another shape")
	}
	var pre []*irStmt
	fv := e
	if e.K != "func" {
		// Bound once, where the call is made (bindFunc).
		l := b.temp("fv", b.sigType(sig, site), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: e})
		fv = &irExpr{K: "local", Pos: p, I: l}
	}
	end := p
	lit := &irExpr{K: "func", Pos: p, End: &end}
	call := &irExpr{K: "vcall", Pos: p, X: fv, Args: []*irExpr{{K: "local", Pos: p, I: b.env}}}
	var params []string
	for _, q := range cb.Params {
		if !hostBasic(q.Type) {
			irNo("Go callback of another shape")
		}
		gt := b.goType(q.Type, site)
		l := b.temp("p", gt, p)
		params = append(params, gt)
		lit.Params = append(lit.Params, l)
		call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: l})
	}
	unit, err := b.temp("unit", "struct{}", p), b.temp("err", "error", p)
	b.keepImport("var _ = " + b.l.rt() + ".GoCallbackError")
	lit.Body = []*irStmt{
		{K: "def2", Pos: p, Local: unit, Val: &err, X: call},
		{K: "ret", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoCallbackError", Args: []*irExpr{{K: "local", Pos: p, I: err}}}},
	}
	lit.T = "func(" + strings.Join(params, ", ") + ") error"
	return pre, lit
}

// goErrorLit is go.Error{Text: s}, a native error of its own
// (v1.GoErrorText), or go.Error{}, the zero go.Error (Go's nil), as the
// emitter's composite writes them (IG5).
func (b *irBuilder) goErrorLit(x *syntax.CompositeLit, t check.Type, p irPos) *irExpr {
	gt := b.goType(t, x)
	switch len(x.Elts) {
	case 0:
		return &irExpr{K: "zero", Pos: p, T: gt}
	case 1:
		v := x.Elts[0]
		if kv, ok := v.(*syntax.KeyValueExpr); ok {
			v = kv.Value
		}
		b.keepImport("var _ = " + b.l.rt() + ".GoErrorText")
		return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoErrorText", Args: []*irExpr{b.expr(v)}}
	}
	irNo("go.Error literal form")
	return nil
}

// goErrorText is e.Text, a go.Error's field read through td, the
// declaration goErrorDecl writes: the method its fieldName names, which
// computes the text from the Go error it keeps (IG5: read when read,
// never at the failure).
func (b *irBuilder) goErrorText(x *syntax.SelectorExpr, td *typeDecl, p irPos) *irExpr {
	m, ok := strings.CutSuffix(td.fieldName[x.Sel.Name], "()")
	if !ok {
		irNo("selector that is not a field")
	}
	return &irExpr{K: "mcall", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: m, X: b.expr(x.X)}
}

// monoName is the Go name of the monomorphic copy of f whose body b builds
// (mono.go monoFor: emitBody writes its head as sp.name), or "" for f's
// own body. A copy's funcCtx takes the copy's type arguments, the same
// map (fc.params = sp.typeArgs), which names the copy. Without it the
// copy's IR body took f's name, a second body for f (FORK-GOLIB: snake's
// machines.Next and NextMono_i1).
func (b *irBuilder) monoName(f *check.Func) string {
	if !b.fc.mono || b.fc.params == nil {
		return ""
	}
	at := reflect.ValueOf(b.fc.params).Pointer()
	for key, sp := range b.l.monos {
		if key.f == f && sp.typeArgs != nil && reflect.ValueOf(sp.typeArgs).Pointer() == at {
			return sp.name
		}
	}
	return ""
}

// nicheOption reports t, an Option of a go.Ref whose Go value is the
// pointer itself (flatunion.go, the niche): Go's pointer result passes as
// it is, nil for None.
func (b *irBuilder) nicheOption(t check.Type) bool {
	fu := b.flatOf(t)
	return fu != nil && fu.niche
}

// flatTag is the tag of the flat union fu in the local s (tagExpr): its
// tag field, or a niche Option's nicheTag.
func (b *irBuilder) flatTag(fu *flatUnion, s int, p irPos, site syntax.Node) *irExpr {
	if fu.niche {
		return b.nicheTag(fu, s, p, site)
	}
	return b.tagOf(s, p)
}

// nicheTag is the tag of the niche Option in the local s (tagExpr): 1
// (None) for Go's nil pointer, 0 (Some) for any other, by the emitter's
// generic helper, which Go inlines.
func (b *irBuilder) nicheTag(fu *flatUnion, s int, p irPos, site syntax.Node) *irExpr {
	name := b.l.nilTagHelper()
	return &irExpr{K: "call", Pos: p, Name: name, T: b.goType(fu.inst, site), Args: []*irExpr{{K: "local", Pos: p, I: s}}}
}

// nicheNarrow is variant k of the niche Option in the local s
// (flatNarrow): Some{Value: s}, or None's zero record.
func (b *irBuilder) nicheNarrow(fu *flatUnion, s, k int, p irPos, site syntax.Node) *irExpr {
	gv := b.goType(fu.variants[k], site)
	if !fu.slot[k] {
		return &irExpr{K: "zero", Pos: p, T: gv}
	}
	return &irExpr{K: "lit", Pos: p, T: gv, Keys: []string{memberName("Value")}, Elems: []*irExpr{{K: "local", Pos: p, I: s}}}
}

// nicheValue is e, a value of variant k, put into the niche Option of Go
// type gt (flatValue): Some's pointer, or nil for None.
func (b *irBuilder) nicheValue(fu *flatUnion, gt string, k int, e *irExpr) *irExpr {
	if !fu.slot[k] {
		return &irExpr{K: "zero", Pos: e.Pos, T: gt}
	}
	return &irExpr{K: "field", Pos: e.Pos, Name: memberName("Value"), X: e}
}
