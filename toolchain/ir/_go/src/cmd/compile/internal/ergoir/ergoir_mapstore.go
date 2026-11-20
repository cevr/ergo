// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// storeTo is the target of a "store": a Map's entry ("mapAt", FLIP F7
// LY-PLACE), or any other expression (c.expr), which the store then
// requires to be a place (ergoPlace). A "mapAt" is a store's target only:
// Go cannot take an entry's address, and no other IR reads it.
func (c *ergoChecker) storeTo(x *ergoExpr) (syntax.Expr, types2.Type) {
	if x.K != "mapAt" {
		return c.expr(x)
	}
	m, mt := c.expr(x.X)
	k, kt := c.expr(x.Y)
	if m == nil || k == nil {
		return nil, nil
	}
	u, ok := types2.CoreType(mt).(*types2.Map)
	if !ok {
		c.errorf(x.Pos, "ergoir: mapAt of %v, not a map", mt)
		return nil, nil
	}
	if !c.assignable(x.Pos, "mapAt key", kt, u.Key()) {
		return nil, nil
	}
	e := &syntax.IndexExpr{X: m, Index: k}
	e.SetPos(c.pos(x.Pos))
	tv := syntax.TypeAndValue{Type: u.Elem()}
	tv.SetIsValue()
	tv.SetAssignable() // a map index: assignable, never addressable
	e.SetTypeInfo(tv)
	return e, u.Elem()
}

// ergoMapEntry reports whether x is a Map's entry that storeTo built.
func ergoMapEntry(x syntax.Expr) bool {
	e, ok := x.(*syntax.IndexExpr)
	if !ok {
		return false
	}
	_, isMap := types2.CoreType(e.X.GetTypeInfo().Type).(*types2.Map)
	return isMap && e.GetTypeInfo().Assignable()
}
