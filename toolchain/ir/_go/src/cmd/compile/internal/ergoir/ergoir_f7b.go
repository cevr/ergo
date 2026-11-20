// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: F7 lane B's statements. See ergoir.go.
//
// "mapget" is Go's comma-ok read of a map, v, ok := m[k] (Local v, Val
// ok, X the map, Y the key): the index expression's type is the tuple
// (V, bool), as types2 records a comma-ok form (recordCommaOkTypes), so
// the unified writer writes an N:1 assignment, which the reader types as
// OAS2MAPR.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// f7bStmt decodes s when it is one of lane B's forms.
func (c *ergoChecker) f7bStmt(s *ergoStmt) (syntax.Stmt, bool) {
	if s.K != "mapget" {
		return nil, false
	}
	pos := c.pos(s.Pos)
	if s.Val == nil || s.X == nil || s.Y == nil {
		c.errorf(s.Pos, "ergoir: mapget without its ok local, map or key")
		return nil, true
	}
	v, ok := c.local(s.Pos, s.Local), c.local(s.Pos, *s.Val)
	m, mt := c.expr(s.X)
	k, kt := c.expr(s.Y)
	if v == nil || ok == nil || m == nil || k == nil {
		return nil, true
	}
	mp, isMap := types2.CoreType(mt).(*types2.Map)
	if !isMap {
		c.errorf(s.Pos, "ergoir: mapget of %v", mt)
		return nil, true
	}
	if !c.assignable(s.Pos, "mapget key", kt, mp.Key()) || !c.assignable(s.Pos, "mapget value", mp.Elem(), v.Type()) || !c.assignable(s.Pos, "mapget ok", types2.Typ[types2.Bool], ok.Type()) {
		return nil, true
	}
	ix := &syntax.IndexExpr{X: m, Index: k}
	ix.SetPos(pos)
	ergoValue(ix, types2.NewTuple(types2.NewParam(pos, nil, "", mp.Elem()), types2.NewParam(pos, nil, "", types2.Typ[types2.Bool])), nil)
	lhs := &syntax.ListExpr{ElemList: []syntax.Expr{c.name(s.Pos, v, true), c.name(s.Pos, ok, true)}}
	lhs.SetPos(pos)
	a := &syntax.AssignStmt{Op: syntax.Def, Lhs: lhs, Rhs: ix}
	a.SetPos(pos)
	return a, true
}
