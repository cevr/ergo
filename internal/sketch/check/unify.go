package check

// Value-argument type inference by unification, after go/types unify.go
// (round-5 M1, M3). Unification walks a parameter type p, which mentions
// the callee's type parameters, against an argument type a and binds the
// free parameters. It reports false when the two cannot match; the call is
// then S108 at that argument, as Go reports "type … does not match …".
//
// The modes follow Go's:
//
//   - assign: the argument is assigned to the parameter. At the top the
//     match may be inexact; element types match exactly.
//   - exact: the types must be identical once parameters are bound.
//   - inexact (no flag): channel directions are ignored, and when one side
//     is an interface the other must implement it, method by method,
//     unifying the method signatures (interface inference); two interfaces
//     unify when one's method set is a subset of the other's.
//
// A bound parameter is matched against the next argument: a typed
// argument that conflicts with an earlier binding fails (Fn(x, y) with x an
// interface and y a struct). Of an interface and a non-interface match,
// Go keeps neither, since either choice would depend on argument order
// (go.dev/issue/60933); two interfaces must have the same method set, and
// of two that unify the named one is kept.
//
// Ergo departs from Go only where its type rules do:
//
//   - types are nominal (types.md), so a named type never unifies with a
//     type literal through its underlying type, except an interface, whose
//     values are assigned by implementation;
//   - a named union unifies with one of its variants, as a variant is
//     assignable to its union (types.md, Unions);
//   - the prelude Machine unifies with a machine declaration's value
//     through the Machine that value converts to (README Gap 46);
//   - row parameters bind by the row-union rule of unifyRow, never fail,
//     and apply to rows only;
//   - a result-position parameter matched against a function with no
//     result binds to "no value".
type unifyMode uint

const (
	unifyAssign unifyMode = 1 << iota
	unifyExact
)

// unifyDepthLimit stops unification on recursive interface types, as Go's
// unificationDepthLimit does.
const unifyDepthLimit = 50

// mentions reports whether t mentions a type parameter of the call, bound
// or free (go/types isParameterized).
func (in *inferer) mentions(t Type) bool {
	seen := map[*Named]bool{}
	var walk func(t Type) bool
	walk = func(t Type) bool {
		switch t := t.(type) {
		case *TypeParam:
			return in.set[t]
		case *Named:
			if seen[t] {
				return false
			}
			seen[t] = true
			for _, a := range t.TArgs {
				if walk(a) {
					return true
				}
			}
		case *RowArg:
			return walkRow(t.Row, walk)
		case *Slice:
			return walk(t.Elem)
		case *Array:
			return walk(t.Elem)
		case *Map:
			return walk(t.Key) || walk(t.Elem)
		case *Pointer:
			return walk(t.Elem)
		case *Tuple:
			for _, e := range t.Types {
				if walk(e) {
					return true
				}
			}
		case *Struct:
			for _, f := range t.Fields {
				if walk(f.Type) {
					return true
				}
			}
		case *Interface:
			for _, m := range t.Methods {
				if m.Sig != nil && walk(m.Sig) {
					return true
				}
			}
		case *Union:
			for _, v := range t.Variants {
				if walk(v) {
					return true
				}
			}
		case *MachineType:
			return walk(t.State) || walk(t.Signal)
		case *Signature:
			for _, p := range t.Params {
				if walk(p.Type) {
					return true
				}
			}
			for _, r := range t.Results {
				if walk(r) {
					return true
				}
			}
			return walkRow(t.Errors, walk) || walkRow(t.Uses, walk)
		}
		return false
	}
	return walk(t)
}

// free0 reports whether t is a free type-kinded parameter of the call.
func (in *inferer) free0(t Type) bool {
	tp, ok := t.(*TypeParam)
	return ok && tp.Kind == KindType && in.free(tp)
}

func walkRow(r *Row, walk func(Type) bool) bool {
	if r == nil {
		return false
	}
	for _, e := range r.Elems() {
		if walk(e) {
			return true
		}
	}
	return false
}

// unify matches the parameter type p against the argument type a in mode,
// binding free parameters, and reports whether they match.
func (in *inferer) unify(p, a Type, mode unifyMode) bool {
	return in.nify(p, a, mode, 0)
}

func (in *inferer) nify(x, y Type, mode unifyMode, depth int) bool {
	// x == y is checked after the parameter case: in a recursive call
	// f(x) with x of type T, the argument's T is the callee's own and
	// binds to itself.
	if x == y && !in.free0(x) {
		return true
	}
	if x == nil || y == nil || isInvalid(x) || isInvalid(y) {
		return true // reported elsewhere: no cascade (D3)
	}
	if depth > unifyDepthLimit {
		return false
	}
	depth++
	// A parameter of the call: bound, its binding must match; free, it
	// binds.
	if px, ok := x.(*TypeParam); ok && in.set[px] {
		if px.Kind != KindType {
			return true // a row parameter in a type position: rows bind in unifyRow
		}
		b := in.m[px]
		if b == nil {
			in.m[px] = y
			return true
		}
		if b == NoValue || y == NoValue {
			return b == y
		}
		if !in.nify(b, y, mode, depth) {
			return false
		}
		// An unknown row in the binding takes y's known part, as an if
		// join does (widenUnknown).
		if w := in.c.widenUnknown(b, y); w != b {
			b, in.m[px] = w, w
		}
		bi, yi := asInterface(b), asInterface(y)
		_, bn := b.(*Named)
		_, yn := y.(*Named)
		switch {
		case bi != nil && yi != nil:
			if bn && yn {
				return in.c.identical(b, y)
			}
			if len(bi.Methods) != len(yi.Methods) {
				return false
			}
		case bi != nil || yi != nil:
			// Either could be the parameter's type: choosing one would
			// depend on argument order (go.dev/issue/60933).
			return false
		}
		if mode&unifyExact == 0 && !bn && yn {
			in.m[px] = y // keep the named type, independent of order
		}
		return true
	}
	// A named union matches one of its variants (types.md, Unions).
	if xn, ok := x.(*Named); ok && mode&unifyExact == 0 {
		if u := asUnion(xn); u != nil {
			yn, _ := y.(*Named)
			if yn == nil || yn.origin() != xn.origin() {
				for _, v := range u.Variants {
					if vn, ok := v.(*Named); ok && yn != nil && vn.origin() == yn.origin() {
						return in.nify(v, y, unifyExact, depth)
					}
				}
				return false
			}
		}
	}
	// A machine declaration's value matches the prelude Machine (or InputMachine) of its
	// state, signal and step, as it is assignable to it (README Gap 46).
	if mode&unifyExact == 0 && isPreludeMachineValue(x) {
		if view, _ := in.c.machineValue(y); view != nil {
			y = view
		}
	}
	// Interface inference (inexact and assign modes).
	if mode&unifyExact == 0 {
		xi, yi := asInterface(x), asInterface(y)
		switch {
		case xi != nil && yi != nil:
			if xi.Set != yi.Set {
				return false
			}
			small, large := xi.Methods, yi.Methods
			if len(small) > len(large) {
				small, large = large, small
			}
			byName := make(map[string]*Method, len(large))
			for _, m := range large {
				byName[m.Name] = m
			}
			for _, m := range small {
				o := byName[m.Name]
				if o == nil || !in.nifySig(m.Sig, o.Sig, unifyExact, depth) {
					return false
				}
			}
			return true
		case xi != nil:
			return in.implements(xi, y, depth)
		case yi != nil:
			return in.implements(yi, x, depth)
		}
	}
	emode := mode
	if mode&unifyAssign != 0 {
		emode |= unifyExact
	}
	switch x := x.(type) {
	case *Basic:
		return in.c.identical(x, y)
	case *Named:
		yn, ok := y.(*Named)
		if !ok || yn.origin() != x.origin() || len(yn.TArgs) != len(x.TArgs) {
			return false
		}
		for i := range x.TArgs {
			if xr, ok := x.TArgs[i].(*RowArg); ok {
				if yr, ok := yn.TArgs[i].(*RowArg); ok {
					in.unifyRow(xr.Row, yr.Row)
					continue
				}
				return false
			}
			if !in.nify(x.TArgs[i], yn.TArgs[i], mode, depth) {
				return false
			}
		}
		return true
	case *RowArg:
		if yr, ok := y.(*RowArg); ok {
			in.unifyRow(x.Row, yr.Row)
			return true
		}
		return false
	case *Slice:
		ys, ok := y.(*Slice)
		return ok && in.nify(x.Elem, ys.Elem, emode, depth)
	case *Array:
		ya, ok := y.(*Array)
		return ok && x.Len == ya.Len && in.nify(x.Elem, ya.Elem, emode, depth)
	case *Map:
		ym, ok := y.(*Map)
		return ok && in.nify(x.Key, ym.Key, emode, depth) && in.nify(x.Elem, ym.Elem, emode, depth)
	case *Pointer:
		yp, ok := y.(*Pointer)
		return ok && in.nify(x.Elem, yp.Elem, emode, depth)
	case *Tuple:
		yt, ok := y.(*Tuple)
		if !ok || len(yt.Types) != len(x.Types) {
			return false
		}
		for i := range x.Types {
			if !in.nify(x.Types[i], yt.Types[i], mode, depth) {
				return false
			}
		}
		return true
	case *Signature:
		ys, ok := y.(*Signature)
		return ok && in.nifySig(x, ys, emode, depth)
	case *Interface:
		// Exact mode: the same methods, signatures unified.
		yi, ok := y.(*Interface)
		if !ok || x.Set != yi.Set || len(x.Methods) != len(yi.Methods) {
			return false
		}
		for _, m := range x.Methods {
			o := yi.method(m.Name)
			if o == nil || !in.nifySig(m.Sig, o.Sig, unifyExact, depth) {
				return false
			}
		}
		return true
	case *Union:
		yu, ok := y.(*Union)
		if !ok || len(yu.Variants) != len(x.Variants) {
			return false
		}
		for i := range x.Variants {
			if !in.nify(x.Variants[i], yu.Variants[i], unifyExact, depth) {
				return false
			}
		}
		return true
	case *MachineType:
		ym, ok := y.(*MachineType)
		return ok && in.nify(x.State, ym.State, emode, depth) && in.nify(x.Signal, ym.Signal, emode, depth)
	}
	return in.c.identical(x, y)
}

// nifySig unifies two signatures: parameters and results in mode, and
// rows by the row rule. A result-position parameter matched against no
// result binds to "no value" (Q3; Gleam's Nil, Effect's void).
func (in *inferer) nifySig(x, y *Signature, mode unifyMode, depth int) bool {
	if x == nil || y == nil {
		return x == y
	}
	if len(x.Params) != len(y.Params) {
		return false
	}
	for i := range x.Params {
		if !in.nify(x.Params[i].Type, y.Params[i].Type, mode, depth) {
			return false
		}
	}
	switch {
	case len(x.Results) == len(y.Results):
		for i := range x.Results {
			if !in.nify(x.Results[i], y.Results[i], mode, depth) {
				return false
			}
		}
	case len(x.Results) == 1 && len(y.Results) == 0:
		tp, ok := x.Results[0].(*TypeParam)
		if !ok || !in.free(tp) {
			return false
		}
		in.m[tp] = NoValue
	default:
		return false
	}
	in.unifyRow(x.Errors, y.Errors)
	in.unifyRow(x.Uses, y.Uses)
	return true
}

// implements unifies each method of the interface it with the method of
// the same name of t, as Go's unifier does for one interface and one
// concrete type: every method must exist and its signature unify.
func (in *inferer) implements(it *Interface, t Type, depth int) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	for _, m := range it.Methods {
		got, _ := in.c.methodOf(t, m.Name)
		if got == nil || len(got.TParams) > 0 || !in.nifySig(m.Sig, got, unifyExact, depth) {
			return false
		}
	}
	return true
}
