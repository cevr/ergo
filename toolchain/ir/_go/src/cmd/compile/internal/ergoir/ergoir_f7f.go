// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: deferred calls (FLIP F7, owned values). See ergoir.go.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoNewZero is *new(T), the zero value of the type parameter T as Go
// source spells it. A nil the writer encodes as T's zero (exprZero) would
// reach the reader as ir.NewZero of T's shape, which for a struct or an
// array is a composite literal that misses typecheck (a fallible generic
// body's failure: arena's Free on lru's node).
func ergoNewZero(pos syntax.Pos, tp *types2.TypeParam, info *types2.Info) syntax.Expr {
	tn := syntax.NewName(pos, tp.Obj().Name())
	info.Uses[tn] = tp.Obj()
	ttv := syntax.TypeAndValue{Type: tp}
	ttv.SetIsType()
	tn.SetTypeInfo(ttv)
	ptr := types2.NewPointer(tp)
	fn := syntax.NewName(pos, "new")
	info.Uses[fn] = types2.Universe.Lookup("new")
	ftv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil,
		types2.NewTuple(types2.NewParam(pos, nil, "", tp)),
		types2.NewTuple(types2.NewParam(pos, nil, "", ptr)), false)}
	ftv.SetIsBuiltin()
	fn.SetTypeInfo(ftv)
	call := &syntax.CallExpr{Fun: fn, ArgList: []syntax.Expr{tn}}
	call.SetPos(pos)
	ergoValue(call, ptr, nil)
	x := &syntax.Operation{Op: syntax.Mul, X: call}
	x.SetPos(pos)
	ergoValue(x, tp, nil)
	return x
}

// deferStmt is defer X, X a call of a function of the package with no
// result: a frame's release (the Ergo lowering's static frame, whose
// deferred function releases the values the frame still owns and reads
// the frame's panic with recover). Go runs it on every exit of the
// function, a panic's included.
//
// X may also be an "mcall" with no result: a frame scope's End, deferred
// directly so that its recover sees the frame's panic (runtime/v1
// Scope.End).
func (c *ergoChecker) deferStmt(s *ergoStmt, pos syntax.Pos) syntax.Stmt {
	if s.X == nil || s.X.K != "call" && s.X.K != "mcall" {
		c.errorf(s.Pos, "ergoir: a deferred statement is a call of a function of the package or a method")
		return nil
	}
	if len(c.loops) > 0 {
		// Go defers a call in a loop once per iteration; the lowering
		// defers a frame's release once, at the frame's start.
		c.errorf(s.Pos, "ergoir: defer inside a loop")
		return nil
	}
	x, t := c.expr(s.X)
	if x == nil {
		return nil
	}
	if t != nil {
		// A call with no result has no type (call).
		c.errorf(s.Pos, "ergoir: a deferred call has a result of type %v", t)
		return nil
	}
	call, ok := x.(*syntax.CallExpr)
	if !ok {
		c.errorf(s.Pos, "ergoir: a deferred statement is a call")
		return nil
	}
	st := &syntax.CallStmt{Tok: syntax.Defer, Call: call}
	st.SetPos(pos)
	return st
}

// recoverExpr is "recover": Go's builtin recover(), of type any. Only a
// function a frame defers directly reads its frame's panic with it (a
// static frame's release, FLIP F7 RELEASE-IR).
func (c *ergoChecker) recoverExpr(x *ergoExpr) (syntax.Expr, types2.Type) {
	pos := c.pos(x.Pos)
	any := types2.Universe.Lookup("any").Type()
	fun := syntax.NewName(pos, "recover")
	c.info.Uses[fun] = types2.Universe.Lookup("recover")
	tv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil, nil,
		types2.NewTuple(types2.NewParam(pos, nil, "", any)), false)}
	tv.SetIsBuiltin()
	fun.SetTypeInfo(tv)
	call := &syntax.CallExpr{Fun: fun}
	call.SetPos(pos)
	ergoValue(call, any, nil)
	return call, any
}

// resultExpr is "result" I: the function's named result I, which a
// frame's deferred End settles (End(&err): the frame's named error
// result, a Go function's only way to set its result after return).
func (c *ergoChecker) resultExpr(x *ergoExpr) (syntax.Expr, types2.Type) {
	if x.I < 0 || x.I >= c.sig.Results().Len() {
		c.errorf(x.Pos, "ergoir: result %d out of range", x.I)
		return nil, nil
	}
	v := c.sig.Results().At(x.I)
	if v.Name() == "" || v.Name() == "_" {
		c.errorf(x.Pos, "ergoir: result %d has no name", x.I)
		return nil, nil
	}
	return c.name(x.Pos, v, false), v.Type()
}
