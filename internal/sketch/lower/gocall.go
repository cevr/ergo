package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Calls of import go (docs/spec/interop.md, Lowering; #202 Level 1). A call
// of a Go function, method or constant of a binding is a direct Go call of
// it, with Go's import (aliased by the naming pass when its name clashes,
// hostImportNames), no environment, no wrapper and no poll: a host call is
// not an interruption point (runtime.md). At the boundary:
//
//   - an argument that holds a Map or slice is copied unless it is fresh or
//     a local dead after the call (the value rule, q26; values.go owns the
//     decision, goArgs): Go may write or keep what it is given;
//     SetHostCopyAlwaysForTest copies every one, the reference lowering;
//   - the last argument of a variadic Go function is spread (xs...);
//   - a result that holds a Map or slice is copied unless the reviewed
//     table freshGoResults says Go made it new (strings.Split);
//   - a nullable Go result (a pointer, an interface) whose Ergo type is
//     Option[T] is None when nil; a go.Ref[S] result that is nil with a nil
//     error is a defect naming the call (v1.GoNilDefect);
//   - several Go results are a tuple; a last error is fails go.Error, as G4
//     lowers a user extern (v1.GoErrorOf);
//   - a Go panic unwinds as any defect; the call's //line directive
//     (identify) names the Ergo call site in the report.
//
// Prior art: Gleam's @external and ReScript's @module externals are direct
// calls with no adapter, and so is this one when no value crosses as a
// shared reference; Kotlin/Native's cinterop and Swift's Clang importer
// map a nullable pointer to an optional the same way.

// hostCopyAlways selects the reference lowering: every Map and slice
// argument of a Go call is copied (TestHostCopyElisionAgrees).
var hostCopyAlways bool

// SetHostCopyAlwaysForTest selects the copy-always lowering of Go call
// arguments (true) or the value rule's elision (false), and returns a
// function that restores the previous choice.
func SetHostCopyAlwaysForTest(always bool) func() {
	old := hostCopyAlways
	hostCopyAlways = always
	return func() { hostCopyAlways = old }
}

// freshGoResults are the Go functions of the standard library whose slice
// or Map result is new on every call, reviewed against their source at
// go1.27: the result shares no memory with an argument or with state Go
// keeps, so it needs no copy.
var freshGoResults = map[string]bool{
	"strings.Split": true, "strings.SplitN": true, "strings.SplitAfter": true, "strings.Fields": true,
	"strings.FieldsFunc": true, "bytes.Split": false,
	"regexp.Regexp.FindAllString": true, "regexp.Regexp.FindStringSubmatch": true,
	"regexp.Regexp.FindAllStringSubmatch": true, "regexp.Regexp.Split": true,
	"regexp.Regexp.SubexpNames": false,
}

// goImportCall reports a call of a Go function or method of import go;
// typeOf types its receiver.
func goImportCall(c *check.Call, typeOf func(syntax.Expr) check.Type) bool {
	if c == nil {
		return false
	}
	switch c.Kind {
	case check.CallFunc:
		return c.Func != nil && c.Func.Pkg.Host()
	case check.CallOp:
		return c.Recv != nil && c.Op != nil && hostRecv(typeOf(c.Recv))
	}
	return false
}

// hostPkgsIn appends to out the Go packages (import go) of the types that t
// holds through type arguments, elements and tuples.
func hostPkgsIn(t check.Type, out []*check.Package) []*check.Package {
	switch t := t.(type) {
	case *check.Named:
		if t.Obj.Pkg.Host() {
			out = append(out, t.Obj.Pkg)
		}
		for _, a := range t.TArgs {
			out = hostPkgsIn(a, out)
		}
	case *check.Slice:
		out = hostPkgsIn(t.Elem, out)
	case *check.Map:
		out = hostPkgsIn(t.Key, out)
		out = hostPkgsIn(t.Elem, out)
	case *check.Pointer:
		out = hostPkgsIn(t.Elem, out)
	case *check.Tuple:
		for _, e := range t.Types {
			out = hostPkgsIn(e, out)
		}
	}
	return out
}

// goShared reports an operand type whose value Go could write or keep: a
// Map or a slice (a Go type over one included, url.Values).
func goShared(t check.Type) bool {
	switch underEqual(t).(type) {
	case *check.Map, *check.Slice:
		return true
	}
	return false
}

// hostRecv reports a receiver of a Go type: a value, a go.Ref or a borrow.
func hostRecv(t check.Type) bool {
	if e, ok := check.GoRefElem(t); ok {
		t = e
	}
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*check.Named)
	return ok && n.Obj.Pkg.Host()
}

// goSig is the signature of the Go call c, its Go text's head (pkg.F, or
// .M after the receiver, which goCall puts first) and the reviewed name of the
// target ("strings.Split", "regexp.Regexp.Split").
func (fc *funcCtx) goSig(c *check.Call, site syntax.Node) (sig *check.Signature, head, name string, variadic bool) {
	if c.Kind == check.CallOp {
		n := hostNamed(fc.typeOf(c.Recv))
		return c.Op.Sig, "." + c.Op.Name, n.Obj.Pkg.Path + "." + n.Obj.Name + "." + c.Op.Name, false
	}
	f := c.Func
	if c.Recv != nil {
		path, typ, m := f.Decl.Extern.SplitMethod()
		return f.Sig, "." + m, path + "." + typ + "." + m, check.GoVariadic(f)
	}
	path, goName := f.Decl.Extern.Split()
	return f.Sig, fc.l.hostPkg(path, site) + "." + goName, path + "." + goName, check.GoVariadic(f)
}

// hostNamed is the Go type a host receiver's methods belong to.
func hostNamed(t check.Type) *check.Named {
	if e, ok := check.GoRefElem(t); ok {
		t = e
	}
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	return t.(*check.Named)
}

// goCall lowers the call x of import go, whose operands are args (no
// trailing block), and returns its value and failure.
func (fc *funcCtx) goCall(x *syntax.CallExpr, c *check.Call) operand {
	sig, head, name, variadic := fc.goSig(c, x)
	list := x.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	vals := fc.callArgs(list, x)
	if c.Recv != nil {
		head, vals = vals[0]+head, vals[1:]
	}
	fc.goErrorArgs(sig, vals)
	tunnels := fc.goCallbacks(sig, x.Args, vals, x)
	if variadic && len(vals) > 0 {
		vals[len(vals)-1] += "..."
	}
	ctx := c.Kind != check.CallOp && check.GoContext(c.Func)
	if ctx {
		vals = append([]string{fc.goContext(x)}, vals...)
	}
	text := head + "(" + strings.Join(vals, ", ") + ")"
	defer fc.identify(x, c)()
	var goResults []check.Type
	if len(sig.Results) == 1 {
		if t, ok := sig.Results[0].(*check.Tuple); ok {
			goResults = t.Types
		} else {
			goResults = sig.Results
		}
	}
	names := make([]string, len(goResults))
	for i := range names {
		names[i] = fc.temp("r")
	}
	e := ""
	lhs := names
	if sig.Fallible {
		e = fc.temp("err")
		lhs = append(append([]string{}, names...), e)
	}
	if len(lhs) == 0 {
		fc.line(text)
		fc.afterGoContext(x, ctx)
		return operand{}
	}
	fc.line(strings.Join(lhs, ", ") + " := " + text)
	fc.afterGoContext(x, ctx)
	switch {
	case e != "" && tunnels:
		fc.goUntunnel(x, e)
	case e != "":
		fc.goErrorOf(x, e)
	}
	vs := make([]string, len(goResults))
	for i, t := range goResults {
		vs[i] = fc.goResult(x, t, names[i], e, name)
	}
	if len(goResults) == 0 {
		return operand{err: e}
	}
	v := vs[0]
	if len(goResults) > 1 {
		parts := make([]string, len(vs))
		for i, s := range vs {
			parts[i] = tupleField(i) + ": " + s
		}
		v = fc.goType(sig.Results[0]) + "{" + strings.Join(parts, ", ") + "}"
	}
	return operand{val: v, err: e}
}

// goErrorArgs makes each go.Error argument of a Go call, vals in sig's
// parameter order, the native error it keeps (IG5): a Go `error`
// parameter gets Go's own error, so errors.Join, fmt and errors.Is in Go
// see the chain the Go call made; the zero go.Error is nil. A []go.Error
// (Go's ...error) is converted element by element.
func (fc *funcCtx) goErrorArgs(sig *check.Signature, vals []string) {
	for i, p := range sig.Params {
		if i >= len(vals) {
			return
		}
		switch t := p.Type.(type) {
		case *check.Named:
			if check.IsGoError(t) {
				vals[i] = "(" + vals[i] + ").Native()"
			}
		case *check.Slice:
			if n, ok := t.Elem.(*check.Named); ok && check.IsGoError(n) {
				vals[i] = fc.l.rt() + ".GoErrorsNative(" + vals[i] + ")"
			}
		}
	}
}

// goResult is the Ergo value of the Go result r of type t (the Ergo type
// the binding gives it), whose call's error is e ("" when it has none).
func (fc *funcCtx) goResult(x syntax.Node, t check.Type, r, e, name string) string {
	if n, ok := t.(*check.Named); ok && check.IsGoError(n) {
		// A Go `error` value that is not the last result (a field, a
		// tuple element): the go.Error that keeps it (IG5).
		return fc.l.goErrorFrom() + "(" + r + ")"
	}
	switch {
	case isOptionType(t):
		if fu := fc.l.flatUnionOf(t); fu != nil && fu.niche {
			// Go's pointer is the Option: nil is None (flatunion.go).
			fc.l.stats.NicheOptions++
			return r
		}
		v := fc.temp("v")
		fc.line("var " + v + " " + fc.goType(t) + " = " + fc.optionNone(t, x))
		fc.line("if " + r + " != nil {")
		fc.line("\t" + v + " = " + fc.optionSome(t, r, x))
		fc.line("}")
		return v
	case isGoRef(t):
		cond := r + " == nil"
		if e != "" {
			cond = e + " == nil && " + cond
		}
		fc.line("if " + cond + " {")
		fc.line("\tpanic(&" + fc.l.rt() + ".GoNilDefect{Func: " + strconv.Quote(name) + ", At: " + strconv.Quote(fc.l.siteText(x)) + "})")
		fc.line("}")
		return r
	case fc.l.holdsValue(t) && !freshGoResults[name] && valueRule():
		return fc.cloneValue(t, r, x)
	}
	return r
}

// goExternCall reports a call of a user extern that the checker admitted
// as a Level 2 adapter outside the C26 domain (check.GoExtern): it lowers
// by the boundary rules of import go (goExtern), not by hostCall.
func goExternCall(c *check.Call) bool {
	if c == nil || !hostCalled(c) {
		return false
	}
	ge := check.GoExternOf(c.Func)
	return ge != nil && !ge.Basic
}

// goExtern lowers the call x of a user extern that adapts its Go target's
// Level 1 signature (docs/spec/interop.md, Level 2 adapters): the direct Go
// call as goCall writes it (the copy rule on Map and slice arguments, the
// variadic spread, Go's results bound), then the adapter:
//
//   - AdaptSame: goCall's results;
//   - AdaptNonNil: a nil result is a defect naming the target and the Ergo
//     call (v1.GoNilDefect), the check hand Go writes;
//   - AdaptOption: Go's (v, ok) is Some(v) when ok, else None;
//   - AdaptPartial: Go's values are kept, and its error is the last result,
//     None when nil, else Some(go.Error).
func (fc *funcCtx) goExtern(x *syntax.CallExpr, c *check.Call) operand {
	f, ge := c.Func, check.GoExternOf(c.Func)
	if len(x.Args) != len(f.Sig.Params) || x.Trailing != nil {
		ice(x, "a call of the user extern %s outside its checked domain", f.Name)
	}
	vals := fc.callArgs(x.Args, x)
	for i, p := range f.Sig.Params {
		if cb, ok := p.Type.(*check.Signature); ok {
			vals[i] = fc.spill(fc.hostCallback(fc.bindFunc(vals[i]), cb, x))
		}
	}
	fc.goErrorArgs(f.Sig, vals)
	if ge.Variadic && len(vals) > 0 {
		vals[len(vals)-1] += "..."
	}
	if ge.Context {
		vals = append([]string{fc.goContext(x)}, vals...)
	}
	text := fc.hostTarget(x, f) + "(" + strings.Join(vals, ", ") + ")"
	defer fc.identify(x, c)()
	path, goName := f.Decl.Extern.Split()
	name := path + "." + goName
	written := f.Sig.Results
	if len(written) == 1 {
		if t, ok := written[0].(*check.Tuple); ok {
			written = t.Types
		}
	}
	goTypes, goFails := written, f.Sig.Fallible
	switch ge.Adapter {
	case check.AdaptOption:
		goTypes = []check.Type{optionElem(written[0]), check.Typ[check.Bool]}
	case check.AdaptPartial:
		goTypes, goFails = written[:len(written)-1], true
	}
	names := make([]string, len(goTypes))
	for i := range names {
		names[i] = fc.temp("r")
	}
	lhs, e := names, ""
	if goFails {
		e = fc.temp("err")
		lhs = append(append([]string{}, names...), e)
	}
	if len(lhs) == 0 {
		fc.line(text)
		fc.afterGoContext(x, ge.Context)
		return operand{}
	}
	fc.line(strings.Join(lhs, ", ") + " := " + text)
	fc.afterGoContext(x, ge.Context)
	if e != "" {
		fc.goErrorOf(x, e)
	}
	switch ge.Adapter {
	case check.AdaptNonNil:
		cond := names[0] + " == nil"
		if e != "" {
			cond = e + " == nil && " + cond
		}
		fc.line("if " + cond + " {")
		fc.line("\tpanic(&" + fc.l.rt() + ".GoNilDefect{Func: " + strconv.Quote(name) + ", At: " + strconv.Quote(fc.l.siteText(x)) + "})")
		fc.line("}")
		return operand{val: names[0], err: e}
	case check.AdaptOption:
		return operand{val: fc.someWhen(x, written[0], names[1], fc.goResult(x, goTypes[0], names[0], e, name)), err: e}
	}
	vs := make([]string, len(goTypes))
	for i, t := range goTypes {
		vs[i] = fc.goResult(x, t, names[i], e, name)
	}
	if ge.Adapter == check.AdaptPartial {
		last := written[len(written)-1]
		vs = append(vs, fc.someWhen(x, last, e+" != nil", e+".("+fc.goType(optionElem(last))+")"))
		e = ""
	}
	switch len(vs) {
	case 0:
		return operand{err: e}
	case 1:
		return operand{val: vs[0], err: e}
	}
	parts := make([]string, len(vs))
	for i, s := range vs {
		parts[i] = tupleField(i) + ": " + s
	}
	return operand{val: fc.goType(f.Sig.Results[0]) + "{" + strings.Join(parts, ", ") + "}", err: e}
}

// goErrorOf writes the conversion of the Go error e of the call x to
// go.Error (v1.GoErrorOf), unless x is the call a plain catch takes with
// its error unread (fc.rawErr, plainCatch): that catch tests e against nil
// alone, which the conversion keeps, so hand Go's `if err != nil` is all
// it writes.
func (fc *funcCtx) goErrorOf(x syntax.Expr, e string) {
	if fc.rawErr != nil && fc.rawErr == x {
		fc.rawErr = nil
		return
	}
	fc.line(e + " = " + fc.l.goErrorOf() + "(" + e + ")")
}

// goErrorCall lowers go.Is(e, target), errors.Is on the native errors
// (v1.GoIs), and go.As[T](e), errors.As into a T (v1.GoAs), Some when Go
// finds one (IG5; check/goerror.go).
func (fc *funcCtx) goErrorCall(x *syntax.CallExpr, c *check.Call) operand {
	args := fc.operands(x.Args, true)
	if c.Name == "go.Is" {
		return operand{val: fc.l.rt() + ".GoIs(" + args[0] + ", " + args[1] + ")"}
	}
	v, ok := fc.temp("v"), fc.temp("ok")
	fc.line(v + ", " + ok + " := " + fc.l.rt() + ".GoAs[" + fc.goType(c.Type) + "](" + args[0] + ")")
	return operand{val: fc.someWhen(x, fc.typeOf(x), ok, v)}
}

// goErrorEager selects the reference lowering of go.Error (G4's eager
// form): the text is computed at the failure (v1.GoErrorEagerOf), where
// the default keeps the native error and computes Text when it is read
// (IG5; TestGoErrorChainsAgree runs both).
var goErrorEager bool

// SetGoErrorEagerForTest selects the eager go.Error (true) or the lazy one
// (false), and returns a function that restores the previous choice.
func SetGoErrorEagerForTest(eager bool) func() {
	old := goErrorEager
	goErrorEager = eager
	return func() { goErrorEager = old }
}

// goErrorOf is the runtime function that makes a Go call's error its
// failure, go.Error or nil.
func (l *lowerer) goErrorOf() string {
	if goErrorEager {
		return l.rt() + ".GoErrorEagerOf"
	}
	return l.rt() + ".GoErrorOf"
}

// goErrorFrom is the runtime function that makes a Go error variable (a
// binding's sentinel, io.EOF) a go.Error value.
func (l *lowerer) goErrorFrom() string {
	if goErrorEager {
		return l.rt() + ".GoErrorEagerFrom"
	}
	return l.rt() + ".GoErrorFrom"
}

// someWhen is a new variable of the Option type t: Some(v) when cond holds,
// else None.
func (fc *funcCtx) someWhen(x syntax.Node, t check.Type, cond, v string) string {
	o := fc.temp("o")
	fc.line("var " + o + " " + fc.goType(t) + " = " + fc.optionNone(t, x))
	fc.line("if " + cond + " {")
	fc.line("\t" + o + " = " + fc.optionSome(t, v, x))
	fc.line("}")
	return o
}

// optionElem is T for Option[T].
func optionElem(t check.Type) check.Type {
	return t.(*check.Named).TArgs[0]
}

func isOptionType(t check.Type) bool {
	n, ok := t.(*check.Named)
	if !ok {
		return false
	}
	if n.Orig != nil {
		n = n.Orig
	}
	return n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" && n.Obj.Name == "Option"
}

func isGoRef(t check.Type) bool {
	_, ok := check.GoRefElem(t)
	return ok
}

// goContext is the hidden context.Context argument of a Go call whose
// binding marks it (`// ergo:context`, IG10): the calling task's, which
// an interruption cancels (rt.GoContext; inside a mask, a context that is
// never cancelled).
func (fc *funcCtx) goContext(x syntax.Node) string {
	return fc.l.taskPkg() + ".GoContext(" + taskOf(fc.envExpr(x)) + ")"
}

// afterGoContext writes, after a Go call that took the task's context, the
// interruption point at which the call's cancellation becomes the task's
// interruption: an interrupted task unwinds there, so its releases run,
// rather than going on with the context.Canceled error Go returned.
func (fc *funcCtx) afterGoContext(x syntax.Node, ctx bool) {
	if ctx {
		fc.poll(x)
	}
}
