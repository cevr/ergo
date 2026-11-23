// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: calls. See ergoir.go.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoRangeTypes is the key and the element type of Go's range over a
// value of type t: a slice's index and element, a map's key and value, a
// string's byte offset and rune; one integer of the bound's type for
// an integer range; nil and nil for an unsupported type.
func ergoRangeTypes(t types2.Type) (key, elem types2.Type) {
	if ergoIsInteger(t) {
		return t, nil
	}
	switch u := types2.CoreType(t).(type) {
	case *types2.Slice:
		return types2.Typ[types2.Int], u.Elem()
	case *types2.Array:
		return types2.Typ[types2.Int], u.Elem() // FLIP F7: Go's range over an array
	case *types2.Map:
		return u.Key(), u.Elem()
	case *types2.Signature:
		// for v := range seq, seq a func(yield func(T) bool) (an Ergo Seq
		// loop): one variable; Stub's rangefunc.Rewrite rewrites the loop.
		if u.Params().Len() == 1 && u.Results().Len() == 0 {
			if y, ok := types2.CoreType(u.Params().At(0).Type()).(*types2.Signature); ok && y.Params().Len() == 1 {
				return y.Params().At(0).Type(), nil
			}
		}
	}
	if ergoIsString(t) {
		return types2.Typ[types2.Int], types2.Universe.Lookup("rune").Type()
	}
	return nil, nil
}

// call checks the arguments of a call of fun, whose signature is sig.
func (c *ergoChecker) call(x *ergoExpr, fun syntax.Expr, sig *types2.Signature) (syntax.Expr, types2.Type) {
	// A function may return several values, which only a tdef binds
	// (ergoir_tuple.go): a package call, or the call a tdef holds.
	// A variadic Go function takes its last argument spread, f(xs...),
	// when the IR's Op says "..." (FLIP F7 FORK-GOLIB): types2's last
	// parameter is then the slice.
	dots := x.Op == "..." && sig.Variadic()
	if sig.Variadic() != dots || sig.Params().Len() != len(x.Args) || !ergoSigOK(sig) && x.K != "pcall" && x != c.tdef {
		c.errorf(x.Pos, "ergoir: call of %s with %d arguments", x.Name, len(x.Args))
		return nil, nil
	}
	call := &syntax.CallExpr{Fun: fun, HasDots: dots}
	call.SetPos(c.pos(x.Pos))
	for i, a := range x.Args {
		e, t := c.expr(a)
		if e == nil || !c.assignable(a.Pos, "argument", t, sig.Params().At(i).Type()) {
			return nil, nil
		}
		call.ArgList = append(call.ArgList, e)
	}
	if sig.Results().Len() == 0 {
		tv := syntax.TypeAndValue{}
		tv.SetIsVoid()
		call.SetTypeInfo(tv)
		return call, nil
	}
	var t types2.Type = sig.Results()
	if sig.Results().Len() == 1 {
		t = sig.Results().At(0).Type()
	}
	// A fallible call's type is its (value, error) tuple, which only a
	// def2 or a return of the whole result list takes (ergoir_fail.go); a
	// package call's several results, which only a tdef takes.
	ergoValue(call, t, nil)
	return call, t
}
