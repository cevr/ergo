// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: unions and match. See ergoir.go.
//
// An Ergo union is a sealed Go interface or a flat struct of a tag and
// slots; the Ergo lowering decides which, and the IR spells it. "toiface"
// converts a variant to the sealed interface, as Go assigns it; "switch"
// is an expression switch over constants (a flat union's tag, or a
// match on a literal subject); a type switch is "tswitch"
// (ergoir_fail.go). A case marked Def is the default clause in its
// source place.

package ergoir

import (
	"go/constant"
	"go/token"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// defClause is the default clause k; dup reports an earlier default.
func (c *ergoChecker) defClause(k *ergoCase, dup bool) *syntax.CaseClause {
	if dup || len(k.Types) > 0 || len(k.Vals) > 0 {
		c.errorf(k.Pos, "ergoir: a second default clause, or a default with cases")
		return nil
	}
	cl := &syntax.CaseClause{Body: c.stmts(k.Body)}
	cl.SetPos(c.pos(k.Pos))
	return cl
}

// unionStmt decodes the statements of unions and match; ok is false for a
// kind it does not know.
func (c *ergoChecker) unionStmt(s *ergoStmt) (st syntax.Stmt, ok bool) {
	pos := c.pos(s.Pos)
	switch s.K {
	case "switch":
		x, t := c.expr(s.X)
		if x == nil {
			return nil, true
		}
		if ergoBasicInfo(t) == 0 {
			c.errorf(s.Pos, "ergoir: switch on %v, not a basic type", t)
			return nil, true
		}
		sw := &syntax.SwitchStmt{Tag: x, Rbrace: c.pos(s.End)}
		sw.SetPos(pos)
		var seen []constant.Value
		hasDef := false
		for _, k := range s.Cases {
			if k == nil {
				c.errorf(s.Pos, "ergoir: a missing case")
				return nil, true
			}
			if k.Def {
				cl := c.defClause(k, hasDef || s.Else != nil)
				if cl == nil {
					return nil, true
				}
				hasDef = true
				sw.Body = append(sw.Body, cl)
				continue
			}
			if len(k.Vals) == 0 || len(k.Types) > 0 {
				c.errorf(k.Pos, "ergoir: a switch case without its constants")
				return nil, true
			}
			var vals []syntax.Expr
			for _, v := range k.Vals {
				if v == nil || v.K != "const" {
					c.errorf(k.Pos, "ergoir: a switch case that is not a constant")
					return nil, true
				}
				e, et := c.expr(v)
				if e == nil || !c.assignable(v.Pos, "case", et, t) {
					return nil, true
				}
				cv := ergoConstant(et, v.V)
				for _, o := range seen {
					if constant.Compare(o, token.EQL, cv) {
						c.errorf(v.Pos, "ergoir: duplicate case %s", v.V)
						return nil, true
					}
				}
				seen = append(seen, cv)
				vals = append(vals, e)
			}
			cl := &syntax.CaseClause{Body: c.stmts(k.Body)}
			cl.SetPos(c.pos(k.Pos))
			if len(vals) == 1 {
				cl.Cases = vals[0]
			} else {
				l := &syntax.ListExpr{ElemList: vals}
				l.SetPos(c.pos(k.Pos))
				cl.Cases = l
			}
			sw.Body = append(sw.Body, cl)
		}
		if s.Else != nil {
			def := &syntax.CaseClause{Body: c.stmts(s.Else)}
			def.SetPos(c.pos(s.ElsePos))
			sw.Body = append(sw.Body, def)
		}
		return sw, true
	}
	return nil, false
}

// unionExpr decodes the expressions of unions; ok is false for a kind it
// does not know.
func (c *ergoChecker) unionExpr(x *ergoExpr) (e syntax.Expr, t types2.Type, ok bool) {
	pos := c.pos(x.Pos)
	switch x.K {
	case "toiface":
		// T(x): a variant x to the sealed interface T it implements, or
		// an interface value x to an interface T its method set
		// implements (an Ergo operation type lent as the prelude's
		// Reader or Writer, IFACE-CONV): Go's conversion, which the
		// compiler lowers to an itab lookup.
		t := c.typ(x.Pos, x.T)
		v, vt := c.expr(x.X)
		if t == nil || v == nil {
			return nil, nil, true
		}
		iface, isIface := t.Underlying().(*types2.Interface)
		if !isIface || vt == nil || !types2.Implements(vt, iface) {
			c.errorf(x.Pos, "ergoir: conversion of %v to %v", vt, t)
			return nil, nil, true
		}
		fun := c.typeExpr(x.Pos, t)
		if fun == nil {
			return nil, nil, true
		}
		call := &syntax.CallExpr{Fun: fun, ArgList: []syntax.Expr{v}}
		call.SetPos(pos)
		ergoValue(call, t, nil)
		return call, t, true
	}
	return nil, nil, false
}
