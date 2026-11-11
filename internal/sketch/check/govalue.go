package check

// Go functions as values (wf10 defects D2; interop.md, Lowering 8).
// A direct function has no parameter or result conversion. Other Go
// functions stay call-only; IG6 owns Ergo callback conversions.

// GoDirectSig reports a function type whose every parameter and result
// has the Go type it maps from, unchanged: a basic type, a non-generic Go
// type of import go, go.Error, or as a parameter a borrow of a Go named
// type; at most one result; an error row of go.Error alone, or none; no
// type parameter.
func GoDirectSig(s *Signature) bool {
	if s == nil || len(s.TParams) > 0 || len(s.Results) > 1 {
		return false
	}
	for _, p := range s.Params {
		if !goDirect(p.Type, true) {
			return false
		}
	}
	if len(s.Results) == 1 && !goDirect(s.Results[0], false) {
		return false
	}
	for _, e := range s.Errors.Elems() {
		if n, ok := e.(*Named); !ok || !IsGoError(n) {
			return false
		}
	}
	return true
}

// goDirect reports a type whose Go form is the Go type it maps from. A
// borrow (*S of a Go named S) is direct only as a parameter: interop.md
// maps a pointer result to go.Ref or Option.
func goDirect(t Type, param bool) bool {
	switch t := t.(type) {
	case *Basic:
		return t.Kind > Invalid && t.Kind <= Duration
	case *Named:
		return IsGoError(t) || (isHostNamed(t) && len(t.TArgs) == 0)
	case *Pointer:
		n, ok := t.Elem.(*Named)
		return param && ok && isHostNamed(n) && len(n.TArgs) == 0
	}
	return false
}
