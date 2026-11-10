package check

import "ergo.local/prototype/internal/sketch/syntax"

// Go functions as values, and Ergo functions as Go callbacks (wf10
// defects D2; interop.md, Lowering 7). The lowering crosses a function
// between Go's form and Ergo's uniform shape with no conversion of a
// parameter or a result: a value whose Go type is the one its Ergo type
// maps from. So a Go function of import go is a value, and an Ergo
// function value is passed to a Go function parameter, only when the
// function type is direct (GoDirectSig). A slice or a Map would need the
// value rule's copy at the edge, an Option or a go.Ref a nil check; those
// stay IG6's, and are S108 (a value) or S109 (a callback) here, never Go's
// build error.

// GoDirectSig reports a function type whose every parameter and result
// has the Go type it maps from, unchanged: a basic type, a non-generic Go
// type of import go, go.Error, or as a parameter a borrow of a Go named
// type; at most one result; an error row of go.Error alone, or none; no
// type parameter.
func GoDirectSig(s *Signature) bool {
	if s == nil || len(s.TParams) > 0 || len(s.Results) > 1 || s.ResultIsTask() {
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

// goCallbacks reports, at its argument, each function parameter of the Go
// function sig of import go whose type the lowering cannot adapt (S109),
// so the call never reaches Go's build with an Ergo function of the wrong
// shape.
func (c *checker) goCallbacks(x *syntax.CallExpr, sig *Signature) {
	for i, p := range sig.Params {
		cb, ok := p.Type.(*Signature)
		if !ok || GoDirectSig(cb) {
			continue
		}
		var at syntax.Node = x
		if i < len(x.Args) {
			at = x.Args[i]
		}
		c.errorf(at, CodeOutside, "a Go function parameter of type %s takes or gives a value that needs a conversion at the Go edge (a slice, a Map, an Option, a go.Ref or several results); IG6 lowers it (interop.md, Lowering 7)", p.Type)
	}
}
