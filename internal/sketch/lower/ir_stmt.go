package lower

// IR builder: statements, blocks, if and loops. See ir.go.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) local(v *check.Var, id *syntax.Ident) int {
	if _, dup := b.locals[v]; dup {
		irNo("local declared twice")
	}
	i := b.next
	b.next++
	b.locals[v] = i
	b.fn.Locals = append(b.fn.Locals, irLocal{Name: v.Name, Type: b.goType(v.Type, id), Pos: b.pos(id, id.Pos())})
	return i
}

// tail is a statement list whose last statement yields the function's
// result when ret is true.
func (b *irBuilder) tail(list []syntax.Stmt, ret bool) []*irStmt {
	var out []*irStmt
	for i, s := range list {
		last := i == len(list)-1
		b.part(func() {
			if last && ret {
				out = append(out, b.yield(s)...)
				return
			}
			out = append(out, b.stmt(s)...)
		})
	}
	if ret && len(list) == 0 {
		irNo("empty body with a result")
	}
	return out
}

// yield is s in result position.
func (b *irBuilder) yield(s syntax.Stmt) []*irStmt {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		return b.ret(s.X, b.pos(s.X, s.X.Pos())) // ir_fail.go
	case *syntax.ReturnStmt:
		return b.stmt(s)
	case *syntax.IfStmt:
		if s.Else == nil {
			irNo("if without else in result position")
		}
		return []*irStmt{b.ifStmt(s, true)}
	}
	irNo("result statement %T", s)
	return nil
}

// cond is a loop's or an if's condition, which runs no operation call.
func (b *irBuilder) cond(x syntax.Expr) (cond *irExpr) {
	b.part(func() {
		pre, c := b.value(x)
		if len(pre) > 0 {
			irNo("operation call in a condition")
		}
		cond = c
	})
	return cond
}

func (b *irBuilder) ifStmt(s *syntax.IfStmt, ret bool) *irStmt {
	cond := b.cond(s.Cond)
	st := &irStmt{K: "if", Pos: b.pos(s, s.Pos()), X: cond, End: b.pos(s.Body, s.Body.Rbrace)}
	st.Then = b.tail(s.Body.List, ret)
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		st.ElsePos, st.ElseEnd = b.pos(e, e.Lbrace), b.pos(e, e.Rbrace)
		st.Else = b.tail(e.List, ret)
	case *syntax.IfStmt:
		// The else block of an else-if wraps the chain to its last brace.
		last := syntax.Stmt(e)
		for {
			n, ok := last.(*syntax.IfStmt)
			if !ok || n.Else == nil {
				break
			}
			last = n.Else
		}
		end := e.Body.Rbrace
		switch l := last.(type) {
		case *syntax.BlockStmt:
			end = l.Rbrace
		case *syntax.IfStmt:
			end = l.Body.Rbrace
		}
		st.ElsePos, st.ElseEnd = b.pos(e, e.Pos()), b.pos(e, end)
		st.Else = []*irStmt{b.ifStmt(e, ret)}
	default:
		irNo("else %T", e)
	}
	return st
}

func (b *irBuilder) stmt(s syntax.Stmt) []*irStmt {
	switch s := s.(type) {
	case *syntax.DeclStmt:
		var out []*irStmt
		for _, spec := range s.Decl.Specs {
			if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
				irNo("let pattern")
			}
			id := spec.Names[0]
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok || id.Name == "_" {
				irNo("let without a variable")
			}
			if p := b.l.info.Patterns[id]; p != nil && p.Kind != check.PatBind {
				irNo("let pattern test")
			}
			if b.fc.scalarSet[v] {
				irNo("scalar-form local")
			}
			var pre []*irStmt
			var x *irExpr
			b.part(func() { pre, x = b.value(spec.Values[0]) })
			out = append(out, pre...)
			out = append(out, &irStmt{K: "def", Pos: b.pos(s.Decl, s.Decl.Pos()), Local: b.local(v, id), X: x})
		}
		return out
	case *syntax.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Tok != syntax.ASSIGN {
			irNo("assignment form %s", s.Tok)
		}
		id, ok := s.Lhs[0].(*syntax.Ident)
		if !ok {
			irNo("assignment to a place")
		}
		v, ok := b.l.info.Uses[id].(*check.Var)
		i, known := b.locals[v]
		if !ok || !known {
			irNo("assignment to a non-local")
		}
		pre, x := b.value(s.Rhs[0])
		return append(pre, &irStmt{K: "set", Pos: b.pos(s, s.Pos()), Local: i, X: x})
	case *syntax.ReturnStmt:
		p := b.pos(s, s.Pos())
		if len(s.Results) == 0 {
			return b.ret(nil, p) // ir_fail.go
		}
		if len(s.Results) != 1 {
			irNo("several results")
		}
		return b.ret(s.Results[0], p) // ir_fail.go
	case *syntax.ExprStmt:
		switch unparen(s.X).(type) {
		case *syntax.TryExpr, *syntax.CatchExpr:
			return b.failStmt(s.X) // ir_fail.go
		}
		call, ok := unparen(s.X).(*syntax.CallExpr)
		if !ok {
			irNo("expression statement that is not a call")
		}
		pre, x := b.call(call, true)
		return append(pre, &irStmt{K: "expr", Pos: b.pos(s.X, s.X.Pos()), X: x})
	case *syntax.IfStmt:
		return []*irStmt{b.ifStmt(s, false)}
	case *syntax.ForStmt:
		if s.Cond == nil {
			irNo("for without a condition")
		}
		cond := b.cond(s.Cond)
		body := append(b.tail(s.Body.List, false), b.loopPoll(s, s.Body)...) // ir_fail.go
		return []*irStmt{{K: "while", Pos: b.pos(s, s.Pos()), X: cond, Body: body, End: b.pos(s.Body, s.Body.Rbrace)}}
	case *syntax.RangeStmt:
		lp := b.l.info.Loops[s]
		if lp == nil || lp.Domain != check.LoopSlice {
			b.gate("for-in over a non-slice")
		}
		var x *irExpr
		b.part(func() {
			var pre []*irStmt
			pre, x = b.value(s.X)
			if len(pre) > 0 {
				irNo("operation call in a range")
			}
		})
		st := &irStmt{K: "range", Pos: b.pos(s, s.Pos()), X: x, End: b.pos(s.Body, s.Body.Rbrace)}
		bind := func(e syntax.Expr) *int {
			if !bound(e) {
				return nil
			}
			id, ok := e.(*syntax.Ident)
			if !ok {
				irNo("for-in pattern")
			}
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok {
				irNo("for-in binding without a variable")
			}
			i := b.local(v, id)
			return &i
		}
		b.part(func() { st.Key = bind(s.Key) })
		b.part(func() { st.Val = bind(s.Value) })
		st.Body = append(b.tail(s.Body.List, false), b.loopPoll(s, s.Body)...) // ir_fail.go
		return []*irStmt{st}
	}
	irNo("statement %T", s)
	return nil
}
