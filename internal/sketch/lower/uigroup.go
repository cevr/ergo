package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Skipping (P-ui; stdlib.md, UI 13). A component call is a statement call
// of a function the program declares whose first argument is a
// *ui.Frame[E] (a name) and whose other arguments are values of types
// that compare with == and hold no pointer, written as any expression
// with no error or capability row (NS2a), the call itself having none: a view is pure apart from
// its writes through the frame (UI 1), so equal arguments write equal
// nodes. The lowering passes such a call through std ui's group, which
// writes the nodes the last pass's call wrote when its arguments are
// equal, and runs the call otherwise:
//
//	group[E, A](env, f, args, func(_ *Env) (struct{}, error) { Card(env, f, a, b); return struct{}{}, nil }, site)
//
// A is the one argument's type, or a struct of them. A name, a field
// selection or a constant is written twice (into args, and in the call
// when it runs): it has no effect and gives the same value both times.
// Any other argument is evaluated once, in order, into a Go local that
// both read (fc.preset), as an ordinary call evaluates it once; one with
// a row (a failure, a capability, a try or catch) is no group, as Compose
// takes any argument expression and compares its value in the callee.
//
// SetUISkipForTest(false) selects the reference: every call runs
// (TestUISkipAgrees); so does a compiler built with
// rewriteLowering=reference (rewrite.go), the build the examples run
// under to pass under both lowerings. Prior art: Compose's compiler makes every
// composable call a group and skips one whose stable arguments are equal;
// Elm's Html.Lazy and React's memo skip only where the author wrote it.

// uiSkipOff selects the reference lowering: no component call is a group.
var uiSkipOff bool

// SetUISkipForTest selects skipping of component calls (true, the
// default) or the reference lowering that runs every call (false), and
// returns a function that restores the previous choice.
func SetUISkipForTest(on bool) func() {
	old := uiSkipOff
	uiSkipOff = !on
	return func() { uiSkipOff = old }
}

// uiGroup is the std ui group function a component call x of c goes
// through, or nil when x is not a component call.
func (fc *funcCtx) uiGroup(x *syntax.CallExpr, c *check.Call) *check.Func {
	if uiSkipOff || rewriteLowering == "reference" || fc.inGroup == x || c.Kind != check.CallFunc || c.Recv != nil || x.Trailing != nil {
		return nil
	}
	fn := c.Func
	if fn == nil || fn.Pkg == nil || fn.Pkg.Std() || fn.Decl == nil || fn.Decl.Extern != nil || check.GoBuiltin(fn) != "" || userExtern(fn) {
		return nil
	}
	sig := fn.Sig
	if len(sig.Results) != 0 || failsHere(c) || len(rowElems(c.Needs)) > 0 || len(x.Args) == 0 {
		return nil
	}
	if _, ok := unparen(x.Args[0]).(*syntax.Ident); !ok {
		return nil
	}
	_, g := groupFrame(fc.typeOf(x.Args[0]))
	if g == nil {
		return nil
	}
	for _, a := range x.Args[1:] {
		if !(plainOperand(a) || fc.rowFree(a)) || !groupArg(fc.typeOf(a)) {
			return nil
		}
	}
	return g
}

// rowFree reports an expression whose evaluation can neither fail nor use
// a capability: no try or catch, and no call with a row, outside a
// function literal (which an argument of a group cannot be: a function
// does not compare).
func (fc *funcCtx) rowFree(x syntax.Expr) bool {
	free := true
	syntax.Inspect(x, func(n syntax.Node) bool {
		if !free {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.TryExpr, *syntax.CatchExpr:
			free = false
		case *syntax.CallExpr:
			if c := fc.l.info.Calls[n]; c == nil || failsHere(c) || len(rowElems(c.Needs)) > 0 || c.StartsTask {
				free = false
			}
		}
		return free
	})
	return free
}

// groupFrame is the frame type t points to and its group function, or
// nils. A frame is a generic type T[E] of a std package that declares a
// group over it: a function of the shape
//
//	func g[E, A](f *T[E], args A, body func(), at CallSite)
//
// The lowering finds it by that checked shape, never by a name (the
// identity-blind rule, OPT4a): std ui's Frame and group are the one pair
// today.
func groupFrame(t check.Type) (*check.Named, *check.Func) {
	p, ok := t.(*check.Pointer)
	if !ok {
		return nil, nil
	}
	n, ok := p.Elem.(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || !n.Obj.Pkg.Std() || len(n.TArgs) != 1 {
		return nil, nil
	}
	for _, g := range n.Obj.Pkg.Funcs {
		if groupShape(g, origin(n)) {
			return n, g
		}
	}
	return nil, nil
}

// groupShape reports g a group over the frame type frame (groupFrame).
func groupShape(g *check.Func, frame *check.Named) bool {
	if g.Decl == nil || g.Decl.Extern != nil || g.Sig == nil {
		return false
	}
	sig := g.Sig
	if len(sig.TParams) != 2 || len(sig.Params) != 4 || len(sig.Results) != 0 {
		return false
	}
	p, ok := sig.Params[0].Type.(*check.Pointer)
	if !ok {
		return false
	}
	f, ok := p.Elem.(*check.Named)
	if !ok || origin(f) != frame || len(f.TArgs) != 1 || f.TArgs[0] != check.Type(sig.TParams[0]) || sig.Params[1].Type != check.Type(sig.TParams[1]) {
		return false
	}
	body, ok := sig.Params[2].Type.(*check.Signature)
	if !ok || len(body.Params) != 0 || len(body.Results) != 0 {
		return false
	}
	_, ok = sig.Params[3].Type.(*check.Named)
	return ok
}

// origin is the generic type n instantiates, or n.
func origin(n *check.Named) *check.Named {
	if n.Orig != nil {
		return n.Orig
	}
	return n
}

// plainOperand reports a name, a field selection of one, or a constant:
// an operand with no effect whose two evaluations give one value.
func plainOperand(x syntax.Expr) bool {
	switch e := unparen(x).(type) {
	case *syntax.Ident:
		return true
	case *syntax.BasicLit:
		return true
	case *syntax.SelectorExpr:
		return plainOperand(e.X)
	}
	return false
}

// groupArg reports a type whose values compare with Go's == and hold no
// pointer, so that equal values are interchangeable: a group's argument.
func groupArg(t check.Type) bool {
	return check.Comparable(t) && !mentionsTypeParam(t) && pointerFree(t, map[check.Type]bool{})
}

// pointerFree reports a type no value of which reaches a pointer: through
// a record's fields, a union's variants, a tuple's or an array's elements.
func pointerFree(t check.Type, seen map[check.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	if n, ok := t.(*check.Named); ok {
		return pointerFree(n.Underlying(), seen)
	}
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Struct:
		for _, f := range u.Fields {
			if !pointerFree(f.Type, seen) {
				return false
			}
		}
		return true
	case *check.Union:
		for _, v := range u.Variants {
			if !pointerFree(v, seen) {
				return false
			}
		}
		return true
	case *check.Tuple:
		for _, e := range u.Types {
			if !pointerFree(e, seen) {
				return false
			}
		}
		return true
	case *check.Array:
		return pointerFree(u.Elem, seen)
	}
	return false
}

// groupCall is the Go text of the component call x of c through group g.
func (fc *funcCtx) groupCall(x *syntax.CallExpr, c *check.Call, g *check.Func) (string, bool, bool) {
	frame, _ := groupFrame(fc.typeOf(x.Args[0]))
	var types, vals []string
	preset := map[syntax.Expr]string{}
	for _, a := range x.Args[1:] {
		t := fc.goType(fc.typeOf(a))
		types = append(types, t)
		v := fc.value(a)
		if _, constant := fc.l.info.Values[a]; !constant && !plainOperand(a) {
			// Evaluated once, here, in order: the call reads the local.
			loc := fc.temp("arg")
			fc.line("var " + loc + " " + t + " = " + v)
			preset[a] = loc
			v = loc
		}
		vals = append(vals, v)
	}
	f := fc.value(x.Args[0])
	var argType, args string
	switch len(types) {
	case 0:
		argType, args = "struct{}", "struct{}{}"
	case 1:
		argType, args = types[0], vals[0]
	default:
		var fields []string
		for i, t := range types {
			fields = append(fields, "F"+strconv.Itoa(i)+" "+t)
		}
		argType = "struct{" + strings.Join(fields, "; ") + "}"
		args = argType + "{" + strings.Join(vals, ", ") + "}"
	}
	old, oldPreset := fc.inGroup, fc.preset
	fc.inGroup, fc.preset = x, preset
	text, _, fallible := fc.callText(x, c)
	fc.inGroup, fc.preset = old, oldPreset
	if fallible {
		ice(x, "a component call lowered as fallible")
	}
	gd := fc.l.useFunc(g, x)
	if gd.method != nil {
		ice(x, "std ui's group lowered as a method")
	}
	site, ok := g.Sig.Params[len(g.Sig.Params)-1].Type.(*check.Named)
	if !ok {
		ice(x, "std ui's group takes no CallSite last")
	}
	body := fc.goType(g.Sig.Params[2].Type)
	env := strings.TrimSuffix(strings.TrimPrefix(body, "func("), ") (struct{}, error)")
	if env == body || strings.Contains(env, ",") {
		ice(x, "std ui's group body has the Go type %s, not func(env) (struct{}, error)", body)
	}
	lit := "func(_ " + env + ") (struct{}, error) {\n" + text + "\nreturn struct{}{}, nil\n}"
	lead := []string{}
	if !gd.pure {
		lead = fc.taskDestArgs(x, c, g.Sig)
	}
	call := gd.name + "[" + fc.goType(frame.TArgs[0]) + ", " + argType + "](" + strings.Join(append(lead, f, args, lit, fc.siteValue(x, site)), ", ") + ")"
	return call, false, gd.fallible
}
