// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: slice expressions (FLIP F7, kind "slice"). See ergoir.go.
//
// x[lo:hi] and x[lo:hi:max] of a slice, and x[lo:hi] of a string (or a
// named type over one): X is the operand, Args the indices, two or three,
// a missing low or high index null. Go's own slice expression, so gc
// lowers it to OSLICE or OSLICESTR and checks its bounds at the
// expression's position. An array place (ergoPlace) is an operand too,
// and the result is a slice of its element (OSLICEARR); Go slices an
// addressable array only, so an array value that is no place is refused.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// slice checks the slice expression x.
func (c *ergoChecker) slice(x *ergoExpr) (syntax.Expr, types2.Type) {
	xs, xt := c.expr(x.X)
	if xs == nil {
		return nil, nil
	}
	n := len(x.Args)
	if n != 2 && n != 3 || n == 3 && (x.Args[1] == nil || x.Args[2] == nil) {
		c.errorf(x.Pos, "ergoir: slice with %d indices", n)
		return nil, nil
	}
	rt := xt
	switch ct := types2.CoreType(xt).(type) {
	case *types2.Slice:
	case *types2.Array:
		if !ergoPlace(xs) {
			c.errorf(x.Pos, "ergoir: slice of an array that is no place")
			return nil, nil
		}
		rt = types2.NewSlice(ct.Elem())
	default:
		if !ergoIsString(xt) || n == 3 {
			c.errorf(x.Pos, "ergoir: slice of %v", xt)
			return nil, nil
		}
	}
	e := &syntax.SliceExpr{X: xs, Full: n == 3}
	for i, a := range x.Args {
		if a == nil {
			continue
		}
		v, vt := c.expr(a)
		if v == nil {
			return nil, nil
		}
		if !ergoIsInteger(vt) { // any integer type, as an index (red #31)
			c.errorf(a.Pos, "ergoir: slice index of type %v", vt)
			return nil, nil
		}
		e.Index[i] = v
	}
	e.SetPos(c.pos(x.Pos))
	ergoValue(e, rt, nil)
	return e, rt
}
