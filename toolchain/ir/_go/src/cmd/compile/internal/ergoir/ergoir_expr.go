// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: expressions, literals and constants. See ergoir.go.

package ergoir

import (
	"go/constant"
	"go/token"
	"strconv"
	"strings"
	"sync"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// expr checks x and returns its typed syntax and its type (nil: a call
// with no result).
func (c *ergoChecker) expr(x *ergoExpr) (syntax.Expr, types2.Type) {
	if x == nil {
		c.errorf(ergoPos{-1}, "ergoir: missing expression")
		return nil, nil
	}
	pos := c.pos(x.Pos)
	switch x.K {
	case "result":
		return c.resultExpr(x) // ergoir_f7f.go
	case "recover":
		return c.recoverExpr(x) // ergoir_f7f.go
	case "builtin":
		return c.builtinExpr(x) // ergoir_builtin.go
	case "local":
		v := c.local(x.Pos, x.I)
		if v == nil {
			return nil, nil
		}
		return c.name(x.Pos, v, false), v.Type()
	case "const":
		t := c.typ(x.Pos, x.T)
		if t == nil {
			return nil, nil
		}
		val := x.V
		if x.Op == "quoted" {
			// A string constant that is not UTF-8, which the JSON file
			// cannot carry: its Go quoted form (CW-IR, CBOR's bytes).
			s, err := strconv.Unquote(x.V)
			if err != nil || !ergoIsString(t) {
				c.errorf(x.Pos, "ergoir: quoted constant %s of type %v", x.V, t)
				return nil, nil
			}
			val = s
		}
		v := ergoConstant(t, val)
		if v == nil {
			c.errorf(x.Pos, "ergoir: constant %q of type %v", x.V, t)
			return nil, nil
		}
		if digits, neg := strings.CutPrefix(x.V, "-"); neg && t == types2.Typ[types2.Float64] {
			// -1e-12: Go has no negative literal, so the same constant in
			// Go is the negation of an untyped literal, exact until it is
			// converted (types2 keeps -1/10^12 on the negation, the rounded
			// float64 only where it is converted). Keep Go's exact value:
			// the compiled float64 is the same, and the text view's
			// (-1e-12) compiles to this tree (IRFIX-FLOAT). Only float64,
			// the literal's default type: a constant of any other type is
			// spelled as a conversion, whose value Go rounds.
			if exact := constant.MakeFromLiteral(digits, token.FLOAT, 0); exact.Kind() != constant.Unknown {
				v = constant.UnaryOp(token.SUB, exact, 0)
			}
		}
		lit := &syntax.BasicLit{Value: x.V}
		lit.SetPos(pos)
		ergoValue(lit, t, v)
		return lit, t
	case "bin":
		op, ok := ergoBinOps[x.Op]
		if !ok {
			c.errorf(x.Pos, "ergoir: operator %q", x.Op)
			return nil, nil
		}
		l, lt := c.expr(x.X)
		r, rt := c.expr(x.Y)
		if l == nil || r == nil {
			return nil, nil
		}
		t := c.binType(x.Pos, op, x.Op, lt, rt)
		if t == nil {
			return nil, nil
		}
		e := &syntax.Operation{Op: op, X: l, Y: r}
		e.SetPos(pos)
		ergoValue(e, t, nil)
		return e, t
	case "un":
		e0, t := c.expr(x.X)
		if e0 == nil {
			return nil, nil
		}
		var op syntax.Operator
		switch {
		case x.Op == "!" && ergoIsBool(t):
			op = syntax.Not
		case x.Op == "-" && ergoIsNumeric(t):
			op = syntax.Sub
		case x.Op == "+" && ergoIsNumeric(t):
			op = syntax.Add
		case x.Op == "^" && ergoIsInteger(t):
			op = syntax.Xor
		default:
			c.errorf(x.Pos, "ergoir: unary %s on %v", x.Op, t)
			return nil, nil
		}
		if b, ok := e0.(*syntax.Operation); ok && b.Y != nil || op == syntax.Not {
			// A binary operand, and every operand of a !, stands in
			// parentheses: the writer's staticBool reads Not(x) as x's own
			// value. Go reaches it with neither a constant x (types2 folds
			// !c) nor an AndAnd (its parser keeps the parentheses); the
			// decoder folds nothing, so !false read false and inverted an
			// assert, and !(p && false) would drop its branch (#42).
			p := &syntax.ParenExpr{X: e0}
			p.SetPos(e0.Pos())
			ergoValue(p, t, nil)
			e0 = p
		}
		e := &syntax.Operation{Op: op, X: e0}
		e.SetPos(pos)
		ergoValue(e, t, nil)
		return e, t
	case "deref":
		// *p: the value a pointer points at (IRG6: a with copies the
		// environment, env2 := *env).
		e0, t := c.expr(x.X)
		if e0 == nil {
			return nil, nil
		}
		pt, ok := types2.CoreType(t).(*types2.Pointer)
		if !ok {
			c.errorf(x.Pos, "ergoir: deref of %v", t)
			return nil, nil
		}
		e := &syntax.Operation{Op: syntax.Mul, X: e0}
		e.SetPos(pos)
		ergoValue(e, pt.Elem(), nil)
		return e, pt.Elem()
	case "addr":
		// &x of a local (IRG6: the copy a with's block runs against), or
		// &T{…} of a literal (an operations conversion's adapter), or &p
		// of a place: a field of an addressable record or through a
		// pointer, a slice's element, *p (FLIP F2).
		if x.X == nil {
			c.errorf(x.Pos, "ergoir: address of nothing")
			return nil, nil
		}
		e0, t := c.expr(x.X)
		if e0 == nil {
			return nil, nil
		}
		if x.X.K != "lit" && !ergoPlace(e0) {
			c.errorf(x.Pos, "ergoir: address of a non-place")
			return nil, nil
		}
		pt := types2.NewPointer(t)
		e := &syntax.Operation{Op: syntax.And, X: e0}
		e.SetPos(pos)
		ergoValue(e, pt, nil)
		return e, pt
	case "call":
		fn, ok := c.pkg.Scope().Lookup(x.Name).(*types2.Func)
		if !ok {
			c.errorf(x.Pos, "ergoir: %s is not a function of the package", x.Name)
			return nil, nil
		}
		sig := fn.Type().(*types2.Signature)
		fun := syntax.NewName(pos, x.Name)
		c.info.Uses[fun] = fn
		if sig.TypeParams().Len() > 0 || x.T != "" {
			// A generic function's instance: T lists its type arguments
			// (Go's spelling), recorded as Go records an inferred one.
			sig = c.funcInstance(x.Pos, fun, sig, x.T)
			if sig == nil {
				return nil, nil
			}
		}
		ergoValue(fun, sig, nil)
		return c.call(x, fun, sig)
	case "mcall":
		recv, rt := c.expr(x.X)
		if recv == nil {
			return nil, nil
		}
		sel, ok := types2.LookupSelection(rt, false, c.pkg, x.Name)
		if !ok || sel.Kind() != types2.MethodVal {
			c.errorf(x.Pos, "ergoir: %v has no method %s", rt, x.Name)
			return nil, nil
		}
		// No implicit &x and no embedded path: a pointer method is called
		// on a pointer only.
		mrecv := sel.Obj().(*types2.Func).Type().(*types2.Signature).Recv().Type()
		if isPtrTo(mrecv, rt) || len(sel.Index()) != 1 {
			c.errorf(x.Pos, "ergoir: %s needs an implicit address or an embedded path", x.Name)
			return nil, nil
		}
		se := &syntax.SelectorExpr{X: recv, Sel: syntax.NewName(pos, x.Name)}
		se.SetPos(pos)
		c.info.Selections[se] = &sel
		c.info.Uses[se.Sel] = sel.Obj()
		sig := sel.Type().(*types2.Signature)
		ergoValue(se, sig, nil)
		return c.call(x, se, sig)
	case "pcall":
		// PATH.NAME(args): an exported function of an imported package.
		pkg := c.imported(x.Pos, x.T)
		if pkg == nil {
			return nil, nil
		}
		fn, ok := pkg.Scope().Lookup(x.Name).(*types2.Func)
		if !ok || !fn.Exported() {
			c.errorf(x.Pos, "ergoir: %s is not a function of %q", x.Name, x.T)
			return nil, nil
		}
		sig := fn.Type().(*types2.Signature)
		sel := syntax.NewName(pos, x.Name)
		c.info.Uses[sel] = fn
		if sig.TypeParams().Len() > 0 || x.V != "" {
			// A generic function's instance: V lists its type arguments.
			sig = c.funcInstance(x.Pos, sel, sig, x.V)
			if sig == nil {
				return nil, nil
			}
		}
		tv := syntax.TypeAndValue{Type: sig}
		tv.SetIsValue()
		sel.SetTypeInfo(tv)
		return c.call(x, c.qualified(x.Pos, pkg, sel, tv), sig)
	case "conv":
		// T(x) between basic types: numeric to numeric, string to
		// string, bool to bool.
		// Or between a function type and a type defined over it
		// (Seq[T] of a function literal; ergoFuncConv).
		t := c.typ(x.Pos, x.T)
		e0, et := c.expr(x.X)
		if t == nil || e0 == nil {
			return nil, nil
		}
		if !(ergoIsNumeric(t) && ergoIsNumeric(et) || ergoIsString(t) && ergoIsString(et) || ergoIsBool(t) && ergoIsBool(et) || ergoFuncConv(t, et)) {
			c.errorf(x.Pos, "ergoir: conversion of %v to %v", et, t)
			return nil, nil
		}
		fun := c.typeExpr(x.Pos, t)
		if fun == nil {
			return nil, nil
		}
		call := &syntax.CallExpr{Fun: fun, ArgList: []syntax.Expr{e0}}
		call.SetPos(pos)
		ergoValue(call, t, nil)
		return call, t
	case "global":
		v, ok := c.pkg.Scope().Lookup(x.Name).(*types2.Var)
		if !ok {
			c.errorf(x.Pos, "ergoir: %s is not a variable of the package", x.Name)
			return nil, nil
		}
		return c.name(x.Pos, v, false), v.Type()
	case "field":
		recv, rt := c.expr(x.X)
		if recv == nil {
			return nil, nil
		}
		sel, ok := types2.LookupSelection(rt, false, c.pkg, x.Name)
		if !ok || sel.Kind() != types2.FieldVal || len(sel.Index()) != 1 {
			c.errorf(x.Pos, "ergoir: %v has no direct field %s", rt, x.Name)
			return nil, nil
		}
		se := &syntax.SelectorExpr{X: recv, Sel: syntax.NewName(pos, x.Name)}
		se.SetPos(pos)
		c.info.Selections[se] = &sel
		c.info.Uses[se.Sel] = sel.Obj()
		ergoValue(se, sel.Type(), nil)
		return se, sel.Type()
	case "index":
		xs, xt := c.expr(x.X)
		i, it := c.expr(x.Y)
		if xs == nil || i == nil {
			return nil, nil
		}
		if m, ok := types2.CoreType(xt).(*types2.Map); ok {
			// A Map's value by its key, read only (CW-IR: a codec
			// writer reads each entry by a key of WireKeys).
			if !types2.Identical(it, m.Key()) {
				c.errorf(x.Pos, "ergoir: index of %v by %v", xt, it)
				return nil, nil
			}
			e := &syntax.IndexExpr{X: xs, Index: i}
			e.SetPos(pos)
			ergoValue(e, m.Elem(), nil)
			return e, m.Elem()
		}
		// Go indexes by a value of any integer type (red #31: a string
		// by a byte).
		elem := ergoIndexElem(xt) // ergoir_f7.go
		if elem == nil || !ergoIsInteger(it) {
			c.errorf(x.Pos, "ergoir: index of %v by %v", xt, it)
			return nil, nil
		}
		e := &syntax.IndexExpr{X: xs, Index: i}
		e.SetPos(pos)
		ergoValue(e, elem, nil)
		return e, elem
	case "len":
		xs, xt := c.expr(x.X)
		if xs == nil {
			return nil, nil
		}
		switch types2.CoreType(xt).(type) {
		case *types2.Slice, *types2.Map:
		default:
			if ergoIsString(xt) {
				break
			}
			c.errorf(x.Pos, "ergoir: len of %v", xt)
			return nil, nil
		}
		b := types2.Universe.Lookup("len")
		fun := syntax.NewName(pos, "len")
		c.info.Uses[fun] = b
		tv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil,
			types2.NewTuple(types2.NewParam(pos, nil, "", xt)),
			types2.NewTuple(types2.NewParam(pos, nil, "", types2.Typ[types2.Int])), false)}
		tv.SetIsBuiltin()
		fun.SetTypeInfo(tv)
		call := &syntax.CallExpr{Fun: fun, ArgList: []syntax.Expr{xs}}
		call.SetPos(pos)
		ergoValue(call, types2.Typ[types2.Int], nil)
		return call, types2.Typ[types2.Int]
	case "append":
		// append(xs, v): Go's builtin append of one element, as types2
		// records it (builtins.go _Append: func(S, ...E) S, variadic).
		if len(x.Args) != 2 {
			c.errorf(x.Pos, "ergoir: append of %d operands", len(x.Args))
			return nil, nil
		}
		xs, xt := c.expr(x.Args[0])
		v, vt := c.expr(x.Args[1])
		if xs == nil || v == nil {
			return nil, nil
		}
		sl, ok := types2.CoreType(xt).(*types2.Slice)
		if !ok {
			c.errorf(x.Pos, "ergoir: append to %v, not a slice", xt)
			return nil, nil
		}
		if !c.assignable(x.Pos, "append", vt, sl.Elem()) {
			return nil, nil
		}
		b := types2.Universe.Lookup("append")
		fun := syntax.NewName(pos, "append")
		c.info.Uses[fun] = b
		tv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil,
			types2.NewTuple(types2.NewParam(pos, nil, "", xt), types2.NewParam(pos, nil, "", types2.NewSlice(sl.Elem()))),
			types2.NewTuple(types2.NewParam(pos, nil, "", xt)), true)}
		tv.SetIsBuiltin()
		fun.SetTypeInfo(tv)
		call := &syntax.CallExpr{Fun: fun, ArgList: []syntax.Expr{xs, v}}
		call.SetPos(pos)
		ergoValue(call, xt, nil)
		return call, xt
	case "slice":
		return c.slice(x) // ergoir_slice.go
	case "lit":
		t := c.typ(x.Pos, x.T)
		if t == nil {
			return nil, nil
		}
		lit := &syntax.CompositeLit{}
		lit.SetPos(pos)
		switch u := types2.CoreType(t).(type) {
		case *types2.Struct:
			// Keyed, in the IR's (source) order, every field once.
			if len(x.Keys) != len(x.Elems) || len(x.Elems) != u.NumFields() {
				c.errorf(x.Pos, "ergoir: %v literal with %d of %d fields", t, len(x.Elems), u.NumFields())
				return nil, nil
			}
			seen := map[string]bool{}
			for i, ex := range x.Elems {
				sel, ok := types2.LookupSelection(t, false, c.pkg, x.Keys[i])
				if !ok || sel.Kind() != types2.FieldVal || len(sel.Index()) != 1 || seen[x.Keys[i]] {
					c.errorf(ex.Pos, "ergoir: %v has no field %s, or it is given twice", t, x.Keys[i])
					return nil, nil
				}
				seen[x.Keys[i]] = true
				e, et := c.expr(ex)
				if e == nil || !c.assignable(ex.Pos, "field", et, sel.Type()) {
					return nil, nil
				}
				key := syntax.NewName(c.pos(ex.Pos), x.Keys[i])
				c.info.Uses[key] = sel.Obj()
				kv := &syntax.KeyValueExpr{Key: key, Value: e}
				kv.SetPos(c.pos(ex.Pos))
				lit.ElemList = append(lit.ElemList, kv)
			}
			lit.NKeys = len(x.Keys)
		case *types2.Slice:
			for _, ex := range x.Elems {
				e, et := c.expr(ex)
				if e == nil || !c.assignable(ex.Pos, "element", et, u.Elem()) {
					return nil, nil
				}
				lit.ElemList = append(lit.ElemList, e)
			}
		case *types2.Array:
			// Every element in order (Ergo's Arrays 2; the empty literal is a "zero").
			if int64(len(x.Elems)) != u.Len() {
				c.errorf(x.Pos, "ergoir: %v literal with %d of %d elements", t, len(x.Elems), u.Len())
				return nil, nil
			}
			for _, ex := range x.Elems {
				e, et := c.expr(ex)
				if e == nil || !c.assignable(ex.Pos, "element", et, u.Elem()) {
					return nil, nil
				}
				lit.ElemList = append(lit.ElemList, e)
			}
		case *types2.Map:
			// Each entry's key, then its value (IRG4b).
			if len(x.Keys) != 0 || len(x.Elems)%2 != 0 {
				c.errorf(x.Pos, "ergoir: map literal with %d elements", len(x.Elems))
				return nil, nil
			}
			for i := 0; i < len(x.Elems); i += 2 {
				k, kt := c.expr(x.Elems[i])
				v, vt := c.expr(x.Elems[i+1])
				if k == nil || v == nil || !c.assignable(x.Elems[i].Pos, "key", kt, u.Key()) || !c.assignable(x.Elems[i+1].Pos, "value", vt, u.Elem()) {
					return nil, nil
				}
				kv := &syntax.KeyValueExpr{Key: k, Value: v}
				kv.SetPos(c.pos(x.Elems[i].Pos))
				lit.ElemList = append(lit.ElemList, kv)
			}
			lit.NKeys = len(lit.ElemList)
		default:
			c.errorf(x.Pos, "ergoir: literal of %v", t)
			return nil, nil
		}
		lit.Rbrace = pos
		ergoValue(lit, t, nil)
		return lit, t
	}
	if e, t, ok := c.failExpr(x); ok { // ergoir_fail.go
		return e, t
	}
	if e, t, ok := c.unionExpr(x); ok { // ergoir_union.go
		return e, t
	}
	if e, t, ok := c.closureExpr(x); ok { // ergoir_closure.go
		return e, t
	}
	c.errorf(x.Pos, "ergoir: unknown expression %q", x.K)
	return nil, nil
}

// binType is the type of lt op rt (spelled name), or nil (an error): Go's
// rules for the basic types. Both operands have one type, except a
// shift's count, which is any integer.
func (c *ergoChecker) binType(p ergoPos, op syntax.Operator, name string, lt, rt types2.Type) types2.Type {
	bad := func() types2.Type {
		c.errorf(p, "ergoir: %s on %v and %v", name, lt, rt)
		return nil
	}
	if op == syntax.Shl || op == syntax.Shr {
		if !ergoIsInteger(lt) || !ergoIsInteger(rt) {
			return bad()
		}
		return lt
	}
	if !c.assignable(p, name, rt, lt) {
		return nil
	}
	switch op {
	case syntax.Add:
		if !ergoIsNumeric(lt) && !ergoIsString(lt) {
			return bad()
		}
		return lt
	case syntax.Sub, syntax.Mul, syntax.Div:
		if !ergoIsNumeric(lt) {
			return bad()
		}
		return lt
	case syntax.Rem, syntax.And, syntax.Or, syntax.Xor, syntax.AndNot:
		if !ergoIsInteger(lt) {
			return bad()
		}
		return lt
	case syntax.Eql, syntax.Neq:
		if !types2.Comparable(lt) {
			return bad()
		}
		return types2.Typ[types2.Bool]
	case syntax.Lss, syntax.Leq, syntax.Gtr, syntax.Geq:
		if !ergoOrdered(lt) {
			return bad()
		}
		return types2.Typ[types2.Bool]
	case syntax.AndAnd, syntax.OrOr:
		if !ergoIsBool(lt) {
			return bad()
		}
		return lt
	}
	return bad()
}

// ergoOrdered reports whether Go's < applies to t: a basic number or
// string, or a type parameter whose type set holds only those (an Ergo
// ordered parameter, GP-ORDER). types2 judges the type parameter: it
// satisfies cmp.Ordered's type set, so a constraint with no type terms
// is not ordered.
func ergoOrdered(t types2.Type) bool {
	if _, ok := t.(*types2.TypeParam); !ok {
		return ergoIsNumeric(t) || ergoIsString(t)
	}
	return types2.Satisfies(t, ergoOrderedSet())
}

// ergoOrderedSet is cmp.Ordered: ~int | … | ~float64 | ~string.
var ergoOrderedSet = sync.OnceValue(func() *types2.Interface {
	var terms []*types2.Term
	for _, k := range []types2.BasicKind{types2.Int, types2.Int8, types2.Int16, types2.Int32, types2.Int64, types2.Uint, types2.Uint8, types2.Uint16, types2.Uint32, types2.Uint64, types2.Uintptr, types2.Float32, types2.Float64, types2.String} {
		terms = append(terms, types2.NewTerm(true, types2.Typ[k]))
	}
	it := types2.NewInterfaceType(nil, []types2.Type{types2.NewUnion(terms)})
	// Its type set is computed here, once (types2 has no Complete): computed
	// lazily, two decoder goroutines would both write the one shared value.
	it.IsMethodSet()
	return it
})

// ergoFuncConv reports a conversion between a function type and a type
// defined over it (Seq[T] of a function literal): identical signatures,
// which Go converts as identical underlying types (GP-DEFINED).
func ergoFuncConv(t, from types2.Type) bool {
	ts, ok := types2.CoreType(t).(*types2.Signature)
	if !ok {
		return false
	}
	fs, ok := types2.CoreType(from).(*types2.Signature)
	return ok && types2.Identical(ts, fs)
}
