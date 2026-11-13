package lower

import "ergo.local/prototype/internal/sketch/check"

// Slot rows (CK-rowarg; types.md, Generics 6 and 8). An error row
// parameter that stands alone as a type argument of a generic type in its
// function's signature, F in `func Await[T, F](a Async[T, F]) T fails F`,
// is a Go type parameter constrained by error: Go then sees the value type
// of F's row where Ergo does, so `Async[string, NotFound]` reaches the
// function as itself, and `Async[string, NotFound | Denied]`, whose value
// type is a Go error, as `Async[string, error]`. Every other row parameter
// is erased, as before. A Slot row is inlined, specialised for a known
// record and given a known variant by none of those rules (hasSlot), which
// keeps its Go argument in one place: the call's type arguments (goArg).

// goParam reports a type parameter that is a Go type parameter: a type, or
// a Slot row.
func goParam(tp *check.TypeParam) bool {
	return tp.Kind == check.KindType || tp.Slot
}

// hasSlot reports a signature with a Slot row.
func hasSlot(sig *check.Signature) bool {
	for _, tp := range sig.TParams {
		if tp.Slot {
			return true
		}
	}
	return false
}

// slotType is the Go type argument a of a generic type's type parameter:
// a Slot row of the enclosing function names its Go parameter; anything
// else is its Go type (a row value of several error types is error).
func (tc *typeCtx) slotType(a check.Type) string {
	if tp := check.SlotRow(a); tp != nil {
		if name, ok := tc.params[tp]; ok {
			return name
		}
	}
	return tc.goType(a)
}

// intoSlot is val, the value of the field named field of a record literal
// of type t, as the Go field takes it. A field whose declared type is a
// type parameter bound to a Slot row of the enclosing function has that Go
// type parameter as its Go type, while a row value elsewhere is a Go
// error: the value is asserted to the parameter, which holds because the
// row value holds a value of the row's one type (or is an error when the
// parameter's argument is error). any(…) admits a value whose Go type is
// already the parameter, which Go cannot assert on directly. A row value
// nested deeper in a field's type ([]F) has no such conversion (a known
// gap of CK-rowarg; the Go build refuses it).
func (fc *funcCtx) intoSlot(td *typeDecl, t check.Type, field, val string) string {
	n, ok := t.(*check.Named)
	if !ok || n.Orig == nil || td.named == nil {
		return val
	}
	s, ok := td.named.Underlying().(*check.Struct)
	if !ok {
		return val
	}
	var p *check.TypeParam
	for _, f := range s.Fields {
		if f.Name == field {
			p, _ = f.Type.(*check.TypeParam)
		}
	}
	if p == nil {
		return val
	}
	for i, q := range n.Orig.TParams {
		if q != p || i >= len(n.TArgs) {
			continue
		}
		if tp := check.SlotRow(n.TArgs[i]); tp != nil && tp.Slot {
			if name, ok := fc.params[tp]; ok {
				return "any(" + val + ").(" + name + ")"
			}
		}
	}
	return val
}

// goArg is the Go type argument of a call for its parameter tp: a type's
// Go type, or for a Slot row the value type of the row bound to it, the
// one error type when the row holds exactly one, a Slot row of the caller
// when it holds that alone, and error otherwise.
func (tc *typeCtx) goArg(tp *check.TypeParam, a check.Type) string {
	if tp.Kind == check.KindType {
		return tc.goType(a)
	}
	ra, ok := a.(*check.RowArg)
	if !ok {
		return tc.slotType(a)
	}
	es := rowElems(ra.Row)
	if len(es) == 1 {
		switch e := es[0].(type) {
		case *check.Named:
			return tc.goType(e)
		case *check.TypeParam:
			if name, ok := tc.params[e]; ok {
				return name
			}
		}
	}
	return "error"
}
