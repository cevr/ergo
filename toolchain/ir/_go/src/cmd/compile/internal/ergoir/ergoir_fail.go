// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: the failure ABI. See ergoir.go.
//
// A fallible Ergo function returns (T, error), or error when it has no
// value. "ret" is its success return (the error slot is nil); "fail"
// leaves with the zero value and an error; "def2" binds a fallible call's
// value and error; "tswitch" is a catch's type switch on an error, and
// "assert" reads the case's type out of it; "panic" (ergoir_stmt.go) is a
// total catch's default.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoCase is one clause of a "tswitch": its types, or none for the
// default clause.
type ergoCase struct {
	Pos   ergoPos     `json:"pos"`
	Types []string    `json:"types"`
	Body  []*ergoStmt `json:"body,omitempty"`
	// Vals are a "switch" case's constants (ergoir_union.go); Def marks
	// the default clause in its source place.
	Vals []*ergoExpr `json:"vals,omitempty"`
	Def  bool        `json:"def,omitempty"`
}

// ergoFallible reports a signature of the failure ABI: its last result is
// the universe's error, after at most one value.
func ergoFallible(sig *types2.Signature) bool {
	n := sig.Results().Len()
	return (n == 1 || n == 2) && types2.Identical(sig.Results().At(n-1).Type(), ergoError())
}

func ergoError() types2.Type { return types2.Universe.Lookup("error").Type() }

// ergoSigOK admits a signature: at most one result, or the failure ABI.
func ergoSigOK(sig *types2.Signature) bool {
	return sig.Results().Len() <= 1 || sig.Results().Len() == 2 && ergoFallible(sig)
}

// nilOf is the zero value of t, spelled as the untyped nil the writer
// encodes as a typed zero (exprZero).
func (c *ergoChecker) nilOf(p ergoPos, t types2.Type) syntax.Expr {
	switch t.Underlying().(type) {
	case *types2.Struct, *types2.Array:
		// The reader's zero of a struct or an array (ir.NewZero) is a
		// composite literal that was never typechecked, which later
		// passes reject; spell T{} instead.
		l := &syntax.CompositeLit{}
		l.SetPos(c.pos(p))
		tv := syntax.TypeAndValue{Type: t}
		tv.SetIsValue()
		l.SetTypeInfo(tv)
		return l
	}
	if tp, ok := t.(*types2.TypeParam); ok {
		return ergoNewZero(c.pos(p), tp, c.info) // ergoir_f7f.go
	}
	n := syntax.NewName(c.pos(p), "nil")
	c.info.Uses[n] = types2.Universe.Lookup("nil")
	tv := syntax.TypeAndValue{Type: t}
	tv.SetIsValue()
	tv.SetIsNil()
	n.SetTypeInfo(tv)
	return n
}

// typeNameExpr is the type spelled s used as an expression (a case or an
// assertion): a name of the universe or of the package, or a qualified
// name (typeExpr).
func (c *ergoChecker) typeNameExpr(p ergoPos, s string) (syntax.Expr, types2.Type) {
	t := c.typ(p, s)
	if t == nil {
		return nil, nil
	}
	x := c.typeExpr(p, t)
	if x == nil {
		return nil, nil
	}
	return x, t
}

// ret is a return under the signature: a fallible function's success
// value gets a nil error; a value of the whole result list (a call's
// tuple, or a unit function's error) passes as it is.
func (c *ergoChecker) ret(s *ergoStmt) syntax.Stmt {
	r := &syntax.ReturnStmt{}
	r.SetPos(c.pos(s.Pos))
	res := c.sig.Results()
	fallible := ergoFallible(c.sig)
	if s.X == nil {
		switch {
		case res.Len() == 0:
		case fallible && res.Len() == 1:
			r.Results = c.nilOf(s.Pos, ergoError())
		default:
			c.errorf(s.Pos, "ergoir: return without its result")
		}
		return r
	}
	x, t := c.expr(s.X)
	if x == nil {
		return nil
	}
	if fallible {
		switch {
		case res.Len() == 2 && types2.Identical(t, res):
			r.Results = x // return f(…): a fallible call's tuple
		case res.Len() == 1 && types2.Identical(t, ergoError()):
			r.Results = x // a unit function returns a fallible call's error
		case res.Len() == 2 && c.assignable(s.Pos, "return", t, res.At(0).Type()):
			l := &syntax.ListExpr{ElemList: []syntax.Expr{x, c.nilOf(s.Pos, ergoError())}}
			l.SetPos(c.pos(s.Pos))
			r.Results = l
		default:
			if res.Len() == 1 {
				c.errorf(s.Pos, "ergoir: return of %v from a function with no value", t)
			}
			return nil
		}
		return r
	}
	if res.Len() != 1 || !c.assignable(s.Pos, "return", t, res.At(0).Type()) {
		return nil
	}
	r.Results = x
	return r
}

// failStmt decodes the failure ABI's statements; ok is false for a kind
// it does not know.
func (c *ergoChecker) failStmt(s *ergoStmt) (st syntax.Stmt, ok bool) {
	pos := c.pos(s.Pos)
	switch s.K {
	case "fail":
		if !ergoFallible(c.sig) {
			c.errorf(s.Pos, "ergoir: fail in a function that cannot fail")
			return nil, true
		}
		x, t := c.expr(s.X)
		if x == nil {
			return nil, true
		}
		if !ergoIsErrorValue(t) {
			c.errorf(s.Pos, "ergoir: fail: %v is not an error", t)
			return nil, true
		}
		r := &syntax.ReturnStmt{}
		r.SetPos(pos)
		if res := c.sig.Results(); res.Len() == 2 {
			l := &syntax.ListExpr{ElemList: []syntax.Expr{c.nilOf(s.Pos, res.At(0).Type()), x}}
			l.SetPos(pos)
			r.Results = l
		} else {
			r.Results = x
		}
		return r, true
	case "def2":
		if s.Val == nil {
			c.errorf(s.Pos, "ergoir: def2 without its error local")
			return nil, true
		}
		v, e := c.local(s.Pos, s.Local), c.local(s.Pos, *s.Val)
		if s.X == nil || (s.X.K != "call" && s.X.K != "mcall" && s.X.K != "pcall" && s.X.K != "vcall") {
			c.errorf(s.Pos, "ergoir: def2 of a value that is not a call")
			return nil, true
		}
		x, t := c.expr(s.X)
		if v == nil || e == nil || x == nil {
			return nil, true
		}
		tup, isTuple := t.(*types2.Tuple)
		if !isTuple || tup.Len() != 2 || !types2.Identical(tup.At(0).Type(), v.Type()) || !types2.Identical(tup.At(1).Type(), ergoError()) || !types2.Identical(e.Type(), ergoError()) {
			c.errorf(s.Pos, "ergoir: def2: %v does not bind %v and error", t, v.Type())
			return nil, true
		}
		lhs := &syntax.ListExpr{ElemList: []syntax.Expr{c.name(s.Pos, v, true), c.name(s.Pos, e, true)}}
		lhs.SetPos(pos)
		a := &syntax.AssignStmt{Op: syntax.Def, Lhs: lhs, Rhs: x}
		a.SetPos(pos)
		return a, true
	case "tswitch":
		x, t := c.expr(s.X)
		if x == nil {
			return nil, true
		}
		iface, isIface := types2.CoreType(t).(*types2.Interface)
		if !isIface {
			c.errorf(s.Pos, "ergoir: type switch on %v, not an interface", t)
			return nil, true
		}
		guard := &syntax.TypeSwitchGuard{X: x}
		guard.SetPos(pos)
		sw := &syntax.SwitchStmt{Tag: guard, Rbrace: c.pos(s.End)}
		sw.SetPos(pos)
		seen := map[types2.Type]bool{}
		hasDef := false
		for _, k := range s.Cases {
			if k != nil && k.Def {
				// The default clause in its source place (ergoir_union.go).
				cl := c.defClause(k, hasDef || s.Else != nil)
				if cl == nil {
					return nil, true
				}
				hasDef = true
				sw.Body = append(sw.Body, cl)
				continue
			}
			if k == nil || len(k.Types) == 0 {
				c.errorf(s.Pos, "ergoir: a type switch case without a type")
				return nil, true
			}
			cl := &syntax.CaseClause{Body: c.stmts(k.Body)}
			cl.SetPos(c.pos(k.Pos))
			var types []syntax.Expr
			for _, ts := range k.Types {
				te, tt := c.typeNameExpr(k.Pos, ts)
				if te == nil {
					return nil, true
				}
				// Go's rule, as an assertion's: a concrete case implements
				// the error interface; an interface case (a union) is any.
				if !types2.IsInterface(tt) && !types2.Implements(tt, iface) || seen[tt] {
					c.errorf(k.Pos, "ergoir: case %v of a switch on %v", tt, t)
					return nil, true
				}
				seen[tt] = true
				types = append(types, te)
			}
			if len(types) == 1 {
				cl.Cases = types[0]
			} else {
				l := &syntax.ListExpr{ElemList: types}
				l.SetPos(c.pos(k.Pos))
				cl.Cases = l
			}
			sw.Body = append(sw.Body, cl)
		}
		if !hasDef {
			def := &syntax.CaseClause{Body: c.stmts(s.Else)}
			def.SetPos(c.pos(s.ElsePos))
			sw.Body = append(sw.Body, def)
		}
		return sw, true
	}
	return nil, false
}

// failExpr decodes the failure ABI's expressions; ok is false for a kind
// it does not know.
func (c *ergoChecker) failExpr(x *ergoExpr) (e syntax.Expr, t types2.Type, ok bool) {
	pos := c.pos(x.Pos)
	switch x.K {
	case "nil":
		t := c.typ(x.Pos, x.T)
		if t == nil {
			return nil, nil, true
		}
		if !types2.IsInterface(t) {
			c.errorf(x.Pos, "ergoir: nil of %v", t)
			return nil, nil, true
		}
		return c.nilOf(x.Pos, t), t, true
	case "zero":
		// The zero value of any type: a catch's result before its arms.
		t := c.typ(x.Pos, x.T)
		if t == nil {
			return nil, nil, true
		}
		return c.nilOf(x.Pos, t), t, true
	case "assert":
		v, vt := c.expr(x.X)
		if v == nil {
			return nil, nil, true
		}
		te, tt := c.typeNameExpr(x.Pos, x.T)
		if te == nil {
			return nil, nil, true
		}
		iface, isIface := types2.CoreType(vt).(*types2.Interface)
		// Go's rule: a concrete type implements the operand's interface;
		// an interface (a row's error into its union, IRG3b; a union of
		// another package, IRG4) is checked at run time, as Go's x.(I).
		if !isIface || !types2.IsInterface(tt) && !types2.Implements(tt, iface) {
			c.errorf(x.Pos, "ergoir: assertion of %v to %v", vt, tt)
			return nil, nil, true
		}
		a := &syntax.AssertExpr{X: v, Type: te}
		a.SetPos(pos)
		ergoValue(a, tt, nil)
		return a, tt, true
	}
	return nil, nil, false
}

// ergoIsErrorValue reports a type that implements error.
func ergoIsErrorValue(t types2.Type) bool {
	if _, isTuple := t.(*types2.Tuple); isTuple || t == nil {
		return false
	}
	return types2.Implements(t, ergoError().Underlying().(*types2.Interface))
}
