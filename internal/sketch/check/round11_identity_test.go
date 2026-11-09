package check

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// structural is the structural identity the interned ids must agree with
// (the checker's identical before class E): a named type by its origin and
// identical type arguments, a row by its set, a signature by its parameters,
// results and rows, an interface by method names, a struct by field names
// and types, a union and a machine only by the object.
func structural(a, b Type) bool {
	if a == b {
		return true
	}
	switch a := a.(type) {
	case *Named:
		b, ok := b.(*Named)
		if !ok || a.origin() != b.origin() || len(a.TArgs) != len(b.TArgs) {
			return false
		}
		for i := range a.TArgs {
			if !structural(a.TArgs[i], b.TArgs[i]) {
				return false
			}
		}
		return true
	case *RowArg:
		b, ok := b.(*RowArg)
		return ok && structuralRow(a.Row, b.Row)
	case *RowVar:
		// A use as a row element: one variable, identical bindings.
		b, ok := b.(*RowVar)
		if !ok || a.root() != b.root() {
			return false
		}
		bind := func(v *RowVar, tp *TypeParam) Type {
			if x, ok := v.m[tp]; ok {
				return x
			}
			return tp
		}
		for _, tp := range a.root().Fn.Sig.TParams {
			if !structural(bind(a, tp), bind(b, tp)) {
				return false
			}
		}
		return true
	case *Slice:
		b, ok := b.(*Slice)
		return ok && structural(a.Elem, b.Elem)
	case *Map:
		b, ok := b.(*Map)
		return ok && structural(a.Key, b.Key) && structural(a.Elem, b.Elem)
	case *Pointer:
		b, ok := b.(*Pointer)
		return ok && structural(a.Elem, b.Elem)
	case *Signature:
		b, ok := b.(*Signature)
		if !ok || len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) || a.NonEscaping != b.NonEscaping {
			return false
		}
		for i := range a.Params {
			if !structural(a.Params[i].Type, b.Params[i].Type) {
				return false
			}
		}
		for i := range a.Results {
			if !structural(a.Results[i], b.Results[i]) {
				return false
			}
		}
		return structuralRow(a.Errors, b.Errors) && structuralRow(a.Uses, b.Uses)
	case *Interface:
		b, ok := b.(*Interface)
		if !ok || a.Set != b.Set || len(a.Methods) != len(b.Methods) {
			return false
		}
		for _, m := range a.Methods {
			o := b.method(m.Name)
			if o == nil || !structural(m.Sig, o.Sig) {
				return false
			}
		}
		return true
	case *Struct:
		b, ok := b.(*Struct)
		if !ok || len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if a.Fields[i].Name != b.Fields[i].Name || !structural(a.Fields[i].Type, b.Fields[i].Type) {
				return false
			}
		}
		return true
	case *Tuple:
		b, ok := b.(*Tuple)
		if !ok || len(a.Types) != len(b.Types) {
			return false
		}
		for i := range a.Types {
			if !structural(a.Types[i], b.Types[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// structuralRow compares two rows as sets, element by element, a row
// variable read as its solution with its use's substitution (class F).
func structuralRow(a, b *Row) bool {
	flat := func(r *Row) []Type {
		var out []Type
		for _, e := range r.Elems() {
			if v, ok := e.(*RowVar); ok {
				out = append(out, v.Solution().Elems()...)
				continue
			}
			out = append(out, e)
		}
		return out
	}
	in := func(xs []Type, t Type) bool {
		for _, e := range xs {
			if structural(e, t) {
				return true
			}
		}
		return false
	}
	fa, fb := flat(a), flat(b)
	for _, e := range fa {
		if !in(fb, e) {
			return false
		}
	}
	for _, e := range fb {
		if !in(fa, e) {
			return false
		}
	}
	return true
}

// collectTypes gathers every type the check recorded and their subterms.
func collectTypes(c *checker) []Type {
	seen := map[Type]bool{}
	var out []Type
	var walk func(t Type)
	walk = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
		switch t := t.(type) {
		case *Named:
			for _, a := range t.TArgs {
				walk(a)
			}
		case *RowArg:
			for _, e := range t.Row.Elems() {
				walk(e)
			}
		case *Slice:
			walk(t.Elem)
		case *Map:
			walk(t.Key)
			walk(t.Elem)
		case *Pointer:
			walk(t.Elem)
		case *Tuple:
			for _, x := range t.Types {
				walk(x)
			}
		case *Struct:
			for _, f := range t.Fields {
				walk(f.Type)
			}
		case *Interface:
			for _, m := range t.Methods {
				walk(m.Sig)
			}
		case *Signature:
			for _, p := range t.Params {
				walk(p.Type)
			}
			for _, r := range t.Results {
				walk(r)
			}
			for _, e := range t.Errors.Elems() {
				walk(e)
			}
			for _, e := range t.Uses.Elems() {
				walk(e)
			}
		}
	}
	for _, t := range c.pkg.Info.Types {
		walk(t)
	}
	for _, s := range c.pkg.Info.Literals {
		walk(s)
	}
	for _, o := range c.pkg.Info.Uses {
		if f, ok := o.(*Func); ok && f.Sig != nil {
			walk(f.Sig)
		}
	}
	for _, inst := range c.instance {
		walk(inst)
		walk(under(inst))
	}
	return out
}

// genericInferred writes generic callees whose rows are inferred, each used
// three ways (round 12 M1): a concrete call, an explicit instantiation and a
// saved instantiated value (a capability-row parameter is implicit, so it
// takes a call and a saved function value instead). Callee i passes on a row parameter's failure,
// fails with an error type over its type parameter, or charges a
// capability-row parameter; every caller writes the row it must get.
func genericInferred(seed uint64) string {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e37))
	var b strings.Builder
	b.WriteString("type Bang {}\ntype Fault[T] { V T }\ntype Cap { M() }\n")
	// fail and work are function values below: ordinary functions, since
	// an extern is call-only (check-v1 stage E4, P3).
	b.WriteString("func fail() fails Bang { return Bang }\nfunc work() needs Cap { Cap.M() }\nfunc mark[T]() fails Fault[T] { try mark[T]() }\n")
	types := []string{"int", "string", "Bang", "[]int"}
	for i := range 6 {
		ty := types[rng.IntN(len(types))]
		switch rng.IntN(3) {
		case 0:
			fmt.Fprintf(&b, "func pass%d[E](f func() fails E) { return f() }\n", i)
			fmt.Fprintf(&b, "func C%d() fails Bang { return pass%d(fail) }\n", i, i)
			fmt.Fprintf(&b, "func X%d() fails Bang { return pass%d[Bang](fail) }\n", i, i)
			fmt.Fprintf(&b, "let saved%d = pass%d[Bang]\nfunc S%d() fails Bang { return saved%d(fail) }\n", i, i, i, i)
		case 1:
			fmt.Fprintf(&b, "func raise%d[T](x T) { return Fault{x} }\n", i)
			fmt.Fprintf(&b, "func C%d(x %s) fails Fault[%s] { return raise%d(x) }\n", i, ty, ty, i)
			fmt.Fprintf(&b, "func X%d(x %s) fails Fault[%s] { return raise%d[%s](x) }\n", i, ty, ty, i, ty)
			fmt.Fprintf(&b, "let saved%d = raise%d[%s]\nfunc S%d(x %s) fails Fault[%s] { return saved%d(x) }\n", i, i, ty, i, ty, ty, i)
		default:
			fmt.Fprintf(&b, "func run%d[R](f func() needs R) { f() }\n", i)
			fmt.Fprintf(&b, "func C%d() needs Cap { run%d(work) }\n", i, i)
			// An implicit capability-row parameter takes no explicit argument:
			// the saved form is a function value passed on.
			fmt.Fprintf(&b, "func S%d() needs Cap { let g = work; run%d(g) }\n", i, i)
		}
	}
	return decided(b.String())
}

// TestIdentityGenericInferred: the generic-callee family is accepted, and
// the interned ids agree with the structural oracle on every recorded type,
// the variable uses read with their substitutions.
func TestIdentityGenericInferred(t *testing.T) {
	pairs := 0
	for seed := range uint64(40) {
		src := genericInferred(seed)
		c, diags := checkFor(src)
		if c == nil || len(diags) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, diags, src)
		}
		if len(c.widened) > 0 {
			t.Errorf("seed %d: wider reporting rows for %v", seed, c.widened)
		}
		types := collectTypes(c)
		for i, x := range types {
			for _, y := range types[i:] {
				pairs++
				if want, have := structural(x, y), c.identical(x, y); want != have {
					t.Errorf("seed %d: %s and %s: structural %v, interned ids %v", seed, x, y, want, have)
				}
			}
		}
	}
	t.Logf("40 sources, %d pairs", pairs)
}
