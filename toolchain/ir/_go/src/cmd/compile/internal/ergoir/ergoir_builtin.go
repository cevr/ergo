// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: calls of Go's builtins. See ergoir.go.

package ergoir

import (
	"go/constant"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// builtinExpr checks the IR kind "builtin", a call of Go's builtin
// x.Name, and writes it as Go's own call of the builtin, with the
// signature types2 records for it (types2/builtins.go): the reader then
// gives Go's ODELETE, OCOPY, OCLEAR, OMIN, OMAX, OCAP and OMAKESLICE, as
// Go source does. append here is append(xs, ys...) of a slice or of a
// string's bytes; append of one element is the kind "append". make is a
// slice's, its type in x.T and its sizes in x.Args. A call with no
// result (delete, clear) is void, and only an expression statement takes
// it.
func (c *ergoChecker) builtinExpr(x *ergoExpr) (syntax.Expr, types2.Type) {
	pos := c.pos(x.Pos)
	var args []syntax.Expr
	var ts []types2.Type
	for _, a := range x.Args {
		e, t := c.expr(a)
		if e == nil {
			return nil, nil
		}
		if t == nil {
			c.errorf(a.Pos, "ergoir: %s of a call with no result", x.Name)
			return nil, nil
		}
		args, ts = append(args, e), append(ts, t)
	}
	arity := map[string]int{"append": 2, "cap": 1, "clear": 1, "copy": 2, "delete": 2}
	if n, ok := arity[x.Name]; ok && len(args) != n || !ok && len(args) == 0 {
		c.errorf(x.Pos, "ergoir: %s of %d operands", x.Name, len(args))
		return nil, nil
	}
	var res types2.Type
	params := ts
	variadic, dots := false, false
	switch x.Name {
	case "append":
		// append(xs, ys...) of a slice of xs's element type (APPEND-SPREAD),
		// with the signature types2 records, (S, []E), or of a string's
		// bytes, (S, string).
		sl, ok := types2.CoreType(ts[0]).(*types2.Slice)
		ys, spread := types2.CoreType(ts[1]).(*types2.Slice)
		switch {
		case ok && spread && types2.Identical(sl.Elem(), ys.Elem()):
			params = []types2.Type{ts[0], types2.NewSlice(sl.Elem())}
		case ok && types2.Identical(sl.Elem(), types2.Typ[types2.Byte]) && ergoIsString(ts[1]):
		default:
			c.errorf(x.Pos, "ergoir: append of %v to %v", ts[1], ts[0])
			return nil, nil
		}
		res, variadic, dots = ts[0], true, true
	case "make":
		// make([]T, n) and make([]T, n, c) (IR-MAKE), checked as types2
		// checks a slice's make: x.T the slice type, the sizes of any
		// integer type, a constant one not negative and a constant n not
		// above a constant c. Go's escape analysis keeps a small make of a
		// constant size on the stack, as it keeps hand Go's.
		t := c.typ(x.Pos, x.T)
		if t == nil {
			return nil, nil
		}
		if _, ok := types2.CoreType(t).(*types2.Slice); !ok || len(args) > 2 {
			c.errorf(x.Pos, "ergoir: make of %v with %d sizes", t, len(args))
			return nil, nil
		}
		var sizes []int64
		for i, a := range x.Args {
			if !ergoIsInteger(ts[i]) {
				c.errorf(a.Pos, "ergoir: make size of type %v", ts[i])
				return nil, nil
			}
			if v := args[i].GetTypeInfo().Value; v != nil {
				n, exact := constant.Int64Val(constant.ToInt(v))
				if !exact || n < 0 {
					c.errorf(a.Pos, "ergoir: make size %v", v)
					return nil, nil
				}
				sizes = append(sizes, n)
			}
		}
		if len(sizes) == 2 && sizes[0] > sizes[1] {
			c.errorf(x.Pos, "ergoir: make length %d above capacity %d", sizes[0], sizes[1])
			return nil, nil
		}
		tn := syntax.NewName(pos, types2.TypeString(t, nil))
		ttv := syntax.TypeAndValue{Type: t}
		ttv.SetIsType()
		tn.SetTypeInfo(ttv)
		args = append([]syntax.Expr{tn}, args...)
		params = append([]types2.Type{t}, ts...)
		res = t
	case "cap":
		if _, ok := types2.CoreType(ts[0]).(*types2.Slice); !ok {
			c.errorf(x.Pos, "ergoir: cap of %v", ts[0])
			return nil, nil
		}
		res = types2.Typ[types2.Int]
	case "clear":
		switch types2.CoreType(ts[0]).(type) {
		case *types2.Slice, *types2.Map:
		default:
			c.errorf(x.Pos, "ergoir: clear of %v", ts[0])
			return nil, nil
		}
	case "copy":
		dst, ok := types2.CoreType(ts[0]).(*types2.Slice)
		src, ok2 := types2.CoreType(ts[1]).(*types2.Slice)
		if !ok || !ok2 || !types2.Identical(dst.Elem(), src.Elem()) {
			c.errorf(x.Pos, "ergoir: copy of %v to %v", ts[1], ts[0])
			return nil, nil
		}
		res = types2.Typ[types2.Int]
	case "delete":
		m, ok := types2.CoreType(ts[0]).(*types2.Map)
		if !ok || !c.assignable(x.Args[1].Pos, "delete key", ts[1], m.Key()) {
			if !ok {
				c.errorf(x.Pos, "ergoir: delete from %v", ts[0])
			}
			return nil, nil
		}
		params = []types2.Type{ts[0], m.Key()}
	case "min", "max":
		b, ok := types2.CoreType(ts[0]).(*types2.Basic)
		if !ok || b.Info()&types2.IsOrdered == 0 {
			c.errorf(x.Pos, "ergoir: %s of %v", x.Name, ts[0])
			return nil, nil
		}
		for i, t := range ts[1:] {
			if !types2.Identical(t, ts[0]) {
				c.errorf(x.Args[i+1].Pos, "ergoir: %s of %v and %v", x.Name, ts[0], t)
				return nil, nil
			}
		}
		res = ts[0]
	default:
		c.errorf(x.Pos, "ergoir: the builtin %q", x.Name)
		return nil, nil
	}
	var in []*types2.Var
	for _, t := range params {
		in = append(in, types2.NewParam(pos, nil, "", t))
	}
	var out *types2.Tuple
	if res != nil {
		out = types2.NewTuple(types2.NewParam(pos, nil, "", res))
	}
	fun := syntax.NewName(pos, x.Name)
	c.info.Uses[fun] = types2.Universe.Lookup(x.Name)
	tv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil, types2.NewTuple(in...), out, variadic)}
	tv.SetIsBuiltin()
	fun.SetTypeInfo(tv)
	call := &syntax.CallExpr{Fun: fun, ArgList: args, HasDots: dots}
	call.SetPos(pos)
	if res == nil {
		void := syntax.TypeAndValue{}
		void.SetIsVoid()
		call.SetTypeInfo(void)
		return call, nil
	}
	ergoValue(call, res, nil)
	return call, res
}
