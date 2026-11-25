package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// valueCopy is e, the IR of x, under the value rule's copy of x when the
// rule copies it (values.go, clone; F7 "value copy"), as the emitter's
// expr writes it (cloneValue): Go's maps.Clone or slices.Clone when no
// element holds a Map or slice, else a call of the type's generated copy
// function, which the lowerer declares once (cloneFunc). A type that
// holds no Map or slice is not copied.
func (b *irBuilder) valueCopy(x syntax.Expr, e *irExpr) *irExpr {
	if b.l.vals == nil || !b.l.vals.clone[x] {
		return e
	}
	return b.copyOf(b.fc.typeOf(x), x, e)
}

// copyOf is e, a value of t, copied as cloneValue copies it; x is the
// site its types are spelled at (an owned-parameter wrapper's copy,
// ir_own.go, as valueCopy's).
func (b *irBuilder) copyOf(t check.Type, x syntax.Node, e *irExpr) *irExpr {
	if !b.l.holdsValue(t) {
		return e
	}
	// The emitter's expr1: deep for Go (V8, vals.deep), else cloneValue,
	// deep unless the entry rule holds (cloneOf).
	// An owned-parameter wrapper's x is its declaration: no site of
	// vals.deep (I-FLIP: LY's entry rule x FORK's wrapper copy).
	deep := !entryRule()
	if xe, ok := x.(syntax.Expr); ok {
		deep = deep || b.l.vals.deep[xe]
	}
	return b.copyDeep(t, x, e, deep)
}

// copyDeep is e, a value of t, copied as cloneOf copies it: each Map's
// entries too when deep (cloneDeep, a Go result's copy, goResultCopy).
func (b *irBuilder) copyDeep(t check.Type, x syntax.Node, e *irExpr, deep bool) *irExpr {
	if !b.l.holdsValue(t) {
		return e
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		if _, ok := b.intMapOf(t); ok && (!deep && !b.l.entryWritten(t) || !b.l.holdsValue(u.Elem) || valueMutant == "shallow") {
			return tableCall(e.Pos, "Clone", e) // ir_intmap.go: the table's own copy
		}
		if !deep && !b.l.entryWritten(t) || !b.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			k, v := b.goType(u.Key, x), b.goType(u.Elem, x)
			m := "map[" + k + "]" + v
			if b.goType(t, x) == m {
				// The import is kept used at one fixed instance: the copied
				// type may name the body's type parameters, which package
				// scope does not.
				b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[map[string]bool]")
				return &irExpr{K: "pcall", Pos: e.Pos, T: "maps", Name: "Clone", V: m + ", " + k + ", " + v, Args: []*irExpr{e}}
			}
		}
	case *check.Slice:
		if !b.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			elem := b.goType(u.Elem, x)
			if b.goType(t, x) == "[]"+elem {
				b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clone[[]byte]")
				return &irExpr{K: "pcall", Pos: e.Pos, T: "slices", Name: "Clone", V: "[]" + elem + ", " + elem, Args: []*irExpr{e}}
			}
		}
	}
	b.goType(t, x)
	name, free := b.l.cloneFunc(t, x, deep)
	args := make([]string, len(free))
	for i, tp := range free {
		args[i] = b.goType(tp, x)
	}
	return &irExpr{K: "call", Pos: e.Pos, Name: name, T: strings.Join(args, ", "), Args: []*irExpr{e}}
}
