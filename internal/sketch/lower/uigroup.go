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
// A is the one argument's type, or a struct of them. A name, a field
// selection or a constant is written twice (into args, and in the call
// when it runs): it has no effect and gives the same value both times.
// Any other argument is evaluated once, in order, into a Go local that
// both read (fc.preset), as an ordinary call evaluates it once; one with
// a row (a failure, a capability, a try or catch) is no group, as Compose
// takes any argument expression and compares its value in the callee.
// A record argument the component does not read all of is compared by
// the fields it reads (check.Info.Reads; NS2b) when each of them compares
// with == and holds no pointer, so a record holding a slice the component
// never reads is a group too (readFields). With NS2c a compared value
// may also be a slice, a Map or a record holding one (eqArg): the call
// goes through groupBy with the generated structural equality and a copy
// the group keeps, made only when the call runs (sameLit, keepLit), and
// only after a pass whose ==-compared values stayed (cheapLit, P-ui-flow).
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
	for k, a := range x.Args {
		if k == 0 {
			continue
		}
		if !(plainOperand(a) || fc.rowFree(a)) {
			return nil
		}
		if _, ok := fc.readFields(fn, k, x); !ok && !eqArg(fc.typeOf(a)) {
			return nil
		}
	}
	// A call whose arguments need generated equality goes through the
	// frame package's groupBy (groupCall); with no lawed groupBy it is no
	// component call.
	if fc.groupNeedsBy(fn, x) && groupByOf(frame) == nil {
		return nil
	}
	return g
}

// groupNeedsBy reports a component call x of fn some of whose compared
// values Go's == cannot compare, so it goes through groupBy (NS2c).
func (fc *funcCtx) groupNeedsBy(fn *check.Func, x *syntax.CallExpr) bool {
	by := false
	for k, a := range x.Args {
		if k == 0 {
			continue
		}
		if fields, ok := fc.readFields(fn, k, x); ok {
			for _, f := range fields {
				by = by || !groupArg(f.Type)
			}
		} else {
			by = by || !groupArg(fc.typeOf(a))
		}
	}
	return by
}

// uiEqOff selects the lowering without generated equality (NS2b's): an
// argument or read field that Go's == cannot compare makes the call no
// group.
var uiEqOff bool

// SetUIEqForTest selects comparing slices, Maps and the records that hold
// them by their generated structural equality, against a copy the group
// keeps (true, the default; NS2c), or making such a call no group (false),
// and returns a function that restores the previous choice.
func SetUIEqForTest(on bool) func() {
	old := uiEqOff
	uiEqOff = !on
	return func() { uiEqOff = old }
}

// eqArg reports a type a group may compare: one Go's == compares that
// holds no pointer (groupArg), or, with NS2c, one whose structural
// equality the lowering generates (a slice, a Map, a record, tuple or
// union that holds one) and that reaches no pointer and no function, so
// that equal values are interchangeable and a copy of one is a value of
// its own. Its compare is O(n) in the value, against the O(nodes) run of
// the call it may skip (measured: about 10 instructions a row of ints
// against about 7,700 to run the row; corpus-wave/LY/ns2c-proposal.md).
func eqArg(t check.Type) bool {
	if groupArg(t) {
		return true
	}
	return !uiEqOff && check.Equatable(t) && !mentionsTypeParam(t) && valueFree(t, map[check.Type]bool{})
}

// valueFree is pointerFree that also goes through a slice's elements and a
// Map's keys and values.
func valueFree(t check.Type, seen map[check.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	if n, ok := t.(*check.Named); ok {
		return valueFree(n.Underlying(), seen)
	}
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Slice:
		return valueFree(u.Elem, seen)
	case *check.Map:
		return valueFree(u.Key, seen) && valueFree(u.Elem, seen)
	case *check.Array:
		return valueFree(u.Elem, seen)
	case *check.Struct:
		for _, f := range u.Fields {
			if !valueFree(f.Type, seen) {
				return false
			}
		}
		return true
	case *check.Union:
		for _, v := range u.Variants {
			if !valueFree(v, seen) {
				return false
			}
		}
		return true
	case *check.Tuple:
		for _, e := range u.Types {
			if !valueFree(e, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// uiReadsOff selects the lowering without read sets (NS2a's): a record
// argument is compared whole, and one that does not compare is no group.
var uiReadsOff bool

// SetUIReadsForTest selects comparing only the fields a component reads
// of a record argument (true, the default) or the whole argument (false),
// and returns a function that restores the previous choice.
func SetUIReadsForTest(on bool) func() {
	old := uiReadsOff
	uiReadsOff = !on
	return func() { uiReadsOff = old }
}

// readFields is the fields of the record argument k of the component call
// x of fn that fn reads (check.Info.Reads; NS2b), when the group may
// compare those alone: the argument has the parameter's record type, with
// no conversion and no type arguments, fn's body does not read all of it,
// and each field it reads is one a group may compare (eqArg): with Go's
// == in constant time, or, with NS2c, by generated equality.
func (fc *funcCtx) readFields(fn *check.Func, k int, x *syntax.CallExpr) ([]*check.Field, bool) {
	if uiReadsOff || fn.Pkg.Info == nil || len(x.Args) != len(fn.Sig.Params) {
		return nil, false
	}
	sets, ok := fn.Pkg.Info.Reads[fn.Decl]
	if !ok || k >= len(sets) || sets[k].All {
		return nil, false
	}
	a := x.Args[k]
	t := fn.Sig.Params[k].Type
	n, ok := t.(*check.Named)
	if _, conv := fc.l.info.Implicit[a]; conv || !ok || fc.typeOf(a) != t || len(n.TArgs) != 0 {
		return nil, false
	}
	s, ok := n.Underlying().(*check.Struct)
	if !ok {
		return nil, false
	}
	var out []*check.Field
	for _, name := range sets[k].Fields {
		var field *check.Field
		for _, f := range s.Fields {
			if f.Name == name {
				field = f
				break
			}
		}
		if field == nil || !eqArg(field.Type) {
			return nil, false
		}
		out = append(out, field)
	}
	return out, true
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

// GroupFuncsForTest is the functions of pkg a component call goes through,
// found as groupFrame and groupByOf find them: for each frame type of pkg,
// its lawed group and groupBy. A test names the group calls it counts by
// these, never by their spelling (STD HAS NO PRIVILEGES: std ui may call
// its group writeGroup).
func GroupFuncsForTest(pkg *check.Package) []*check.Func {
	var out []*check.Func
	seen := map[*check.Func]bool{}
	add := func(g *check.Func) {
		if g != nil && !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, h := range pkg.Funcs {
		if h.Sig == nil || len(h.Sig.Params) == 0 {
			continue
		}
		p, ok := h.Sig.Params[0].Type.(*check.Pointer)
		if !ok {
			continue
		}
		n, ok := p.Elem.(*check.Named)
		if !ok || n.Obj == nil || n.Obj.Pkg != pkg {
			continue
		}
		frame := origin(n)
		if groupShape(h, frame) && groupLawed(pkg, h) {
			add(h)
		}
		add(groupByOf(frame))
	}
	return out
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
	// by: some compared value is one Go's == cannot compare, so the call
	// goes through groupBy with generated equality and copy (NS2c).
	by := fc.groupNeedsBy(c.Func, x)
	var types, vals []string
	var ctypes []check.Type
	preset := map[syntax.Expr]string{}
	for k, a := range x.Args {
		if k == 0 {
			continue
		}
		t := fc.goType(fc.typeOf(a))
		if fields, ok := fc.readFields(c.Func, k, x); ok {
			// The fields the component reads, of the argument evaluated
			// once into a local the call reads (NS2b).
			loc := fc.temp("arg")
			fc.line("var " + loc + " " + t + " = " + fc.value(a))
			preset[a] = loc
			td := fc.l.typeDecl(fc.typeOf(a).(*check.Named).Obj, a)
			for _, f := range fields {
				types = append(types, fc.goType(f.Type))
				ctypes = append(ctypes, f.Type)
				vals = append(vals, loc+"."+td.fieldName[f.Name])
			}
			continue
		}
		types = append(types, t)
		ctypes = append(ctypes, fc.typeOf(a))
		v := fc.value(a)
		// Under groupBy a name or selection is read once too: a value the
		// value rule copies at the call would be copied twice.
		if _, constant := fc.l.info.Values[a]; !constant && (by || !plainOperand(a)) {
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
	body := fc.goType(g.Sig.Params[2].Type)
	env := strings.TrimSuffix(strings.TrimPrefix(body, "func("), ") (struct{}, error)")
	if env == body || strings.Contains(env, ",") {
		ice(x, "std ui's group body has the Go type %s, not func(env) (struct{}, error)", body)
	}
	lit := "func(_ " + env + ") (struct{}, error) {\n" + text + "\nreturn struct{}{}, nil\n}"
	operands := []string{f, args}
	if by {
		g = groupByOf(frame)
		if g == nil {
			ice(x, "the frame's package declares no lawed groupBy (uiGroup admits no such call)")
		}
		operands = append(operands, fc.sameLit(env, argType, ctypes, x), fc.cheapLit(env, argType, ctypes, x), fc.keepLit(env, argType, ctypes, x))
	}
	gd := fc.l.useFunc(g, x)
	if gd.method != nil {
		ice(x, "std ui's group lowered as a method")
	}
	site, ok := g.Sig.Params[len(g.Sig.Params)-1].Type.(*check.Named)
	if !ok {
		ice(x, "std ui's group takes no CallSite last")
	}
	lead := []string{}
	if !gd.envFree {
		lead = fc.taskDestArgs(x, c, g.Sig)
	}
	operands = append(append(lead, operands...), lit, fc.siteValue(x, site))
	call := gd.name + "[" + fc.goType(frame.TArgs[0]) + ", " + argType + "](" + strings.Join(operands, ", ") + ")"
	return call, false, gd.fallible
}

// sameLit is the Go text of groupBy's same for arguments of the Go type
// argType holding values of ctypes (one, or fields F0, F1, …): each
// compared by Go's == or its generated structural equality (equal.go).
func (fc *funcCtx) sameLit(env, argType string, ctypes []check.Type, x syntax.Node) string {
	a, b := fc.temp("ga"), fc.temp("gb")
	var conj []string
	if len(ctypes) == 1 {
		conj = []string{fc.equalCall(ctypes[0], a, b, x)}
	} else {
		for i, t := range ctypes {
			n := "F" + strconv.Itoa(i)
			conj = append(conj, fc.equalCall(t, a+"."+n, b+"."+n, x))
		}
	}
	return "func(_ " + env + ", " + a + ", " + b + " " + argType + ") (bool, error) {\nreturn " + strings.Join(conj, " && ") + ", nil\n}"
}

// cheapLit is the Go text of groupBy's cheap: the values among the
// arguments that Go's == compares (groupArg), each by ==, and true when
// there are none. groupBy keeps its own copy of the arguments only after a
// pass whose cheap values were equal to the last ones (P-ui-flow).
func (fc *funcCtx) cheapLit(env, argType string, ctypes []check.Type, x syntax.Node) string {
	a, b := fc.temp("ga"), fc.temp("gb")
	var conj []string
	if len(ctypes) == 1 {
		if groupArg(ctypes[0]) {
			conj = append(conj, a+" == "+b)
		}
	} else {
		for i, t := range ctypes {
			if groupArg(t) {
				n := ".F" + strconv.Itoa(i)
				conj = append(conj, a+n+" == "+b+n)
			}
		}
	}
	if len(conj) == 0 {
		conj = []string{"true"}
	}
	return "func(_ " + env + ", " + a + ", " + b + " " + argType + ") (bool, error) {\n_, _ = " + a + ", " + b + "\nreturn " + strings.Join(conj, " && ") + ", nil\n}"
}

// keepLit is the Go text of groupBy's keep: a copy of the arguments that
// shares no slice or Map with them (the value rule's copy, values.go).
func (fc *funcCtx) keepLit(env, argType string, ctypes []check.Type, x syntax.Node) string {
	a := fc.temp("ga")
	var v string
	if len(ctypes) == 1 {
		v = fc.cloneValue(ctypes[0], a, x)
	} else {
		var fs []string
		for i, t := range ctypes {
			fs = append(fs, fc.cloneValue(t, a+".F"+strconv.Itoa(i), x))
		}
		v = argType + "{" + strings.Join(fs, ", ") + "}"
	}
	return "func(_ " + env + ", " + a + " " + argType + ") (" + argType + ", error) {\nreturn " + v + ", nil\n}"
}

// groupByOf is the group function over frame for arguments Go's == cannot
// compare, found by its checked shape as groupFrame finds group:
//
//	func g[E, A](f *T[E], args A, same func(a A, b A) bool, cheap func(a A, b A) bool, keep func(a A) A, body func(), at CallSite)
func groupByOf(frame *check.Named) *check.Func {
	for _, g := range frame.Obj.Pkg.Funcs {
		sig := g.Sig
		if g.Decl == nil || g.Decl.Extern != nil || sig == nil || len(sig.TParams) != 2 || len(sig.Params) != 7 || len(sig.Results) != 0 {
			continue
		}
		p, ok := sig.Params[0].Type.(*check.Pointer)
		if !ok {
			continue
		}
		f, ok := p.Elem.(*check.Named)
		a := check.Type(sig.TParams[1])
		if !ok || origin(f) != origin(frame) || sig.Params[1].Type != a {
			continue
		}
		same, ok1 := sig.Params[2].Type.(*check.Signature)
		cheap, ok4 := sig.Params[3].Type.(*check.Signature)
		keep, ok2 := sig.Params[4].Type.(*check.Signature)
		body, ok3 := sig.Params[5].Type.(*check.Signature)
		if !ok1 || !ok2 || !ok3 || !ok4 || len(same.Params) != 2 || len(same.Results) != 1 || len(cheap.Params) != 2 || len(cheap.Results) != 1 || len(keep.Params) != 1 || len(keep.Results) != 1 || keep.Results[0] != a || len(body.Params) != 0 || len(body.Results) != 0 {
			continue
		}
		// As group, groupBy is the frame package's only under a top-level
		// law that reaches it (groupLawed, SP9): never by std membership.
		if _, ok := sig.Params[6].Type.(*check.Named); ok && groupLawed(frame.Obj.Pkg, g) {
			return g
		}
	}
	return nil
}
