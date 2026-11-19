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
// A string keeps its reason until its proved-bounds forms (rt.StringAt,
// rt.StringN) are built on the same kind (IR2's F4); an array, which Go
// slices only as a place, keeps it too.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sliceOf is the slice expression x of a slice.
func (b *irBuilder) sliceOf(x *syntax.SliceExpr, p irPos) *irExpr {
	if _, ok := underEqual(b.l.info.Types[x.X]).(*check.Slice); !ok {
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
