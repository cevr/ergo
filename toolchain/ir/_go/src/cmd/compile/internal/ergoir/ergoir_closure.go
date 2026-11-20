// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: function values (IRG6). See ergoir.go.
//
// A function value's type is spelled as Go spells it, "func(A, B) (R,
// error)", and the empty struct a function with no value returns as
// "struct{}". "func" is a function literal: its parameters are locals of
// the enclosing body's table, and its body is a function of its own (its
// returns, its labels, its loops). "vcall" calls a value of a function
// type.

package ergoir

import (
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// literalType is the type s spells when it is a function type or the
// empty struct; ok is false for any other spelling.
func (c *ergoChecker) literalType(p ergoPos, s string) (t types2.Type, ok bool) {
	switch {
	case s == "struct{}":
		return types2.NewStruct(nil, nil), true
	case strings.HasPrefix(s, "struct{"):
		return c.structType(p, s), true // ergoir_tuple.go
	case !strings.HasPrefix(s, "func("):
		return nil, false
	}
	params, rest, ok := ergoSplitList(s[len("func"):])
	if !ok {
		c.errorf(p, "ergoir: function type %q", s)
		return nil, true
	}
	var results []string
	switch rest = strings.TrimSpace(rest); {
	case rest == "":
	case strings.HasPrefix(rest, "("):
		var tail string
		results, tail, ok = ergoSplitList(rest)
		if !ok || strings.TrimSpace(tail) != "" {
			c.errorf(p, "ergoir: function type %q", s)
			return nil, true
		}
	default:
		results = []string{rest}
	}
	tuple := func(list []string) (*types2.Tuple, bool) {
		var vars []*types2.Var
		for _, e := range list {
			et := c.typ(p, e)
			if et == nil {
				return nil, false
			}
			vars = append(vars, types2.NewParam(c.pos(p), c.pkg, "", et))
		}
		return types2.NewTuple(vars...), true
	}
	pt, ok1 := tuple(params)
	rt, ok2 := tuple(results)
	if !ok1 || !ok2 {
		return nil, true
	}
	return types2.NewSignatureType(nil, nil, nil, pt, rt, false), true
}

// ergoSplitList splits "(A, B) rest" at the commas outside any bracket:
// the list's elements and what follows its closing parenthesis.
func ergoSplitList(s string) (list []string, rest string, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", false
	}
	depth, start := 0, 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if part := strings.TrimSpace(s[start:i]); part != "" {
					list = append(list, part)
				}
				return list, s[i+1:], true
			}
		case ',':
			if depth == 1 {
				list = append(list, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, "", false
}

// closureExpr decodes a function literal and a call of a function value;
// ok is false for any other kind.
func (c *ergoChecker) closureExpr(x *ergoExpr) (e syntax.Expr, t types2.Type, ok bool) {
	switch x.K {
	case "vcall":
		fun, ft := c.expr(x.X)
		if fun == nil {
			return nil, nil, true
		}
		sig, isSig := types2.CoreType(ft).(*types2.Signature)
		if !isSig || sig.TypeParams().Len() > 0 {
			c.errorf(x.Pos, "ergoir: call of a value of %v", ft)
			return nil, nil, true
		}
		e, t = c.call(x, fun, sig)
		return e, t, true
	case "func":
		e, t = c.funcLit(x)
		return e, t, true
	}
	return nil, nil, false
}

// funcLit is func(params) results { body }: its parameters are the locals
// x.Params names, which become the literal's parameters, and its body is
// decoded as a function's, against the literal's signature.
func (c *ergoChecker) funcLit(x *ergoExpr) (syntax.Expr, types2.Type) {
	pos := c.pos(x.Pos)
	t := c.typ(x.Pos, x.T)
	if t == nil {
		return nil, nil
	}
	sig0, ok := t.(*types2.Signature)
	if !ok || sig0.Params().Len() != len(x.Params) || !ergoSigOK(sig0) {
		c.errorf(x.Pos, "ergoir: function literal of %v with %d parameters", t, len(x.Params))
		return nil, nil
	}
	ft := &syntax.FuncType{}
	ft.SetPos(pos)
	field := func(name *syntax.Name, t types2.Type) *syntax.Field {
		// The type's syntax is only a typed name: the writer encodes the
		// literal from its signature, and the text prints its types.
		ts := syntax.NewName(pos, types2.TypeString(t, nil))
		tv := syntax.TypeAndValue{Type: t}
		tv.SetIsType()
		ts.SetTypeInfo(tv)
		f := &syntax.Field{Name: name, Type: ts}
		f.SetPos(pos)
		return f
	}
	var params []*types2.Var
	for i, li := range x.Params {
		v := c.local(x.Pos, li)
		if v == nil {
			return nil, nil
		}
		if !types2.Identical(v.Type(), sig0.Params().At(i).Type()) {
			c.errorf(x.Pos, "ergoir: function literal parameter %s of %v, not %v", v.Name(), v.Type(), sig0.Params().At(i).Type())
			return nil, nil
		}
		pv := types2.NewParam(v.Pos(), c.pkg, v.Name(), v.Type())
		c.locals[li] = pv
		params = append(params, pv)
		name := syntax.NewName(pos, pv.Name())
		c.info.Defs[name] = pv
		ft.ParamList = append(ft.ParamList, field(name, pv.Type()))
	}
	for i := 0; i < sig0.Results().Len(); i++ {
		ft.ResultList = append(ft.ResultList, field(nil, sig0.Results().At(i).Type()))
	}
	sig := types2.NewSignatureType(nil, nil, nil, types2.NewTuple(params...), sig0.Results(), false)
	// The body is a function of its own: its returns are the literal's,
	// and no loop or label of the enclosing body is in its scope.
	sigS, loopsS, loopLabelsS, labelsS, gotosS := c.sig, c.loops, c.loopLabels, c.labels, c.gotos
	c.sig, c.loops, c.loopLabels, c.labels, c.gotos = sig, nil, nil, nil, nil
	before := c.nerr
	body := &syntax.BlockStmt{List: c.stmts(x.Body), Rbrace: c.pos(x.End)}
	body.SetPos(pos)
	c.ergoLabelsDone()
	if c.nerr == before && sig.Results().Len() > 0 {
		if last := lastNonEmptyStmt(body.List); last == nil || !ergoTerminates(last) {
			c.errorf(x.End, "ergoir: function literal: missing return")
		}
	}
	failed := c.nerr != before
	c.sig, c.loops, c.loopLabels, c.labels, c.gotos = sigS, loopsS, loopLabelsS, labelsS, gotosS
	if failed {
		return nil, nil
	}
	lit := &syntax.FuncLit{Type: ft, Body: body}
	lit.SetPos(pos)
	ergoValue(lit, sig, nil)
	return lit, sig
}
