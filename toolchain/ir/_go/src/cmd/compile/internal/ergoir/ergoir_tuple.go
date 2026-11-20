// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: tuples and multi-result calls (FLIP F4b). See ergoir.go.
//
// An Ergo tuple is an unnamed struct, spelled as Go spells it,
// "struct{F0 T0; F1 T1}": "lit" builds one and "field" reads one, as for
// a named record. "tdef" binds the results of a call with several
// results (of a function of a package or of this one, a Go method or a
// function value) to locals, in order: l0, l1, … := f(…); a local of -1
// is Go's blank.

package ergoir

import (
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// structType is the unnamed struct s spells, "struct{F0 T0; F1 T1}", or
// nil (an error).
func (c *ergoChecker) structType(p ergoPos, s string) types2.Type {
	body := strings.TrimSuffix(strings.TrimPrefix(s, "struct{"), "}")
	if len(body) != len(s)-len("struct{}") {
		c.errorf(p, "ergoir: struct type %q", s)
		return nil
	}
	var fields []*types2.Var
	seen := map[string]bool{}
	depth, start := 0, 0
	for i := 0; i <= len(body); i++ {
		if i < len(body) {
			switch body[i] {
			case '(', '[', '{':
				depth++
				continue
			case ')', ']', '}':
				depth--
				continue
			case ';':
				if depth != 0 {
					continue
				}
			default:
				continue
			}
		}
		part := strings.TrimSpace(body[start:i])
		start = i + 1
		name, typ, ok := strings.Cut(part, " ")
		if !ok || name == "" || name[0] < 'A' || name[0] > 'Z' || seen[name] {
			c.errorf(p, "ergoir: struct type %q", s)
			return nil
		}
		seen[name] = true
		ft := c.typ(p, strings.TrimSpace(typ))
		if ft == nil {
			return nil
		}
		fields = append(fields, types2.NewField(c.pos(p), c.pkg, name, ft, false))
	}
	return types2.NewStruct(fields, nil)
}

// ergoTdefCall are the call kinds a tdef binds.
var ergoTdefCall = map[string]bool{"pcall": true, "call": true, "mcall": true, "vcall": true}

// tupleStmt decodes a "tdef"; ok is false for any other kind.
func (c *ergoChecker) tupleStmt(s *ergoStmt) (st syntax.Stmt, ok bool) {
	if s.K != "tdef" {
		return nil, false
	}
	if s.X == nil || !ergoTdefCall[s.X.K] || len(s.Ls) < 2 {
		c.errorf(s.Pos, "ergoir: tdef of a value that is not a call")
		return nil, true
	}
	c.tdef = s.X
	x, t := c.expr(s.X)
	c.tdef = nil
	if x == nil {
		return nil, true
	}
	tup, isTuple := t.(*types2.Tuple)
	if !isTuple || tup.Len() != len(s.Ls) {
		c.errorf(s.Pos, "ergoir: tdef: %v does not bind %d locals", t, len(s.Ls))
		return nil, true
	}
	pos := c.pos(s.Pos)
	lhs := &syntax.ListExpr{}
	lhs.SetPos(pos)
	def := false
	for i, l := range s.Ls {
		if l == -1 {
			lhs.ElemList = append(lhs.ElemList, syntax.NewName(pos, "_"))
			continue
		}
		v := c.local(s.Pos, l)
		if v == nil {
			return nil, true
		}
		if !types2.Identical(tup.At(i).Type(), v.Type()) {
			c.errorf(s.Pos, "ergoir: tdef: result %d is %v, not %v", i, tup.At(i).Type(), v.Type())
			return nil, true
		}
		lhs.ElemList = append(lhs.ElemList, c.name(s.Pos, v, true))
		def = true
	}
	a := &syntax.AssignStmt{Lhs: lhs, Rhs: x}
	if def {
		a.Op = syntax.Def
	}
	a.SetPos(pos)
	return a, true
}
