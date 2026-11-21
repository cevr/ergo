package lower

// Row gaps (NARROW-GO; types.md, Generics 1). A declaration's error row
// parameter that is not a Slot (rowslot.go) is erased: in the declaration's
// Go type it is Go's error. The checker gives an instance's one-member row
// argument its member's record (SM WEB1, check/handlers.go rowValue):
// Classed[Boom].Of, declared func(e E) Fate, is func(e Boom) Fate there,
// whose Go type is func(*Env, Boom) while the Go field is func(*Env, error).
// So where an instance's place meets a value, the Go types can differ by
// exactly that row. The lowering decides it from the Go types at the place
// (rowGapOf), one helper for every place, and adapts the value:
//
//   - a write, a value of the instance's type put into the declaration's
//     place: an argument of a declared function, a field of a record or
//     variant literal, a field assignment (rowWriteDecl). A record goes
//     into Go's error as Go assigns it (the IR spells the conversion); a
//     function is wrapped in one that asserts each erased parameter to the
//     record the function takes (rowFunc).
//   - a read, the declaration's place read as the instance's type: a
//     field of an instance, the result of a declared function
//     (rowReadDecl). A record is asserted out of Go's error; a function is
//     wrapped in one that takes the records and passes them on as errors.
//     A field that is called (c.Of(e)) is not wrapped: its operands are
//     writes of the field's declared parameters instead.
//
// A return needs neither: a body's result type is its own declaration's,
// with no instance put in. A row of several error types is a Go error on
// both sides (a function of one of them goes in by ConvNarrow, narrowFunc),
// so the gap and ConvNarrow never both adapt one value. A row nested
// deeper ([]E, Option[E], a result E) is not adapted: the follow-up
// ROW-SLOT-TYPES gives a one-member row argument its member's Go type in
// the declaration, which closes every gap at once.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// rowGap is how a place's Go type differs from a value's by an erased row.
type rowGap struct {
	whole  bool  // the place is the row: Go's error, the value a record
	params []int // the place is a function whose parameters at these are the row
}

func (g rowGap) none() bool { return !g.whole && len(g.params) == 0 }

// rowGapOf is the gap between decl, a place's declared type (a
// declaration's own, its error row parameters erased), and have, the type
// the checker gives the other side of the place at an instance, decided by
// their Go types (goType).
func rowGapOf(goType func(check.Type) string, decl, have check.Type) rowGap {
	if decl == nil || have == nil {
		return rowGap{}
	}
	if holdsRowParam(decl) {
		return rowGap{whole: goType(have) != "error"}
	}
	// A value of a named function type has the declaration's Go type,
	// whose parameters are erased already; a function's own type is the
	// signature.
	ds, ok := underlyingOf(decl).(*check.Signature)
	hs, ok2 := have.(*check.Signature)
	if !ok || !ok2 || len(ds.TParams) > 0 || len(ds.Params) != len(hs.Params) {
		return rowGap{}
	}
	var g rowGap
	for i := range ds.Params {
		if holdsRowParam(ds.Params[i].Type) && goType(hs.Params[i].Type) != "error" {
			g.params = append(g.params, i)
		}
	}
	return g
}

// mentionsRowParam reports a declared type that a gap can be found at: a
// row parameter, or a function with one as a parameter.
func mentionsRowParam(t check.Type) bool {
	if holdsRowParam(t) {
		return true
	}
	s, ok := underlyingOf(t).(*check.Signature)
	if !ok {
		return false
	}
	for _, p := range s.Params {
		if holdsRowParam(p.Type) {
			return true
		}
	}
	return false
}

// erasedSig is s with its parameters at params of Go's error: the Go type
// of the declaration's place that s, an instance's function, fills.
func erasedSig(s *check.Signature, params []int) *check.Signature {
	e := *s
	e.Params = append([]*check.Var(nil), s.Params...)
	for _, i := range params {
		v := *e.Params[i]
		v.Type = &check.Union{Row: &check.Row{}} // a row value: Go's error (goType)
		e.Params[i] = &v
	}
	return &e
}

// instField is the declared type of the field x selects, when x's operand
// is an instance of a generic record or variant (through a pointer too).
func (l *lowerer) instField(x *syntax.SelectorExpr) check.Type {
	t := l.info.Types[x.X]
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*check.Named)
	if !ok || n.Orig == nil {
		return nil
	}
	return originField(n, x.Sel.Name)
}

// originField is the declared type of n's field name, from n's origin.
func originField(n *check.Named, name string) check.Type {
	s, ok := n.Orig.Underlying().(*check.Struct)
	if !ok {
		return nil
	}
	for _, f := range s.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return nil
}

// rowPlaces are the places of one package's Info (findRowPlaces): the
// declared type of each write place, and the fields that are called.
type rowPlaces struct {
	writes  map[syntax.Expr]check.Type
	callees map[*syntax.SelectorExpr]bool
}

// places are the row places of the Info the lowering reads now (l.info,
// which each package's body sets).
func (l *lowerer) places() *rowPlaces {
	if p := l.rowPlaces[l.info]; p != nil {
		return p
	}
	if l.rowPlaces == nil {
		l.rowPlaces = map[*check.Info]*rowPlaces{}
	}
	p := l.findRowPlaces()
	l.rowPlaces[l.info] = p
	return p
}

// rowWriteDecl is the declared type of the place x goes into when a gap
// can be found there, else nil. The places are found once, from every
// call, record literal and assignment the checker saw, and are the values
// without their parentheses: a parenthesized x is nil, so that a value is
// adapted once, at its innermost expression.
func (l *lowerer) rowWriteDecl(x syntax.Expr) check.Type {
	return l.places().writes[x]
}

// rowReadDecl is the declared type of the place x reads when a gap can be
// found there, else nil: a field of an instance that is not called, or a
// declared function's result.
func (l *lowerer) rowReadDecl(x syntax.Expr) check.Type {
	callees := l.places().callees
	var decl check.Type
	switch y := x.(type) { // a parenthesized read is its operand's
	case *syntax.SelectorExpr:
		if callees[y] {
			return nil
		}
		if _, member := l.info.Uses[y.Sel].(*check.Var); member || l.info.Uses[y.Sel] == nil {
			decl = l.instField(y)
		}
	case *syntax.CallExpr:
		if c := l.info.Calls[y]; c != nil && c.Func != nil && c.Func.Sig != nil && len(c.Func.Sig.Results) == 1 {
			decl = c.Func.Sig.Results[0]
		}
	}
	if decl == nil || !mentionsRowParam(decl) {
		return nil
	}
	return decl
}

// findRowPlaces records each write place a gap can be found at: the
// operands of every call of a declared function (its receiver first, as
// its signature lists it) and of every call of an instance's field, the
// fields of every record or variant literal of an instance, and the value
// of every assignment to an instance's field.
func (l *lowerer) findRowPlaces() *rowPlaces {
	p := &rowPlaces{writes: map[syntax.Expr]check.Type{}, callees: map[*syntax.SelectorExpr]bool{}}
	note := func(x syntax.Expr, decl check.Type) {
		if x != nil && decl != nil && mentionsRowParam(decl) {
			p.writes[unparen(x)] = decl
		}
	}
	for x, c := range l.info.Calls {
		var params []*check.Var
		args := x.Args
		switch {
		case c.Func != nil && c.Func.Sig != nil:
			params = c.Func.Sig.Params
			if c.Recv != nil {
				args = append([]syntax.Expr{c.Recv}, args...)
			}
		case c.Func == nil && c.Op == nil:
			// A field of an instance called as a function value.
			sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
			if !ok {
				continue
			}
			s, ok := underlyingOf(l.instField(sel)).(*check.Signature)
			if !ok || !mentionsRowParam(s) {
				continue
			}
			p.callees[sel] = true
			params = s.Params
		}
		for i, a := range args {
			if i < len(params) {
				note(a, params[i].Type)
			}
		}
	}
	for x, t := range l.info.Types {
		lit, ok := x.(*syntax.CompositeLit)
		if !ok {
			continue
		}
		if p, ok := t.(*check.Pointer); ok {
			t = p.Elem
		}
		n, ok := t.(*check.Named)
		if !ok || n.Orig == nil {
			continue
		}
		s, ok := n.Orig.Underlying().(*check.Struct)
		if !ok {
			continue
		}
		for i, e := range lit.Elts {
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				if k, ok := kv.Key.(*syntax.Ident); ok {
					note(kv.Value, originField(n, k.Name))
				}
				continue
			}
			if lit.Base == nil && i < len(s.Fields) {
				note(e, s.Fields[i].Type)
			}
		}
	}
	for _, o := range l.info.Defs {
		f, ok := o.(*check.Func)
		if !ok || f.Decl == nil || f.Decl.Body == nil {
			continue
		}
		syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
			if a, ok := n.(*syntax.AssignStmt); ok && len(a.Lhs) == len(a.Rhs) {
				for i, lhs := range a.Lhs {
					if sel, ok := unparen(lhs).(*syntax.SelectorExpr); ok {
						note(a.Rhs[i], l.instField(sel))
					}
				}
			}
			return true
		})
	}
	return p
}

// haveOf is the type of x's value where it goes into a place: after its
// implicit conversion.
func (l *lowerer) haveOf(x syntax.Expr) check.Type {
	if cv, ok := l.info.Implicit[x]; ok && cv.Kind != 0 && cv.To != nil {
		return cv.To
	}
	if p, ok := x.(*syntax.ParenExpr); ok {
		return l.haveOf(p.X)
	}
	return l.info.Types[x]
}

// rowWrite is val, the value of x (its parentheses too: the emitter
// adapts a value once, where it goes in), as the place x goes into holds
// it.
func (fc *funcCtx) rowWrite(x syntax.Expr, val string) string {
	decl := fc.l.rowWriteDecl(unparen(x))
	if decl == nil {
		return val
	}
	have := fc.l.haveOf(x)
	g := rowGapOf(fc.goType, decl, have)
	if len(g.params) == 0 {
		return val // a record goes into Go's error as Go assigns it
	}
	s := have.(*check.Signature)
	return fc.rowFunc(s, erasedSig(s, g.params), fc.funcType(s), fc.funcType(erasedSig(s, g.params)), g.params, val, x)
}

// rowRead is val, the value x reads from a declaration's place, as x's
// type holds it.
func (fc *funcCtx) rowRead(x syntax.Expr, val string) string {
	decl := fc.l.rowReadDecl(x)
	if decl == nil {
		return val
	}
	have := fc.typeOf(x)
	g := rowGapOf(fc.goType, decl, have)
	switch {
	case g.whole:
		return val + ".(" + fc.goType(have) + ")"
	case len(g.params) > 0:
		s := have.(*check.Signature)
		return fc.rowFunc(erasedSig(s, g.params), s, fc.funcType(erasedSig(s, g.params)), fc.funcType(s), nil, val, x)
	}
	return val
}

// rowFunc is val, a function value of from (Go type fromGo), as one of to
// (Go type toGo), which has the
// same parameters but for Go's error where from takes a record or the
// reverse: a function of the uniform shape (funcType) that asserts its
// parameters at asserts to from's, passes the others as they are (Go
// assigns a record to its error) and calls val, evaluated once, where the
// value is made. narrowFunc (ConvNarrow) and the row gaps (rowWrite,
// rowRead) build it.
func (fc *funcCtx) rowFunc(from, to *check.Signature, fromGo, toGo string, asserts []int, val string, site syntax.Node) string {
	if len(from.Params) != len(to.Params) {
		ice(site, "a function of %d parameters as one of %d", len(from.Params), len(to.Params))
	}
	assert := map[int]bool{}
	for _, i := range asserts {
		assert[i] = true
	}
	f := fc.scope.alloc("f")
	inner := fc.envClosure(callerEnv, "e", site, func(env string) (params []string, results, body string) {
		args := []string{env}
		for i, p := range to.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			arg := name
			if assert[i] {
				arg = name + ".(" + fc.goType(from.Params[i].Type) + ")"
			}
			args = append(args, arg)
		}
		var result check.Type
		if len(to.Results) == 1 {
			result = to.Results[0]
		}
		return params, fc.valueResults(result), strings.Repeat("\t", fc.depth+1) + "return " + f + "(" + strings.Join(args, ", ") + ")\n"
	})
	return "func(" + f + " " + fromGo + ") " + toGo + " { return " + inner + " }(" + val + ")"
}
