package lower

// IR builder: statements, blocks, if and loops. See ir.go.

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// reserve makes v a known local in a census whose let of v did not build,
// so its uses do not report it again; outside a census it does nothing (the
// body stops).
func (b *irBuilder) reserve(v *check.Var) {
	if b.st.census == nil {
		return
	}
	if _, known := b.locals[v]; !known {
		b.locals[v] = b.next
		b.next++
	}
}

func (b *irBuilder) local(v *check.Var, id *syntax.Ident) int {
	if _, dup := b.locals[v]; dup {
		irNo("local declared twice")
	}
	if b.l.info.OwnedVars[v] != nil {
		irNo("owned local")
	}
	i := b.temp(v.Name, b.goType(v.Type, id), b.pos(id, id.Pos()))
	b.locals[v] = i
	return i
}

// temp declares a local of the IR body that no Ergo variable names: an
// if expression's result.
func (b *irBuilder) temp(name, typ string, p irPos) int {
	i := b.next
	b.next++
	b.fn.Locals = append(b.fn.Locals, irLocal{Name: name, Type: typ, Pos: p})
	return i
}

// tail is a statement list whose last statement yields the function's
// result when ret is true.
func (b *irBuilder) tail(list []syntax.Stmt, ret bool) []*irStmt {
	return b.tailTo(list, ret, nil)
}

// tailTo is tail; with to set, the last statement yields its value into
// to (an if expression's branch) instead of returning it.
func (b *irBuilder) tailTo(list []syntax.Stmt, ret bool, to *yieldTo) []*irStmt {
	var out []*irStmt
	for i, s := range list {
		last := i == len(list)-1
		b.part(func() {
			if len(b.l.info.Releases[s]) > 0 {
				irNo("statement that releases owned values")
			}
			if last && (ret || to != nil) {
				out = append(out, b.yield(s, to)...)
				return
			}
			out = append(out, b.stmt(s)...)
		})
	}
	if (ret || to != nil) && len(list) == 0 {
		irNo("empty body with a result")
	}
	return out
}

// yieldTo is where a branch's last statement puts its value: a local.
type yieldTo struct {
	local int
	t     check.Type // the local's type: a variant joins its union (ir_union.go)
}

// yield is s in result position: its value is returned, or put in to.
func (b *irBuilder) yield(s syntax.Stmt, to *yieldTo) []*irStmt {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		p := b.pos(s.X, s.X.Pos())
		_, conv := b.l.info.Implicit[s.X]
		if m, ok := unparen(s.X).(syntax.Stmt); ok && !conv {
			switch m.(type) {
			case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
				return b.matchStmt(m, true, to) // ir_match.go: each arm yields
			}
		}
		if to != nil {
			pre, x := b.value(s.X)
			x = b.assignedTo(s.X, x, to.t) // ir_union.go
			return append(pre, &irStmt{K: "set", Pos: p, Local: to.local, X: x})
		}
		return b.ret(s.X, p) // ir_fail.go
	case *syntax.ReturnStmt:
		return b.stmt(s)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return b.matchStmt(s, true, to) // ir_match.go
	case *syntax.IfStmt:
		if s.Else == nil {
			if to != nil {
				irNo("if without else in result position")
			}
			// The branch returns, or the function's end panics (bodyEnd).
			return []*irStmt{b.ifStmt(s, true, nil)}
		}
		return []*irStmt{b.ifStmt(s, true, to)}
	case *syntax.ForStmt, *syntax.RangeStmt:
		if to != nil {
			irNo("result statement %T", s)
		}
		// A loop as the last statement: the function's end panics after
		// it (bodyEnd), as the emitter's does.
		return b.stmt(s)
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

// ifStmt is s; ret says its branches yield, into to or as the result.
func (b *irBuilder) ifStmt(s *syntax.IfStmt, ret bool, to *yieldTo) *irStmt {
	cond := b.cond(s.Cond)
	st := &irStmt{K: "if", Pos: b.pos(s, s.Pos()), X: cond, End: b.pos(s.Body, s.Body.Rbrace)}
	st.Then = b.tailTo(s.Body.List, ret && to == nil, to)
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		st.ElsePos, st.ElseEnd = b.pos(e, e.Lbrace), b.pos(e, e.Rbrace)
		st.Else = b.tailTo(e.List, ret && to == nil, to)
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
		if to != nil && e.Else == nil {
			irNo("if without else in result position")
		}
		st.ElsePos, st.ElseEnd = b.pos(e, e.Pos()), b.pos(e, end)
		st.Else = []*irStmt{b.ifStmt(e, ret, to)}
	default:
		irNo("else %T", e)
	}
	return st
}

// ifValue is the if expression s in a value position: a local of its type,
// declared first, and an if whose branches yield into it.
func (b *irBuilder) ifValue(s *syntax.IfStmt) ([]*irStmt, *irExpr) {
	if s.Else == nil {
		irNo("if without else in result position")
	}
	t, ok := b.l.info.Types[s]
	if !ok {
		irNo("expression without a type")
	}
	p := b.pos(s, s.Pos())
	i := b.temp(fmt.Sprintf("if%d", b.next), b.goType(t, s), p)
	pre := []*irStmt{{K: "var", Pos: p, Local: i}, b.ifStmt(s, true, &yieldTo{local: i, t: t})}
	return pre, &irExpr{K: "local", Pos: p, I: i}
}

// discard is x evaluated for its effects and dropped (the emitter's
// discard): a call is a call statement, any other value is `_ = x`.
func (b *irBuilder) discard(x syntax.Expr) []*irStmt {
	x = unparen(x)
	p := b.pos(x, x.Pos())
	if call, ok := x.(*syntax.CallExpr); ok {
		if c := b.l.info.Calls[call]; c != nil && c.Kind != check.CallConversion && c.Kind != check.CallLen {
			pre, e := b.call(call, true)
			return append(pre, &irStmt{K: "expr", Pos: p, X: e})
		}
	}
	switch y := x.(type) {
	case *syntax.TryExpr, *syntax.CatchExpr:
		return b.failStmt(x) // ir_fail.go
	case *syntax.SwitchStmt:
		return b.matchStmt(y, false, nil) // ir_match.go
	case *syntax.TypeSwitchStmt:
		return b.matchStmt(y, false, nil) // ir_match.go
	case *syntax.FuncLit:
		irNo("expression statement %T", x)
	}
	pre, e := b.value(x)
	return append(pre, &irStmt{K: "discard", Pos: p, X: e})
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
			if p := b.l.info.Patterns[id]; p != nil && p.Kind != check.PatBind || id.Name == "_" {
				// A discard, or a known name the checker proved
				// irrefutable: the value is evaluated and dropped.
				out = append(out, b.discard(spec.Values[0])...)
				continue
			}
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok {
				irNo("let without a variable")
			}
			if b.fc.scalarOf(v) != nil {
				b.reserve(v)
				irNo("scalar-form local")
			}
			var pre []*irStmt
			var x *irExpr
			b.part(func() { pre, x = b.value(spec.Values[0]) })
			out = append(out, pre...)
			i := -1
			b.part(func() { i = b.local(v, id) })
			if i < 0 {
				b.reserve(v)
			}
			out = append(out, &irStmt{K: "def", Pos: b.pos(s.Decl, s.Decl.Pos()), Local: i, X: x})
		}
		return out
	case *syntax.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
			irNo("assignment form several")
		}
		op := ""
		if s.Tok != syntax.ASSIGN {
			text := s.Tok.String()
			op = text[:len(text)-1]
			if !irOps[op] {
				irNo("assignment form %s", text)
			}
		}
		if id, ok := s.Lhs[0].(*syntax.Ident); ok && id.Name == "_" && op == "" {
			return b.discard(s.Rhs[0])
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
		if op != "" {
			b.operator(op, b.l.info.Types[s.Lhs[0]], s)
		}
		pre, x := b.value(s.Rhs[0])
		if op != "" && len(pre) > 0 {
			// The emitter reads the target before a value that writes
			// statements (assign's old); the IR has no such read yet.
			irNo("op-assignment of a value with statements")
		}
		return append(pre, &irStmt{K: "set", Pos: b.pos(s, s.Pos()), Local: i, X: x, Op: op})
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
		return b.discard(s.X)
	case *syntax.IfStmt:
		return []*irStmt{b.ifStmt(s, false, nil)}
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return b.matchStmt(s, false, nil) // ir_match.go
	case *syntax.ForStmt:
		var cond *irExpr
		if s.Cond != nil {
			cond = b.cond(s.Cond)
		}
		return []*irStmt{{K: "while", Pos: b.pos(s, s.Pos()), X: cond, Body: b.loopBody(s, s.Body), End: b.pos(s.Body, s.Body.Rbrace)}}
	case *syntax.BranchStmt:
		if b.loops == 0 {
			irNo("break or continue outside a loop")
		}
		switch s.Tok {
		case syntax.BREAK:
			if b.switches > 0 {
				irNo("break inside a match") // ir_match.go: Go's break would leave the switch
			}
			return []*irStmt{{K: "break", Pos: b.pos(s, s.Pos())}}
		case syntax.CONTINUE:
			return []*irStmt{{K: "continue", Pos: b.pos(s, s.Pos())}}
		}
		irNo("statement *syntax.BranchStmt %s", s.Tok)
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
			i := -1
			b.part(func() { i = b.local(v, id) })
			if i < 0 {
				b.reserve(v)
				return nil
			}
			return &i
		}
		b.part(func() { st.Key = bind(s.Key) })
		b.part(func() { st.Val = bind(s.Value) })
		st.Body = b.loopBody(s, s.Body)
		return []*irStmt{st}
	}
	irNo("statement %T", s)
	return nil
}

// loopBody is loop s's body, where break and continue stand, then its
// back-edge poll in a polling frame (loopPoll, ir_fail.go).
func (b *irBuilder) loopBody(s syntax.Stmt, body *syntax.BlockStmt) []*irStmt {
	b.loops++
	switches := b.switches
	b.switches = 0
	defer func() { b.loops, b.switches = b.loops-1, switches }()
	return append(b.tail(body.List, false), b.loopPoll(s, body)...)
}

// bodyEnd is what follows a body with a result whose last statement may
// complete (a loop, an if without else): the emitter's panic, which a
// checked program never reaches (fc.body).
func (b *irBuilder) bodyEnd(list []syntax.Stmt, end irPos) []*irStmt {
	if b.result == nil || len(list) == 0 {
		return nil
	}
	switch s := list[len(list)-1].(type) {
	case *syntax.ExprStmt, *syntax.ReturnStmt:
		return nil
	case *syntax.IfStmt:
		if s.Else != nil && !b.completes(s) {
			return nil
		}
	}
	msg := &irExpr{K: "const", Pos: end, T: "string", V: "ergo: unreachable: the function's end"}
	return []*irStmt{{K: "panic", Pos: end, X: msg}}
}

// completes reports an if chain in result position with a branch whose
// last statement may complete (a loop, a one-armed if).
func (b *irBuilder) completes(s *syntax.IfStmt) bool {
	lastCompletes := func(list []syntax.Stmt) bool {
		if len(list) == 0 {
			return true
		}
		switch l := list[len(list)-1].(type) {
		case *syntax.ExprStmt, *syntax.ReturnStmt:
			return false
		case *syntax.IfStmt:
			return l.Else == nil || b.completes(l)
		}
		return true
	}
	if lastCompletes(s.Body.List) {
		return true
	}
	switch e := s.Else.(type) {
	case *syntax.BlockStmt:
		return lastCompletes(e.List)
	case *syntax.IfStmt:
		return e.Else == nil || b.completes(e)
	}
	return true
}
