// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: statements and blocks. See ergoir.go.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoTerminates is the Go spec's terminating statement for the IR's
// statement forms: a return, a call of panic, a goto, an if whose both
// branches terminate, a block whose last statement terminates, a labeled
// statement that terminates and that no break leaves, a for without a
// condition that no break leaves, a switch with a default whose clauses
// all terminate and that no break leaves (the builder's irTerminates,
// ir_end.go, is the same list).
func ergoTerminates(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.BlockStmt:
		last := lastNonEmptyStmt(s.List)
		return last != nil && ergoTerminates(last)
	case *syntax.IfStmt:
		return s.Else != nil && ergoTerminates(s.Then) && ergoTerminates(s.Else)
	case *syntax.BranchStmt:
		return s.Tok == syntax.Goto
	case *syntax.LabeledStmt:
		// A block the IR labels (ergoir_label.go); a break naming it
		// leaves it.
		return ergoTerminates(s.Stmt) && !ergoBreaks(s.Stmt, s.Label.Value)
	case *syntax.ForStmt:
		return s.Cond == nil && s.Init == nil && s.Post == nil && !ergoBreaks(s.Body, "")
	case *syntax.SwitchStmt:
		def := false
		for _, c := range s.Body {
			def = def || c.Cases == nil
			last := lastNonEmptyStmt(c.Body)
			if last == nil || !ergoTerminates(last) {
				return false
			}
			for _, b := range c.Body {
				if ergoBreaks(b, "") {
					return false
				}
			}
		}
		return def
	case *syntax.ExprStmt:
		call, ok := s.X.(*syntax.CallExpr)
		if !ok {
			return false
		}
		name, ok := call.Fun.(*syntax.Name)
		return ok && name.Value == "panic"
	}
	return false
}

// ergoBreaks reports a break in s that leaves the statement s is in: one
// naming label, or with label "" an unlabeled one outside any for or
// switch nested in s.
func ergoBreaks(s syntax.Stmt, label string) bool {
	found := false
	syntax.Inspect(s, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BranchStmt:
			if n.Tok == syntax.Break && (label == "" && n.Label == nil || n.Label != nil && n.Label.Value == label) {
				found = true
			}
		case *syntax.ForStmt, *syntax.SwitchStmt:
			return label != ""
		case syntax.Expr:
			return false
		}
		return !found
	})
	return found
}

func (c *ergoChecker) stmts(list []*ergoStmt) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		if st := c.stmt(s); st != nil {
			out = append(out, st)
		}
	}
	return out
}

func (c *ergoChecker) block(p, end ergoPos, list []*ergoStmt) *syntax.BlockStmt {
	b := &syntax.BlockStmt{List: c.stmts(list), Rbrace: c.pos(end)}
	b.SetPos(c.pos(p))
	return b
}

// loopBlock is a loop's body, where break and continue name loop.
// label is the loop's label, or "" (ergoir_label.go).
func (c *ergoChecker) loopBlock(loop *syntax.ForStmt, label string, p, end ergoPos, list []*ergoStmt) *syntax.BlockStmt {
	c.loops, c.loopLabels = append(c.loops, loop), append(c.loopLabels, label)
	defer func() { c.loops, c.loopLabels = c.loops[:len(c.loops)-1], c.loopLabels[:len(c.loopLabels)-1] }()
	return c.block(p, end, list)
}

func (c *ergoChecker) stmt(s *ergoStmt) syntax.Stmt {
	if s == nil {
		c.errorf(ergoPos{-1}, "ergoir: missing statement")
		return nil
	}
	pos := c.pos(s.Pos)
	switch s.K {
	case "defer":
		return c.deferStmt(s, pos) // ergoir_f7f.go
	case "def", "set":
		v := c.local(s.Pos, s.Local)
		x, t := c.expr(s.X)
		if v == nil || x == nil {
			return nil
		}
		a := &syntax.AssignStmt{Lhs: c.name(s.Pos, v, s.K == "def"), Rhs: x}
		switch {
		case s.Op != "":
			// x op= y: the operator's rule, its result x's type.
			op, ok := ergoBinOps[s.Op]
			if !ok || s.K != "set" || op == syntax.AndAnd || op == syntax.OrOr {
				c.errorf(s.Pos, "ergoir: assignment operator %q", s.Op)
				return nil
			}
			if rt := c.binType(s.Pos, op, s.Op, v.Type(), t); rt == nil || !c.assignable(s.Pos, s.Op+"=", rt, v.Type()) {
				return nil
			}
			a.Op = op
		case !c.assignable(s.Pos, s.K, t, v.Type()):
			return nil
		case s.K == "def":
			a.Op = syntax.Def
		}
		a.SetPos(pos)
		return a
	case "store":
		// p = y, p op= y: p a place (ergoPlace), a field of a record a
		// local points at (IRG6: a with publishes its providers into the
		// environment's copy), a field of a local record, a slice's
		// element, *q, and any chain of them (FLIP F2).
		if s.X == nil {
			c.errorf(s.Pos, "ergoir: store to nothing")
			return nil
		}
		lhs, lt := c.storeTo(s.X) // ergoir_mapstore.go: a Map's entry (LY-PLACE), else expr
		rhs, rt := c.expr(s.Y)
		if lhs == nil || rhs == nil {
			return nil
		}
		if !ergoPlace(lhs) && !ergoMapEntry(lhs) {
			c.errorf(s.Pos, "ergoir: store to a non-place")
			return nil
		}
		a := &syntax.AssignStmt{Lhs: lhs, Rhs: rhs}
		if s.Op != "" {
			op, ok := ergoBinOps[s.Op]
			if !ok || op == syntax.AndAnd || op == syntax.OrOr {
				c.errorf(s.Pos, "ergoir: assignment operator %q", s.Op)
				return nil
			}
			if t := c.binType(s.Pos, op, s.Op, lt, rt); t == nil || !c.assignable(s.Pos, s.Op+"=", t, lt) {
				return nil
			}
			a.Op = op
		} else if !c.assignable(s.Pos, "store", rt, lt) {
			return nil
		}
		a.SetPos(pos)
		return a
	case "var":
		// var x T: the local's zero value.
		v := c.local(s.Pos, s.Local)
		if v == nil {
			return nil
		}
		d := &syntax.VarDecl{NameList: []*syntax.Name{c.name(s.Pos, v, true)}}
		d.SetPos(pos)
		st := &syntax.DeclStmt{DeclList: []syntax.Decl{d}}
		st.SetPos(pos)
		return st
	case "discard":
		// _ = x: x evaluated, its value dropped.
		x, t := c.expr(s.X)
		if x == nil || t == nil {
			c.errorf(s.Pos, "ergoir: discard of no value")
			return nil
		}
		a := &syntax.AssignStmt{Lhs: syntax.NewName(pos, "_"), Rhs: x}
		a.SetPos(pos)
		return a
	case "ret":
		return c.ret(s) // under the failure ABI (ergoir_fail.go)
	case "if":
		cond := c.cond(s.X)
		if cond == nil {
			return nil
		}
		// Scope positions must increase (DWARF scopes): the then block is
		// Pos..End, the else block ElsePos..ElseEnd, after it.
		st := &syntax.IfStmt{Cond: cond, Then: c.block(s.Pos, s.End, s.Then)}
		st.SetPos(pos)
		if s.Else != nil {
			st.Else = c.block(s.ElsePos, s.ElseEnd, s.Else)
		}
		return st
	case "while":
		// for cond { body }, or for { body } with no condition.
		st := &syntax.ForStmt{}
		st.SetPos(pos)
		if s.X != nil {
			if st.Cond = c.cond(s.X); st.Cond == nil {
				return nil
			}
		}
		st.Body = c.loopBlock(st, s.Label, s.Pos, s.End, s.Body)
		return c.labeled(s, st) // ergoir_label.go
	case "count":
		// for i := X; i < Y; i++ { body }: a for-in over an integer Range
		// (IRG4b), i the loop's integer local; Y is read each iteration.
		v := c.local(s.Pos, s.Local)
		lo, lt := c.expr(s.X)
		if v == nil || lo == nil || s.Y == nil {
			return nil
		}
		if !ergoIsInteger(v.Type()) || !c.assignable(s.Pos, "count", lt, v.Type()) {
			c.errorf(s.Pos, "ergoir: count of %v", v.Type())
			return nil
		}
		init := &syntax.AssignStmt{Op: syntax.Def, Lhs: c.name(s.Pos, v, true), Rhs: lo}
		init.SetPos(pos)
		cond := c.cond(&ergoExpr{K: "bin", Pos: s.Pos, Op: "<", X: &ergoExpr{K: "local", Pos: s.Pos, I: s.Local}, Y: s.Y})
		if cond == nil {
			return nil
		}
		post := &syntax.AssignStmt{Op: syntax.Add, Lhs: c.name(s.Pos, v, false)}
		post.SetPos(pos)
		st := &syntax.ForStmt{Init: init, Cond: cond, Post: post}
		st.SetPos(pos)
		st.Body = c.loopBlock(st, s.Label, s.Pos, s.End, s.Body)
		return c.labeled(s, st) // ergoir_label.go
	case "break", "continue":
		return c.branch(s) // ergoir_label.go
	case "panic":
		// panic(x), x a string: a path a checked program never takes; or
		// x an error: a failure in a frame that cannot fail, or a total
		// catch's default (ergoir_fail.go); or x a pointer to a struct: a
		// defect value, the prelude's Unreachable (ergoir_opc.go).
		x, t := c.expr(s.X)
		if x == nil || !ergoIsString(t) && !ergoIsErrorValue(t) && !ergoIsDefect(t) {
			c.errorf(s.Pos, "ergoir: panic of %v", t)
			return nil
		}
		obj := types2.Universe.Lookup("panic")
		fun := syntax.NewName(pos, "panic")
		c.info.Uses[fun] = obj
		any := types2.Universe.Lookup("any").Type()
		tv := syntax.TypeAndValue{Type: types2.NewSignatureType(nil, nil, nil,
			types2.NewTuple(types2.NewParam(pos, nil, "", any)), nil, false)}
		tv.SetIsBuiltin()
		fun.SetTypeInfo(tv)
		call := &syntax.CallExpr{Fun: fun, ArgList: []syntax.Expr{x}}
		call.SetPos(pos)
		void := syntax.TypeAndValue{}
		void.SetIsVoid()
		call.SetTypeInfo(void)
		st := &syntax.ExprStmt{X: call}
		st.SetPos(pos)
		return st
	case "range":
		x, t := c.expr(s.X)
		if x == nil {
			return nil
		}
		key, elem := ergoRangeTypes(t) // ergoir_call.go
		if key == nil {
			c.errorf(s.Pos, "ergoir: range over %v, not a slice, a map or a string", t)
			return nil
		}
		lhs := &syntax.ListExpr{}
		lhs.SetPos(pos)
		for i, want := range []types2.Type{key, elem} {
			idx := s.Key
			if i == 1 {
				idx = s.Val
			}
			if want == nil {
				continue // a range over a func has one variable
			}
			if idx == nil {
				lhs.ElemList = append(lhs.ElemList, syntax.NewName(pos, "_"))
				continue
			}
			v := c.local(s.Pos, *idx)
			if v == nil || !c.assignable(s.Pos, "range", want, v.Type()) {
				return nil
			}
			lhs.ElemList = append(lhs.ElemList, c.name(s.Pos, v, true))
		}
		if _, isFunc := types2.CoreType(t).(*types2.Signature); isFunc {
			c.rangeFunc = true // ergoir_closure.go
		}
		rc := &syntax.RangeClause{Lhs: lhs, Def: true, X: x}
		if elem == nil {
			rc.Lhs = lhs.ElemList[0]
		}
		if s.Key == nil && s.Val == nil {
			// for range x, as the text view prints it (ergoir_opc.go).
			rc = ergoBareRange(x)
		}
		rc.SetPos(pos)
		st := &syntax.ForStmt{Init: rc}
		st.SetPos(pos)
		st.Body = c.loopBlock(st, s.Label, s.Pos, s.End, s.Body)
		return c.labeled(s, st) // ergoir_label.go
	case "expr":
		if s.X == nil || (s.X.K != "call" && s.X.K != "mcall" && s.X.K != "pcall" && s.X.K != "vcall" && s.X.K != "builtin") {
			c.errorf(s.Pos, "ergoir: an expression statement is a call")
			return nil
		}
		x, t := c.expr(s.X)
		if x == nil {
			return nil
		}
		if _, isTuple := t.(*types2.Tuple); isTuple {
			c.errorf(s.Pos, "ergoir: a fallible call's error is dropped")
			return nil
		}
		st := &syntax.ExprStmt{X: x}
		st.SetPos(pos)
		return st
	}
	if st, ok := c.f7bStmt(s); ok { // ergoir_f7b.go
		return st
	}
	if st, ok := c.failStmt(s); ok { // ergoir_fail.go
		return st
	}
	if st, ok := c.unionStmt(s); ok { // ergoir_union.go
		return st
	}
	if st, ok := c.labelStmt(s); ok { // ergoir_label.go
		return st
	}
	if st, ok := c.tupleStmt(s); ok { // ergoir_tuple.go
		return st
	}
	c.errorf(s.Pos, "ergoir: unknown statement %q", s.K)
	return nil
}

func (c *ergoChecker) cond(x *ergoExpr) syntax.Expr {
	e, t := c.expr(x)
	if e == nil {
		return nil
	}
	if !ergoIsBool(t) {
		c.errorf(x.Pos, "ergoir: condition of type %v", t)
		return nil
	}
	return e
}
