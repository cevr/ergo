package lower

// IR builder: a record literal's field value, converted into the field's
// type as Go assigns it (B-CONVFIELD). See ir.go.
//
// The value of a field goes in after the checker's implicit conversion
// (implicit, ir_union.go: a variant into its union, a machine value, an
// operations conversion), which expr applies. A value the checker records
// no conversion for and whose Go type is not the field's is a failure type
// put into a field of a row type: the row's Go type is error (D-C3), and
// Go converts the variant to it, which the IR spells "toiface" to error.
// Any other difference keeps the source body.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// intoField is e, the value of x, as the record field of type ft holds it.
func (b *irBuilder) intoField(x syntax.Expr, e *irExpr, ft check.Type) *irExpr {
	if ft == nil || e == nil {
		irNo("record key")
	}
	from := b.l.info.Types[x]
	if cv, ok := b.l.info.Implicit[x]; ok && cv.Kind != 0 {
		from = cv.To // expr converted it (implicit)
	}
	if from == nil {
		irNo("expression without a type")
	}
	gf, gt := b.goType(from, x), b.goType(ft, x)
	switch {
	case gf == gt:
		return e
	case b.rowHolds(ft, gf, x):
		return &irExpr{K: "toiface", Pos: e.Pos, T: gt, X: e} // into the row's error
	}
	irNo("record field of a converted value")
	return nil
}

// intoSlot is e, the value of the field named field of a record literal of
// type t, as the Go field takes it, as the emitter's intoSlot (rowslot.go):
// a field whose declared type is a type parameter bound to a Slot row of
// the enclosing function (Failed[F]{Error: err} in actor.ExitOf) has that
// Go type parameter as its Go type. A value already typed as the slot
// goes straight in; an erased row (Go error) is asserted to the slot.
func (b *irBuilder) intoSlot(td *typeDecl, t check.Type, field string, x syntax.Expr, e *irExpr) *irExpr {
	n, ok := t.(*check.Named)
	if !ok || n.Orig == nil || td.named == nil {
		return e
	}
	s, ok := td.named.Underlying().(*check.Struct)
	if !ok {
		return e
	}
	var p *check.TypeParam
	for _, f := range s.Fields {
		if f.Name == field {
			p, _ = f.Type.(*check.TypeParam)
		}
	}
	if p == nil {
		return e
	}
	for i, q := range n.Orig.TParams {
		if q != p || i >= len(n.TArgs) {
			continue
		}
		if tp := check.SlotRow(n.TArgs[i]); tp != nil && tp.Slot {
			name, ok := b.fc.params[tp]
			if !ok {
				return e
			}
			from := b.l.info.Types[x]
			if cv, ok := b.l.info.Implicit[x]; ok && cv.Kind != 0 {
				from = cv.To
			}
			if from != nil {
				switch b.goType(from, x) {
				case name:
					return e
				case "error":
					return &irExpr{K: "assert", Pos: e.Pos, T: name, X: e}
				}
			}
			irNo("record field of a Slot row from a value that is not an error")
		}
	}
	return e
}

// rowHolds reports whether t is a row of failure types (failureRow) one
// of whose variants has the Go type gf.
func (b *irBuilder) rowHolds(t check.Type, gf string, site syntax.Node) bool {
	if !failureRow(b.fc, t) {
		return false
	}
	for _, v := range t.(*check.Union).Variants {
		if b.goType(v, site) == gf {
			return true
		}
	}
	return false
}

// failureRow reports whether t is a row of failure types, whose Go type is
// error (D-C3): the one rule the IR admits a row by (admit, ir_types.go;
// FLIP F7 CS's bindType, ir_pattern.go, when it merges, reads it too).
func failureRow(fc *funcCtx, t check.Type) bool {
	u, ok := t.(*check.Union)
	return ok && fc.goType(u) == "error"
}
