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
	return b.copyAs(b.fc.typeOf(x), e, x)
}

// copyAs is a copy of e, a value of type t, as valueCopy writes it (an
// owned site's copy function, ir_refown.go, too).
func (b *irBuilder) copyAs(t check.Type, e *irExpr, x syntax.Node) *irExpr {
	if !b.l.holdsValue(t) {
		return e
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		// As cloneOf with deep false (batch-19's V8: a Map whose entries
		// are never written is copied shallow).
		if !b.l.entryWritten(t) || !b.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			k, v := b.goType(u.Key, x), b.goType(u.Elem, x)
			m := "map[" + k + "]" + v
			if b.goType(t, x) != m {
				// The decoder instantiates Clone at identical types only.
				irNo("value copy of a defined Map type")
			}
			// The import is kept used at one fixed instance: the copied
			// type may name the body's type parameters, which package
			// scope does not.
			b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[map[string]bool]")
			return &irExpr{K: "pcall", Pos: e.Pos, T: "maps", Name: "Clone", V: m + ", " + k + ", " + v, Args: []*irExpr{e}}
		}
	case *check.Slice:
		if !b.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			elem := b.goType(u.Elem, x)
			if b.goType(t, x) != "[]"+elem {
				irNo("value copy of a defined slice type")
			}
			b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clone[[]byte]")
			return &irExpr{K: "pcall", Pos: e.Pos, T: "slices", Name: "Clone", V: "[]" + elem + ", " + elem, Args: []*irExpr{e}}
		}
	}
	b.goType(t, x)
	name, free := b.l.cloneFunc(t, x, false) // cloneValue's copy, not cloneDeep's
	args := make([]string, len(free))
	for i, tp := range free {
		args[i] = b.goType(tp, x)
	}
	return &irExpr{K: "call", Pos: e.Pos, Name: name, T: strings.Join(args, ", "), Args: []*irExpr{e}}
}
