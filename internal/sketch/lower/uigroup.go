package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Skipping (P-ui; stdlib.md, UI 13). A component call is a statement call
// of a function the program declares whose first argument is a
// *ui.Frame[E] and whose other arguments are values of types that compare
// with == and hold no pointer, written as names, field selections or
// constants, with no error or capability row: a view is pure apart from
// its writes through the frame (UI 1), so equal arguments write equal
// nodes. The lowering passes such a call through std ui's group, which
// writes the nodes the last pass's call wrote when its arguments are
// equal, and runs the call otherwise:
//
//	group[E, A](env, f, args, func(_ *Env) (struct{}, error) { Card(env, f, a, b); return struct{}{}, nil }, site)
//
// A is the one argument's type, or a struct of them. The arguments are
// evaluated twice (into args, and by the call when it runs), which is why
// only names, selections and constants qualify: they have no effect and
// give the same value both times.
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
	frame := uiFrame(fc.typeOf(x.Args[0]))
	if frame == nil {
		return nil
	}
	for _, a := range x.Args[1:] {
		if !plainOperand(a) || !groupArg(fc.typeOf(a)) {
			return nil
		}
	}
	for _, g := range frame.Obj.Pkg.Funcs {
		if g.Name == "group" {
			return g
		}
	}
	return nil
}

// uiFrame is the *ui.Frame[E] type t names, nil for any other type.
func uiFrame(t check.Type) *check.Named {
	p, ok := t.(*check.Pointer)
	if !ok {
		return nil
	}
	n, ok := p.Elem.(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || n.Obj.Pkg.Path != "ui" || !n.Obj.Pkg.Std() || n.Obj.Name != "Frame" || len(n.TArgs) != 1 {
		return nil
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
	frame := uiFrame(fc.typeOf(x.Args[0]))
	var types, vals []string
	for _, a := range x.Args[1:] {
		types = append(types, fc.goType(fc.typeOf(a)))
		vals = append(vals, fc.value(a))
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
	old := fc.inGroup
	fc.inGroup = x
	text, _, fallible := fc.callText(x, c)
	fc.inGroup = old
	if fallible {
		text = "_ = " + text
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
