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
// hostBind, which converts its error; hostResult converts a Go result (a
// go.Ref checked for nil, an Option, a slice or Map copied).
// hostWriteParam returns the index into hostArgs' args slice of the slice
// parameter written in place by a // ergo:writes Go call, or (-1, false).
func hostWriteParam(c *check.Call, f *check.Func) (int, bool) {
	if c == nil || f == nil || f.Decl == nil || (c.Recv == nil) != (f.Decl.Recv == nil) {
		return -1, false
	}
	i, ok := check.GoWrites(f)
	if !ok {
		return -1, false
	}
	if c.Recv != nil && !c.RecvIsParam() {
		i--
	}
	if i < 0 || i >= len(f.Sig.Params) {
		return -1, false
	}
	return i, true
}

func (b *irBuilder) goTarget(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr) {
	b.goWritten = -1
	argc := len(x.Args)
	if c.RecvIsParam() {
		argc++
	}
	switch {
	case f.Decl == nil || f.Decl.Extern == nil || f.Decl.Extern.Target == nil, hostGeneric(f), // ir_gocall.go
		argc != len(f.Sig.Params):
		irNo("call of a Go package function")
	case f.Sig.Fallible && !failsHere(c):
		irNo("Go call whose site drops its failure")
	}
	ctx := check.GoContext(f)
	if ctx && b.env < 0 {
		irNo("Go context call from a pure function")
	}
	for _, r := range hostResults(f) { // ir_tuple.go
		if !b.hostValue(r) && !isOptionType(r) && !b.l.holdsValue(r) && !isGoRef(r) && !check.IsGoAny(r) {
			irNo("Go call with a result of another type")
		}
	}
	pre, recv, args := b.hostArgs(x, c, f, p) // ir_gocall.go
	if wi, ok := hostWriteParam(c, f); ok && wi < len(args) {
		w := b.temp("w", b.goType(f.Sig.Params[wi].Type, x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: w, X: args[wi]})
		args[wi] = &irExpr{K: "local", Pos: p, I: w}
		b.goWritten = w
	}
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

// hostBound is hostCall's form of a Go call that cannot fail and takes
// the task's context (afterGoContext) or has a result goResult converts:
// Go's results into fresh locals, then the task's interruption point,
// then their Ergo values. ok is false for any other call.
func (b *irBuilder) hostBound(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	results := hostResults(f) // ir_tuple.go
	_, writes := hostWriteParam(c, f)
	conv := false
	for _, r := range results {
		conv = conv || b.optionResult(r) || isGoRef(r) || b.l.holdsValue(r)
	}
	if f.Sig.Fallible || !check.GoContext(f) && !conv && !writes {
		return nil, nil, false
	}
	goResults := results
	if writes && len(goResults) > 0 {
		goResults = goResults[1:]
	}
	pre, call := b.goTarget(x, c, f, p)
	written := b.goWritten
	b.goWritten = -1
	var ls []int
	switch len(goResults) {
	case 0:
		pre = append(pre, &irStmt{K: "expr", Pos: p, X: call})
	case 1:
		l := b.temp("r", b.rawGoType(goResults[0], x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: call})
		ls = []int{l}
	default:
		def := &irStmt{K: "tdef", Pos: p, X: call}
		for _, r := range goResults {
			def.Ls = append(def.Ls, b.temp("r", b.rawGoType(r, x), p))
		}
		pre, ls = append(pre, def), def.Ls
	}
	if check.GoContext(f) {
		pre = append(pre, b.ctxPoll(x)...)
	}
	vals := make([]*irExpr, 0, len(results))
	if writes && written >= 0 {
		vals = append(vals, &irExpr{K: "local", Pos: p, I: written})
	}
	for i, l := range ls {
		var vpre []*irStmt
		var val *irExpr
		vpre, val = b.hostResult(hostTargetName(f), goResults[i], l, -1, x, p)
		pre = append(pre, vpre...)
		vals = append(vals, val)
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
// the pointer otherwise; a go.Ref is r, after the defect Go's nil raises
// when the call did not fail (goNilCheck); any other result is r itself.
// errL is the local of the call's error, -1 for a call that cannot fail.
func (b *irBuilder) hostResult(name string, t check.Type, r, errL int, site syntax.Node, p irPos) ([]*irStmt, *irExpr) {
	rv := &irExpr{K: "local", Pos: p, I: r}
	if isGoRef(t) {
		return []*irStmt{b.goNilCheck(name, t, r, errL, site, p)}, rv
	}
	if !b.optionResult(t) {
		if b.l.holdsValue(t) {
			return nil, b.goResultCopy(name, t, rv, site)
		}
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

// goBuiltin is the call x of go.New, go.Zero, go.Is or go.As (c.Kind
// CallGoError), as the emitter's goErrorCall writes it (IR-GOCALLS):
//   - go.New[T]() is Go's new(T), the address of a fresh zero T, and
//     go.New(v) the address of a fresh copy of v (GBX1, GBX5b);
//   - go.Zero[T]() is Go's zero T;
//   - go.Is(e, target) is v1.GoIs;
//   - go.As[T](a) on a go.Any is Go's type assertion, Some when a holds
//     a T: a type switch with T's one case; on a go.Error it is
//     v1.GoAs[T]'s (T, bool), Some when ok.
//
// None of them has an effect.
func (b *irBuilder) goBuiltin(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr, bool) {
	switch c.Name {
	case "go.New":
		elem, ok := check.GoRefElem(b.l.info.Types[x])
		if !ok || len(x.Args) > 1 {
			irNo("go.New form")
		}
		et := b.goType(elem, x)
		var pre []*irStmt
		init := &irExpr{K: "zero", Pos: p, T: et}
		if len(x.Args) == 1 {
			pre, init = b.value(x.Args[0])
			init = b.assignedTo(x.Args[0], init, elem) // ir_union.go
		}
		n := b.temp("n", et, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: n, X: init})
		return pre, &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: n}}, false
	case "go.Zero":
		if c.Type == nil {
			irNo("go.Zero form")
		}
		return nil, &irExpr{K: "zero", Pos: p, T: b.goType(c.Type, x)}, false
	case "go.Is":
		pre, args := b.operands(x.Args) // ir_fail.go
		b.keepImport("var _ = " + b.l.rt() + ".GoIs")
		return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoIs", Args: args}, false
	case "go.As":
	default:
		irNo("go builtin %s", c.Name)
	}
	if len(x.Args) != 1 || c.Type == nil {
		irNo("go.As form")
	}
	t := b.l.info.Types[x] // Option[T]
	some, _ := optionVariants(t, x)
	gt := b.goType(c.Type, x)
	pre, arg := b.value(x.Args[0])
	res := b.temp("as", b.goType(t, x), p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: res, X: b.machineNone(t, p, x)}) // ir_machine.go
	someOf := func(v *irExpr) *irStmt {
		lit := &irExpr{K: "lit", Pos: p, T: b.goType(some, x), Keys: []string{memberName("Value")}, Elems: []*irExpr{v}}
		return &irStmt{K: "set", Pos: p, Local: res, X: b.variantInto(some, t, lit, x)} // ir_union.go
	}
	if check.IsGoAny(b.l.info.Types[x.Args[0]]) {
		a := b.temp("a", "any", p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: a, X: arg})
		local := &irExpr{K: "local", Pos: p, I: a}
		sw := &irStmt{K: "tswitch", Pos: p, End: p, X: local, Cases: []*irCase{{Pos: p, Types: []string{gt}, Body: []*irStmt{
			someOf(&irExpr{K: "assert", Pos: p, T: gt, X: local}),
		}}}}
		return append(pre, sw), &irExpr{K: "local", Pos: p, I: res}, false
	}
	b.keepImport("var _ = " + b.l.rt() + ".GoAs[int]")
	v, ok := b.temp("v", gt, p), b.temp("ok", "bool", p)
	call := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "GoAs", V: gt, Args: []*irExpr{arg}}
	pre = append(pre,
		&irStmt{K: "tdef", Pos: p, X: call, Ls: []int{v, ok}},
		&irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "local", Pos: p, I: ok}, Then: []*irStmt{someOf(&irExpr{K: "local", Pos: p, I: v})}})
	return pre, &irExpr{K: "local", Pos: p, I: res}, false
}

// hostTargetName is the Go target of f as goSig names it (gocall.go):
// PATH.NAME of a package function, PATH.TYPE.NAME of a method.
func hostTargetName(f *check.Func) string {
	switch {
	case f.Decl == nil || f.Decl.Extern == nil || f.Decl.Extern.Target == nil:
		irNo("Go call with a result of another type")
	case f.Decl.Recv != nil:
		path, typ, m := f.Decl.Extern.SplitMethod()
		return path + "." + typ + "." + m
	}
	path, goName := f.Decl.Extern.Split()
	return path + "." + goName
}

// goResultCopy is rv, a Go result of t that holds a slice or a Map, as
// goResult gives it (IR-GOCALLS-2): the value rule's deep copy, since Go
// may still write it (V8), unless Go made it new (a validated fresh row,
// goFresh: strings.Split) or only Go takes it (goShare, H4 and H5).
func (b *irBuilder) goResultCopy(name string, t check.Type, rv *irExpr, site syntax.Node) *irExpr {
	if !valueRule() || goFresh(site, name) {
		return rv
	}
	if e, ok := site.(syntax.Expr); ok && b.l.valueFacts().goShare[e] {
		return rv
	}
	return b.copyDeep(t, site, rv, true) // ir_copy.go
}

// goNilCheck is goResult's guard on a go.Ref result r of the Go call of
// f, which is never nil (docs/spec/interop.md, go.Ref): when Go returned
// nil and no error (errL, -1 for a call that cannot fail), the call is a
// defect naming the Go target and the Ergo site, v1.GoNilDefect.
func (b *irBuilder) goNilCheck(name string, t check.Type, r, errL int, site syntax.Node, p irPos) *irStmt {
	var cond *irExpr = &irExpr{K: "bin", Pos: p, Op: "==", X: &irExpr{K: "local", Pos: p, I: r}, Y: &irExpr{K: "zero", Pos: p, T: b.goType(t, site)}}
	if errL >= 0 {
		noErr := &irExpr{K: "bin", Pos: p, Op: "==", X: &irExpr{K: "local", Pos: p, I: errL}, Y: &irExpr{K: "zero", Pos: p, T: "error"}}
		cond = &irExpr{K: "bin", Pos: p, Op: "&&", X: noErr, Y: cond}
	}
	b.keepImport("var _ " + b.l.rt() + ".GoNilDefect")
	defect := &irExpr{K: "addr", Pos: p, X: &irExpr{K: "lit", Pos: p, T: b.spelling(b.l.rt() + ".GoNilDefect"), Keys: []string{"Func", "At"}, Elems: []*irExpr{
		irStringConst(p, "string", name),
		irStringConst(p, "string", b.l.siteText(site)),
	}}}
	return &irStmt{K: "if", Pos: p, End: p, X: cond, Then: []*irStmt{{K: "panic", Pos: p, X: defect}}}
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
	if c == nil || c.Kind != check.CallFunc || !c.Func.CallsGo() || check.GoExternOf(c.Func) != nil || check.GoBuiltin(c.Func) != "" {
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
	_, writes := hostWriteParam(b.l.info.Calls[call], f)
	goResults := results
	if writes && len(goResults) > 0 {
		goResults = goResults[1:]
	}
	var tuple *check.Tuple
	if len(results) > 1 {
		// (T, U, error): the value is the tuple of Go's results
		// (IR-GOCALLS; the emitter's goCall binds them all, then the
		// error).
		t, ok := b.l.info.Types[call].(*check.Tuple)
		if !ok || len(t.Types) != len(results) {
			irNo("fallible Go call with several results")
		}
		tuple = t
	}
	pre, x := b.rawCall(call) // ir_call.go → hostCall → goTarget
	written := b.goWritten
	b.goWritten = -1
	p := b.pos(call, call.Pos())
	e, v = b.temp("err", "error", p), -1
	var rs []int
	switch {
	case len(goResults) == 0:
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: e, X: x})
	case len(goResults) == 1:
		r := b.temp("r", b.rawGoType(goResults[0], call), p)
		rs = []int{r}
		pre = append(pre, &irStmt{K: "def2", Pos: p, Local: r, Val: &e, X: x})
	default:
		def := &irStmt{K: "tdef", Pos: p, X: x}
		for _, r := range goResults {
			l := b.temp("r", b.rawGoType(r, call), p)
			rs = append(rs, l)
			def.Ls = append(def.Ls, l)
		}
		def.Ls = append(def.Ls, e)
		pre = append(pre, def)
	}
	if check.GoContext(f) {
		pre = append(pre, b.ctxPoll(call)...)
	}
	if b.rawErr != nil && b.rawErr == call && !hostTunnels(f) {
		// A plain catch tests Go's error against nil alone (ir_fail.go
		// catch, GOERR-BOX): it takes the error unconverted. I-FLIP: the
		// GOLIB form of bind's rawErr branch.
		b.rawErr = nil
	} else {
		conv := "GoErrorOf"
		if hostTunnels(f) {
			conv = "GoUntunnel"
		}
		b.keepImport("var _ = " + b.l.rt() + "." + conv)
		errL := &irExpr{K: "local", Pos: p, I: e}
		pre = append(pre, &irStmt{K: "set", Pos: p, Local: e, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: conv, Args: []*irExpr{errL}}})
	}
	vals := make([]*irExpr, 0, len(results))
	if writes && written >= 0 {
		vals = append(vals, &irExpr{K: "local", Pos: p, I: written})
	}
	for i, r := range rs {
		vpre, val := b.hostResult(hostTargetName(f), goResults[i], r, e, call, p)
		pre = append(pre, vpre...)
		vals = append(vals, val)
	}
	switch {
	case len(vals) == 0:
		v = -1
	case len(vals) == 1:
		if vals[0].K == "local" {
			v = vals[0].I
		} else {
			// A copied result (goResultCopy: a slice of Go records,
			// hostfs.ReadDir's []Entry) is an expression; bind it, so the
			// caller reads a local, never local 0 (the env).
			c := b.temp("v", b.rawGoType(results[0], call), p)
			pre = append(pre, &irStmt{K: "def", Pos: p, Local: c, X: vals[0]})
			v = c
		}
	default:
		v = b.temp("v", b.goType(tuple, call), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: b.tupleLit(tuple, vals, call, p)}) // ir_tuple.go
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
	// A receiver that holds a slice or Map (url.Values, http.Header) is
	// an operand like any other: the value rule's copy, when it copies
	// it, is operands' (valueCopy), as callArgs gives the emitter's
	// (IR-GOCALLS-3).
	if !hostRecv(t) {
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

// goAnyArg reports a Go parameter of go.Any or a slice of go.Any (a
// variadic `...any`: db.Exec's args, rows.Scan's dest): Go's own any, its
// operand converted to it (variantInto, ir_union.go), a slice literal's
// elements each (IR-GOCALLS).
func goAnyArg(t check.Type) bool {
	if s, ok := t.(*check.Slice); ok {
		t = s.Elem
	}
	return check.IsGoAny(t)
}

// goPointerArg reports a Go parameter of Go's pointer to a Go type (`own
// *Cookie`, Request.AddCookie's) or a go.Ref of one: its operand, a
// go.Ref, is Go's *T as is, as the emitter's callArgs passes it
// (IR-GOCALLS-4).
func goPointerArg(t check.Type) bool {
	if isGoRef(t) {
		return true
	}
	p, ok := t.(*check.Pointer)
	return ok && hostRecv(p)
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
		if key.decl == monoDecl(monoFunc{f}) && sp.typeArgs != nil && reflect.ValueOf(sp.typeArgs).Pointer() == at {
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
