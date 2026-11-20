// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: types, locals, names and constants. See ergoir.go.

package ergoir

import (
	"go/constant"
	"go/token"
	"internal/buildcfg"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// typ resolves a type spelling: NAME (a type parameter of the function,
// then package scope or universe),
// PATH.NAME (an exported type of the imported package PATH), []T, *T,
// map[K]V, a generic type's instance NAME[T1, T2] and a function
// literal's type (ergoir_closure.go).
func (c *ergoChecker) typ(p ergoPos, s string) types2.Type {
	if t, ok := c.literalType(p, s); ok { // ergoir_closure.go
		return t
	}
	switch {
	case strings.HasPrefix(s, "[]"):
		if e := c.typ(p, s[2:]); e != nil {
			return types2.NewSlice(e)
		}
		return nil
	case strings.HasPrefix(s, "*"):
		if e := c.typ(p, s[1:]); e != nil {
			return types2.NewPointer(e)
		}
		return nil
	}
	if t, ok := c.arrayType(p, s); ok { // ergoir_f7.go
		return t
	}
	if rest, ok := strings.CutPrefix(s, "map["); ok {
		// map[K]V: K ends at the bracket that closes map's.
		depth := 1
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth == 0 {
				k, v := c.typ(p, rest[:i]), c.typ(p, rest[i+1:])
				if k == nil || v == nil {
					return nil
				}
				if !types2.Comparable(k) {
					c.errorf(p, "ergoir: map key %v is not comparable", k)
					return nil
				}
				return types2.NewMap(k, v)
			}
		}
		c.errorf(p, "ergoir: %q is not a type", s)
		return nil
	}
	if i := strings.IndexByte(s, '['); i > 0 && strings.HasSuffix(s, "]") {
		return c.instance(p, s[:i], s[i+1:len(s)-1])
	}
	var obj types2.Object
	if i := strings.LastIndex(s, "."); i >= 0 {
		if pkg := c.imported(p, s[:i]); pkg != nil {
			obj = pkg.Scope().Lookup(s[i+1:])
			if obj != nil && !obj.Exported() {
				obj = nil
			}
		}
	} else {
		for _, tp := range c.tparams {
			if tp.Obj().Name() == s {
				return tp
			}
		}
		obj = c.pkg.Scope().Lookup(s)
		if obj == nil {
			obj = types2.Universe.Lookup(s)
		}
	}
	tn, ok := obj.(*types2.TypeName)
	if !ok {
		c.errorf(p, "ergoir: %q is not a type", s)
		return nil
	}
	return tn.Type()
}

// instance is the generic type name instantiated with the type arguments
// args, written as Go writes them (comma and space between top-level
// arguments): Option[int], Pair[[]string, Option[int]]. Instantiate
// checks the arguments against the type parameters' constraints.
func (c *ergoChecker) instance(p ergoPos, name, args string) types2.Type {
	gen := c.typ(p, name)
	if gen == nil {
		return nil
	}
	named, ok := gen.(*types2.Named)
	if !ok || named.TypeParams().Len() == 0 {
		c.errorf(p, "ergoir: %s is not a generic type", name)
		return nil
	}
	targs := c.typeList(p, args)
	if targs == nil {
		return nil
	}
	t, err := types2.Instantiate(c.context(), named.Origin(), targs, true)
	if err != nil {
		c.errorf(p, "ergoir: %s[%s]: %v", name, args, err)
		return nil
	}
	return t
}

// typeList resolves a list of type spellings, comma and space between
// top-level ones; nil when one is not a type (an error).
func (c *ergoChecker) typeList(p ergoPos, args string) []types2.Type {
	var out []types2.Type
	depth, start := 0, 0
	for i := 0; i <= len(args); i++ {
		if i < len(args) {
			switch args[i] {
			case '[', '(', '{':
				// A function type argument's lists hold commas too
				// (FLIP F7 LY: Option[func(*Env, E) (struct{}, error)]).
				depth++
				continue
			case ']', ')', '}':
				depth--
				continue
			case ',':
				if depth > 0 {
					continue
				}
			default:
				continue
			}
		}
		t := c.typ(p, strings.TrimSpace(args[start:i]))
		if t == nil {
			return nil
		}
		out = append(out, t)
		start = i + 1
	}
	return out
}

// funcInstance is the signature of the generic function fun's instance
// with the type arguments args, recorded in Instances at fun; nil (an
// error) when args do not instantiate sig.
func (c *ergoChecker) funcInstance(p ergoPos, fun *syntax.Name, sig *types2.Signature, args string) *types2.Signature {
	if sig.TypeParams().Len() == 0 || args == "" {
		c.errorf(p, "ergoir: type arguments [%s] for %v", args, sig)
		return nil
	}
	targs := c.typeList(p, args)
	if targs == nil {
		return nil
	}
	t, err := types2.Instantiate(c.context(), sig, targs, true)
	if err != nil {
		c.errorf(p, "ergoir: %s[%s]: %v", fun.Value, args, err)
		return nil
	}
	inst := t.(*types2.Signature)
	c.info.Instances[fun] = types2.Instance{TypeArgs: ergoTypeList(targs), Type: inst}
	return inst
}

// ergoTypeList is targs as a *types2.TypeList, which types2 makes only
// for an instance: the type arguments of a throwaway generic type's
// instance (types2 exports no TypeList constructor; the writer reads an
// instance's arguments from the list).
func ergoTypeList(targs []types2.Type) *types2.TypeList {
	var nopos syntax.Pos
	any := types2.Universe.Lookup("any").Type()
	tparams := make([]*types2.TypeParam, len(targs))
	for i := range targs {
		tparams[i] = types2.NewTypeParam(types2.NewTypeName(nopos, nil, "T", nil), any)
	}
	gen := types2.NewNamed(types2.NewTypeName(nopos, nil, "ergoTypeList", nil), types2.NewStruct(nil, nil), nil)
	gen.SetTypeParams(tparams)
	t, err := types2.Instantiate(nil, gen, targs, false)
	if err != nil {
		base.Fatalf("ergoir: type list: %v", err)
	}
	return t.(*types2.Named).TypeArgs()
}

func (c *ergoChecker) context() *types2.Context {
	if c.ctxt == nil {
		c.ctxt = types2.NewContext()
	}
	return c.ctxt
}

// imported is the package of import path the package imports, or nil
// (an error).
func (c *ergoChecker) imported(p ergoPos, path string) *types2.Package {
	for _, pkg := range c.pkg.Imports() {
		if pkg.Path() == path {
			return pkg
		}
	}
	c.errorf(p, "ergoir: the package does not import %q", path)
	return nil
}

// typeExpr is a type expression of t, for a conversion's function: a name
// of the universe or of the package, or a qualified name.
func (c *ergoChecker) typeExpr(p ergoPos, t types2.Type) syntax.Expr {
	pos := c.pos(p)
	var obj *types2.TypeName
	switch t := t.(type) {
	case *types2.Basic:
		obj, _ = types2.Universe.Lookup(t.Name()).(*types2.TypeName)
	case *types2.Named:
		obj = t.Obj()
	case *types2.TypeParam:
		obj = t.Obj() // a Slot row's parameter (F error), asserted to (OPC-ADAPT2)
	case *types2.Alias:
		if t.TypeArgs().Len() == 0 {
			obj = t.Obj() // a shared type's alias (ergo_ergoshared)
		}
	}
	if obj == nil {
		c.errorf(p, "ergoir: conversion to %v", t)
		return nil
	}
	tv := syntax.TypeAndValue{Type: t}
	tv.SetIsType()
	name := syntax.NewName(pos, obj.Name())
	c.info.Uses[name] = obj
	name.SetTypeInfo(tv)
	if obj.Pkg() == nil || obj.Pkg() == c.pkg {
		return name
	}
	return c.qualified(p, obj.Pkg(), name, tv)
}

// qualified is pkg.sel, sel the name of an object of the imported pkg,
// with its type record tv.
func (c *ergoChecker) qualified(p ergoPos, pkg *types2.Package, sel *syntax.Name, tv syntax.TypeAndValue) syntax.Expr {
	pos := c.pos(p)
	q := syntax.NewName(pos, pkg.Name())
	c.info.Uses[q] = types2.NewPkgName(pos, c.pkg, pkg.Name(), pkg)
	se := &syntax.SelectorExpr{X: q, Sel: sel}
	se.SetPos(pos)
	se.SetTypeInfo(tv)
	return se
}

func (c *ergoChecker) local(p ergoPos, i int) *types2.Var {
	if i < 0 || i >= len(c.locals) {
		c.errorf(p, "ergoir: local %d out of range", i)
		return nil
	}
	return c.locals[i]
}

// name is a use (def false) or the definition of v.
func (c *ergoChecker) name(p ergoPos, v *types2.Var, def bool) *syntax.Name {
	n := syntax.NewName(c.pos(p), v.Name())
	if def {
		c.info.Defs[n] = v
		return n
	}
	c.info.Uses[n] = v
	tv := syntax.TypeAndValue{Type: v.Type()}
	tv.SetIsValue()
	tv.SetAddressable()
	tv.SetAssignable()
	n.SetTypeInfo(tv)
	return n
}

func (c *ergoChecker) assignable(p ergoPos, what string, src, dst types2.Type) bool {
	if src == nil || dst == nil || !types2.Identical(src, dst) {
		c.errorf(p, "ergoir: %s: %v is not %v", what, src, dst)
		return false
	}
	return true
}

func ergoBasicInfo(t types2.Type) types2.BasicInfo {
	if b, ok := types2.CoreType(t).(*types2.Basic); ok {
		return b.Info()
	}
	return 0
}

func ergoIsBool(t types2.Type) bool     { return ergoBasicInfo(t)&types2.IsBoolean != 0 }
func ergoIsInteger(t types2.Type) bool  { return ergoBasicInfo(t)&types2.IsInteger != 0 }
func ergoIsUnsigned(t types2.Type) bool { return ergoBasicInfo(t)&types2.IsUnsigned != 0 }
func ergoIsFloat(t types2.Type) bool    { return ergoBasicInfo(t)&types2.IsFloat != 0 }
func ergoIsNumeric(t types2.Type) bool  { return ergoIsInteger(t) || ergoIsFloat(t) }
func ergoIsString(t types2.Type) bool   { return ergoBasicInfo(t)&types2.IsString != 0 }

// ergoConstant is the constant of type t that the IR spells v, or nil when
// v does not spell one or t cannot hold it: an integer in decimal (an
// optional minus sign), a float as strconv.FormatFloat writes it, true or
// false, any string.
func ergoConstant(t types2.Type, v string) constant.Value {
	switch {
	case ergoIsInteger(t):
		digits, neg := strings.CutPrefix(v, "-")
		if digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
			return nil
		}
		x := constant.MakeFromLiteral(digits, token.INT, 0)
		if neg {
			x = constant.UnaryOp(token.SUB, x, 0)
		}
		bits := uint(8 * types2.SizesFor("gc", buildcfg.GOARCH).Sizeof(t))
		lo, hi := constant.MakeInt64(0), constant.Shift(constant.MakeInt64(1), token.SHL, bits)
		if !ergoIsUnsigned(t) {
			hi = constant.Shift(constant.MakeInt64(1), token.SHL, bits-1)
			lo = constant.UnaryOp(token.SUB, hi, 0)
		}
		if constant.Compare(x, token.LSS, lo) || constant.Compare(x, token.GEQ, hi) {
			return nil
		}
		return x
	case ergoIsFloat(t):
		bits := 64
		if types2.SizesFor("gc", buildcfg.GOARCH).Sizeof(t) == 4 {
			bits = 32
		}
		f, err := strconv.ParseFloat(v, bits)
		if err != nil || strconv.FormatFloat(f, 'g', -1, bits) != v {
			return nil
		}
		return constant.MakeFloat64(f)
	case ergoIsBool(t):
		if v != "true" && v != "false" {
			return nil
		}
		return constant.MakeBool(v == "true")
	case ergoIsString(t):
		return constant.MakeString(v)
	}
	return nil
}

// value records t (and v, a constant) as x's type.
// ergoPlace reports whether x is a place, as Go's addressability has it,
// and marks it and every place inside it addressable and assignable: a
// variable, *p, a slice's element, a field through a pointer, a field of
// a place (FLIP F2: a store's target and &p).
func ergoPlace(x syntax.Expr) bool {
	ok := false
	switch e := x.(type) {
	case *syntax.Name:
		return e.GetTypeInfo().Addressable()
	case *syntax.ParenExpr:
		ok = ergoPlace(e.X)
	case *syntax.Operation:
		ok = e.Op == syntax.Mul && e.Y == nil
	case *syntax.IndexExpr:
		// A slice's element, an element of an array place or of the
		// array a pointer points at (FLIP F7 arrays x F2 places).
		switch t := types2.CoreType(e.X.GetTypeInfo().Type).(type) {
		case *types2.Slice:
			ok = true
		case *types2.Array:
			ok = ergoPlace(e.X)
		case *types2.Pointer:
			_, ok = types2.CoreType(t.Elem()).(*types2.Array)
		}
	case *syntax.SelectorExpr:
		_, ok = types2.CoreType(e.X.GetTypeInfo().Type).(*types2.Pointer)
		ok = ok || ergoPlace(e.X)
	}
	if ok {
		tv := x.GetTypeInfo()
		tv.SetAddressable()
		tv.SetAssignable()
		x.SetTypeInfo(tv)
	}
	return ok
}

func ergoValue(x syntax.Expr, t types2.Type, v constant.Value) {
	tv := syntax.TypeAndValue{Type: t, Value: v}
	tv.SetIsValue()
	x.SetTypeInfo(tv)
}
