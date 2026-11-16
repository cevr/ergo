package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Skipping (P-ui; stdlib.md, UI 13). A component call is a statement call
// of a function declared outside the frame's package whose first argument
// is a frame (*ui.Frame[E], or any frame type with a group: groupFrame)
// and whose other arguments are values of types that compare
// with == and hold no pointer, written as names, field selections or
// constants, with no error or capability row: a view is pure apart from
// its writes through the frame (UI 1), so equal arguments write equal
// nodes. The lowering passes such a call through the frame's group, which
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

// uiGroup is the group function a component call x of c goes through,
// or nil when x is not a component call. A call of a function of the
// frame's own package is none: those are the library's primitives, which
// the group writes through (std ui's Text, Box and the group's helpers).
func (fc *funcCtx) uiGroup(x *syntax.CallExpr, c *check.Call) *check.Func {
	if uiSkipOff || rewriteLowering == "reference" || fc.inGroup == x || c.Kind != check.CallFunc || c.Recv != nil || x.Trailing != nil {
		return nil
	}
	fn := c.Func
	if fn == nil || fn.Pkg == nil || fn.Decl == nil || fn.Decl.Extern != nil || check.GoBuiltin(fn) != "" || userExtern(fn) {
		return nil
	}
	sig := fn.Sig
	if len(sig.Results) != 0 || failsHere(c) || len(rowElems(c.Needs)) > 0 || len(x.Args) == 0 {
		return nil
	}
	if _, ok := unparen(x.Args[0]).(*syntax.Ident); !ok {
		return nil
	}
	frame, g := fc.l.groupFrame(fc.typeOf(x.Args[0]))
	if g == nil || fn.Pkg == frame.Obj.Pkg {
		return nil
	}
	for _, a := range x.Args[1:] {
		if !plainOperand(a) || !groupArg(fc.typeOf(a)) {
			return nil
		}
	}
	return g
}

// groupFrame is the frame type t points to and its group function, or
// nils. A frame is a generic type T[E] of any package that declares a
// group over it (the group protocol, stdlib.md UI 13): a function of the
// shape
//
//	func g[E, A](f *T[E], args A, body func(), at CallSite)
//
// and a top-level law that reaches g, through the functions of the
// package it names: the law the skip rests on, that equal arguments write
// equal nodes, which the package's own ergo test runs. The lowering finds
// the pair by that checked shape and that law, never by a name or by std
// membership (the identity-blind rule, OPT4a; owner, 2026-11-16: std has
// no privileges over user code), so a user's UI library gets the Go std
// ui gets (TestGroupProtocolCopyAgrees). std ui's Frame and group, with
// the law GroupSkipsEqualArguments, are one such pair.
func (l *lowerer) groupFrame(t check.Type) (*check.Named, *check.Func) {
	p, ok := t.(*check.Pointer)
	if !ok {
		return nil, nil
	}
	n, ok := p.Elem.(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) != 1 {
		return nil, nil
	}
	frame := origin(n)
	g, ok := l.groups[frame]
	if !ok {
		for _, h := range n.Obj.Pkg.Funcs {
			if groupShape(h, frame) && groupLawed(n.Obj.Pkg, h) {
				g = h
				break
			}
		}
		if l.groups == nil {
			l.groups = map[*check.Named]*check.Func{}
		}
		l.groups[frame] = g
	}
	if g == nil {
		return nil, nil
	}
	return n, g
}

// groupLawed reports a top-level law of pkg that reaches g: its body
// names g, or names a function of pkg whose body reaches g. A group with
// no such law is an ordinary function: the compiler never sends a call
// through a function whose package has not stated the law it rests on.
func groupLawed(pkg *check.Package, g *check.Func) bool {
	if pkg.Info == nil || g.Decl == nil {
		return false
	}
	seen := map[*syntax.FuncDecl]bool{}
	var reaches func(n syntax.Node) bool
	reaches = func(n syntax.Node) bool {
		found := false
		syntax.Inspect(n, func(m syntax.Node) bool {
			if found {
				return false
			}
			id, ok := m.(*syntax.Ident)
			if !ok {
				return true
			}
			f, ok := pkg.Info.Uses[id].(*check.Func)
			if !ok || f.Pkg != pkg || f.Decl == nil {
				return true
			}
			if f.Decl == g.Decl {
				found = true
			} else if !seen[f.Decl] && f.Decl.Body != nil {
				seen[f.Decl] = true
				found = reaches(f.Decl.Body)
			}
			return !found
		})
		return found
	}
	for _, law := range pkg.Laws {
		if law.Owner == nil && law.Machine == nil && law.Decl != nil && law.Decl.Body != nil && reaches(law.Decl.Body) {
			return true
		}
	}
	return false
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
	frame, _ := fc.l.groupFrame(fc.typeOf(x.Args[0]))
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
