package lower

// IR builder: a slice expression (FLIP F7 GP, "expression
// *syntax.SliceExpr"). See ir.go.
//
// x[lo:hi] and x[lo:hi:max] of a slice are Go's own slice expression, as
// the emitter writes it (expr.go, sliceExpr): the IR's "slice" kind, X
// the operand and Args the indices, a missing low or high index nil
// (ergoir_slice.go). The operand and the indices run in source order
// (ir_order.go), and Go checks the bounds at the expression's position.
// The value rule's copy of the result is expr's, as for any value.
//
// x[lo:hi] of a string (or a named type over one) is the same kind, Go's
// substring, which the decoder takes (ergoir_slice.go; PORT-SLICE). The
// emitter's proved-bounds forms (rt.StringAt, rt.StringN) read the
// bounds facts its own walk gathers, which the IR has not: its substring
// is the checked one, as its string index is. An array, which Go slices
// only as a place and the decoder takes no operand of, keeps its reason.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sliceOf is the slice expression x of a slice or a string.
func (b *irBuilder) sliceOf(x *syntax.SliceExpr, p irPos) *irExpr {
	if e, ok := b.uncheckedCut(x, p); ok { // ir_bounds.go
		return e
	}
	switch t := underEqual(b.l.info.Types[x.X]).(type) {
	case *check.Slice:
	case *check.Basic:
		if t.Kind != check.String || x.Slice3 {
			irNo("expression %T", x)
		}
	default:
		irNo("expression %T", x)
	}
	idx := []syntax.Expr{x.Low, x.High}
	if x.Slice3 {
		idx = append(idx, x.Max)
	}
	list := []syntax.Expr{x.X}
	for _, e := range idx {
		if e != nil {
			list = append(list, e)
		}
	}
	vals := b.inOrder(list) // ir_order.go
	s := &irExpr{K: "slice", Pos: p, X: vals[0], Args: make([]*irExpr, len(idx))}
	k := 1
	for i, e := range idx {
		if e != nil {
			s.Args[i] = vals[k]
			k++
		}
	}
	return s
}
