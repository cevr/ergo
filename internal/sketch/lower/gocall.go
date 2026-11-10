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

// hostFuncRef is the Go function of import go that x names, in any
// parentheses (pkg.F), or nil.
func (fc *funcCtx) hostFuncRef(x syntax.Expr) *check.Func {
	sel, ok := unparen(x).(*syntax.SelectorExpr)
	if !ok {
		return nil
	}
	id, ok := sel.X.(*syntax.Ident)
	if !ok {
		return nil
	}
	pn, ok := fc.l.info.Uses[id].(*check.PkgName)
	if !ok || !pn.Pkg.Host() {
		return nil
	}
	for _, o := range pn.Pkg.Exported() {
		if f, ok := o.(*check.Func); ok && f.Name == sel.Sel.Name {
			return f
		}
	}
	return nil
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
	// A function parameter (wf10 D2): a Go function of import go goes as
	// Go's own; any other function value through an adapter to Go's plain
	// function form (hostCallback). check.GoDirectSig keeps both within
	// the types whose Go form needs no conversion.
	callbacks := make([]*check.Signature, len(list))
	for i, p := range sig.Params {
		cb, ok := p.Type.(*check.Signature)
		if !ok || i >= len(list) || (variadic && i == len(sig.Params)-1) {
			continue
		}
		if fc.hostFuncRef(list[i]) != nil {
			if fc.goDirect == nil {
				fc.goDirect = map[syntax.Node]bool{}
			}
			fc.goDirect[unparen(list[i])] = true
			continue
		}
		callbacks[i] = cb
	}
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	vals := fc.callArgs(list, x)
	if c.Recv != nil {
		head, vals = vals[0]+head, vals[1:]
	}
	for i, cb := range callbacks {
		if cb != nil {
			vals[i] = fc.spill(fc.hostCallback(fc.bindFunc(vals[i]), cb, x))
		}
	}
	if variadic && len(vals) > 0 {
		vals[len(vals)-1] += "..."
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
		return operand{}
	}
	fc.line(strings.Join(lhs, ", ") + " := " + text)
	if e != "" {
		fc.line(e + " = " + fc.l.rt() + ".GoErrorOf(" + e + ")")
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

// goResult is the Ergo value of the Go result r of type t (the Ergo type
// the binding gives it), whose call's error is e ("" when it has none).
func (fc *funcCtx) goResult(x syntax.Node, t check.Type, r, e, name string) string {
	switch {
	case isOptionType(t):
		some, none := optionVariants(t, x)
		v := fc.temp("v")
		fc.line("var " + v + " " + fc.goType(t) + " = " + fc.goType(none) + "{}")
		fc.line("if " + r + " != nil {")
		fc.line("\t" + v + " = " + fc.goType(some) + "{" + memberName("Value") + ": " + r + "}")
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
