package lower

// IR builder: the failure ABI (IRG2). See ir.go.
//
// A fallible function's Go form is the emitter's: it returns (T, error),
// or error with no value (decls.go, resultList). The IR spells only what
// the emitter's body does with it:
//
//   - "ret" is the success return; the patched compiler writes the nil
//     error (a return of a fallible call's whole result passes it on).
//   - "fail" leaves with the frame's zero value and an error (fail).
//   - try binds a fallible call's value and error ("def2") and fails with
//     the error when it is not nil (try, propagate).
//   - catch is the emitter's type switch on the error ("tswitch"): each
//     arm a case, `T as e` an assertion of the case's type, the default a
//     failure under try and a panic in a total catch (catch).
//
// Operands run once, left to right; an operand before one that runs
// statements is evaluated first, into a temporary (callArgs, spill).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irCase is one clause of a "tswitch": the case's Go types and its body.
type irCase struct {
	Pos   irPos     `json:"pos"`
	Types []string  `json:"types"`
	Body  []*irStmt `json:"body,omitempty"`
}

// fallibleCall is x, unparenthesized, when it is a call that can fail at
// its site.
func (b *irBuilder) fallibleCall(x syntax.Expr) *syntax.CallExpr {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	c := b.l.info.Calls[call]
	if c == nil || !failsHere(c) {
		return nil
	}
	return call
}

// ret is the return of x (nil: a bare return) at p, as the emitter's
// retExpr: a value of an error type is a failure; an untried fallible
// call returns its value and error as they are.
func (b *irBuilder) ret(x syntax.Expr, p irPos) []*irStmt {
	if x == nil {
		if b.result != nil {
			irNo("bare return")
		}
		return []*irStmt{{K: "ret", Pos: p}}
	}
	if call := b.fallibleCall(x); call != nil {
		if _, implicit := b.l.info.Implicit[x]; implicit {
			irNo("implicit conversion")
		}
		pre, e := b.rawCall(call)
		return append(pre, &irStmt{K: "ret", Pos: p, X: e})
	}
	if b.isFailure(x) {
		pre, e := b.value(x)
		k := "panic"
		if b.fc.frame.fallible {
			k = "fail"
		}
		return append(pre, &irStmt{K: k, Pos: p, X: e})
	}
	if b.result == nil {
		// A no-value call returned for its effect (retExpr).
		return append(b.discard(x), &irStmt{K: "ret", Pos: p})
	}
	pre, e := b.value(x)
	return append(pre, &irStmt{K: "ret", Pos: p, X: e})
}

// isFailure reports a value x of an error type that the frame does not
// return as its success value: returned, it fails the frame (retExpr).
func (b *irBuilder) isFailure(x syntax.Expr) bool {
	pkg := b.l.bodyPkg()
	t := b.l.info.Types[x]
	return t != nil && b.fallibleCall(x) == nil && b.l.isErrorValue(t, pkg, pkg.OwnerOf(x)) && !b.l.info.ReturnedValues[x]
}

// bodyStmts is a function body. Every body's last statement is its tail
// (the emitter's yieldList with destReturn), with or without a value: in
// a body with no value, a tail that is an untried fallible call returns
// its error, and a tail of an error value fails the frame (or panics in
// one that cannot fail).
func (b *irBuilder) bodyStmts(list []syntax.Stmt) []*irStmt {
	n := len(list)
	if b.result != nil || n == 0 {
		return b.tail(list, b.result != nil)
	}
	out := b.tail(list[:n-1], false)
	b.part(func() {
		if es, ok := list[n-1].(*syntax.ExprStmt); ok && (b.fallibleCall(es.X) != nil || b.isFailure(es.X)) {
			out = append(out, b.ret(es.X, b.pos(es.X, es.X.Pos()))...)
			return
		}
		if b.failureTail(list[n-1], false) {
			irNo("failure in a branch tail of a body with no value")
		}
		out = append(out, b.stmt(list[n-1])...)
	})
	return out
}

// failureTail reports a branch of s whose tail is an error value: in a
// body with no value it fails the frame (the emitter's ifYield), which
// the builder does not spell yet. nested is s inside a branch.
func (b *irBuilder) failureTail(s syntax.Stmt, nested bool) bool {
	last := func(list []syntax.Stmt) bool {
		return len(list) > 0 && b.failureTail(list[len(list)-1], true)
	}
	switch s := s.(type) {
	case *syntax.ExprStmt:
		return nested && b.isFailure(s.X)
	case *syntax.BlockStmt:
		return last(s.List)
	case *syntax.IfStmt:
		return last(s.Body.List) || (s.Else != nil && b.failureTail(s.Else, true))
	case *syntax.SwitchStmt:
		for _, c := range s.Clauses {
			if last(c.Body) {
				return true
			}
		}
	case *syntax.TypeSwitchStmt:
		for _, c := range s.Clauses {
			if last(c.Body) {
				return true
			}
		}
	}
	return false
}

// failValue is a try or a catch in value position.
func (b *irBuilder) failValue(x syntax.Expr) ([]*irStmt, *irExpr) {
	pre, v := b.failExpr(x)
	if v == nil {
		irNo("try without a value")
	}
	return pre, v
}

// failStmt is a try or a catch in statement position: its value, if any,
// is dropped.
func (b *irBuilder) failStmt(x syntax.Expr) []*irStmt {
	pre, _ := b.failExpr(x)
	return pre
}

func (b *irBuilder) failExpr(x syntax.Expr) ([]*irStmt, *irExpr) {
	switch x := unparen(x).(type) {
	case *syntax.TryExpr:
		b.tries++
		defer func() { b.tries-- }()
		if c, ok := unparen(x.X).(*syntax.CatchExpr); ok {
			return b.catch(c, true)
		}
		call := b.fallibleCall(x.X)
		if call == nil {
			// try covers the whole expression: its operands' fallible
			// calls fail the frame (operand).
			if _, isCall := unparen(x.X).(*syntax.CallExpr); !isCall {
				irNo("try of a non-call")
			}
			return b.value(x.X)
		}
		return b.try(call)
	case *syntax.CatchExpr:
		return b.catch(x, false)
	}
	irNo("expression %T", x)
	return nil, nil
}

// try is try call: its value, after a failure leaves the frame.
func (b *irBuilder) try(call *syntax.CallExpr) ([]*irStmt, *irExpr) {
	pre, v, e := b.bind(call)
	p := b.pos(call, call.Pos())
	pre = append(pre, b.propagate(e, p))
	if v < 0 {
		return pre, nil
	}
	return pre, &irExpr{K: "local", Pos: p, I: v}
}

// bind evaluates the fallible call into a value local (-1: none) and an
// error local.
func (b *irBuilder) bind(call *syntax.CallExpr) (pre []*irStmt, v, e int) {
	pre, x := b.rawCall(call)
	p := b.pos(call, call.Pos())
	e = b.temp("err", "error", p)
	t := b.l.info.Types[call]
	if t == nil || isNoValue(t) {
		return append(pre, &irStmt{K: "def", Pos: p, Local: e, X: x}), -1, e
	}
	v = b.temp("v", b.goType(t, call), p)
	return append(pre, &irStmt{K: "def2", Pos: p, Local: v, Val: &e, X: x}), v, e
}

// isNoValue reports the type of a call with no value.
func isNoValue(t check.Type) bool {
	if tu, ok := t.(*check.Tuple); ok && len(tu.Types) == 0 {
		return true
	}
	return t == check.NoValue
}

// propagate is `if e != nil { fail e }` (the emitter's propagate).
func (b *irBuilder) propagate(e int, p irPos) *irStmt {
	errLocal := &irExpr{K: "local", Pos: p, I: e}
	k := "fail"
	if !b.fc.frame.fallible {
		k = "panic"
	}
	return &irStmt{K: "if", Pos: p, End: p, X: b.notNil(e, p), Then: []*irStmt{{K: k, Pos: p, X: errLocal}}}
}

func (b *irBuilder) notNil(e int, p irPos) *irExpr {
	return &irExpr{K: "bin", Pos: p, Op: "!=", X: &irExpr{K: "local", Pos: p, I: e}, Y: &irExpr{K: "nil", Pos: p, T: "error"}}
}

// catch is x catch { arms }: the emitter's type switch on the call's
// error (catch). partial marks try … catch.
func (b *irBuilder) catch(x *syntax.CatchExpr, partial bool) ([]*irStmt, *irExpr) {
	m := b.l.info.Matches[x]
	if m == nil || m.Partial != partial {
		irNo("catch without its Matches entry")
	}
	call := b.fallibleCall(x.X)
	if call == nil {
		irNo("catch of a non-call")
	}
	type arm struct {
		types []string
		as    *check.Var
		asT   string
		pos   irPos
		body  []syntax.Stmt
	}
	var arms []arm
	for _, a := range x.Arms {
		ar := arm{body: a.Body, pos: b.pos(x, a.Case)}
		for _, te := range a.Types {
			p := b.l.info.Patterns[te]
			if p == nil {
				irNo("catch arm without a pattern")
			}
			switch {
			case p.Kind == check.PatTypeTest:
				ar.types = append(ar.types, b.goType(p.Type, te))
			case p.Kind == check.PatAs && len(a.Types) == 1 && len(p.Elems) == 1 && p.Elems[0].Kind == check.PatTypeTest && p.Var != nil:
				ar.asT = b.goType(p.Elems[0].Type, te)
				ar.types = append(ar.types, ar.asT)
				ar.as = p.Var
			default:
				irNo("catch with patterns")
			}
		}
		arms = append(arms, ar)
	}
	pre, v, e := b.bind(call)
	p := b.pos(x, x.Catch)
	result := -1
	var rt string
	if m.Result != nil {
		rt = b.goType(m.Result, x)
		if v >= 0 {
			if ct := b.l.info.Types[call]; ct == nil || b.goType(ct, call) != rt {
				irNo("catch value of another type")
			}
		}
		result = b.temp("caught", rt, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: result, X: &irExpr{K: "zero", Pos: p, T: rt}})
	}
	sw := &irStmt{K: "tswitch", Pos: p, End: b.pos(x, x.Rbrace), X: &irExpr{K: "local", Pos: p, I: e}}
	for i, a := range arms {
		c := &irCase{Pos: a.pos, Types: a.types}
		if a.as != nil {
			name := b.temp(a.as.Name, a.asT, a.pos)
			b.locals[a.as] = name
			c.Body = append(c.Body, &irStmt{K: "def", Pos: a.pos, Local: name, X: &irExpr{K: "assert", Pos: a.pos, T: a.asT, X: &irExpr{K: "local", Pos: a.pos, I: e}}})
		}
		c.Body = append(c.Body, b.armBody(x.Arms[i], result)...)
		sw.Cases = append(sw.Cases, c)
	}
	sw.ElsePos = b.pos(x, x.Rbrace)
	k := "panic"
	if partial {
		k = "fail"
		if !b.fc.frame.fallible {
			k = "panic"
		}
	}
	sw.Else = []*irStmt{{K: k, Pos: sw.ElsePos, X: &irExpr{K: "local", Pos: sw.ElsePos, I: e}}}
	ifs := &irStmt{K: "if", Pos: p, End: sw.End, X: b.notNil(e, p), Then: []*irStmt{sw}}
	if result >= 0 && v >= 0 {
		ifs.ElsePos, ifs.ElseEnd = sw.End, sw.End
		ifs.Else = []*irStmt{{K: "set", Pos: sw.End, Local: result, X: &irExpr{K: "local", Pos: sw.End, I: v}}}
	}
	pre = append(pre, ifs)
	if result < 0 {
		return pre, nil
	}
	return pre, &irExpr{K: "local", Pos: p, I: result}
}

// armBody is a catch arm's statements; its last expression is the
// catch's value when the catch has one (result >= 0), else dropped.
func (b *irBuilder) armBody(a *syntax.CatchArm, result int) []*irStmt {
	list := a.Body
	if len(list) == 0 {
		if result >= 0 {
			irNo("catch arm form")
		}
		return nil
	}
	last, isExpr := list[len(list)-1].(*syntax.ExprStmt)
	if !isExpr {
		if result >= 0 {
			if _, isRet := list[len(list)-1].(*syntax.ReturnStmt); !isRet {
				irNo("catch arm form")
			}
		}
		return b.tail(list, false)
	}
	out := b.tail(list[:len(list)-1], false)
	if result < 0 {
		if _, isCall := unparen(last.X).(*syntax.CallExpr); !isCall {
			irNo("catch arm form")
		}
		return append(out, b.tail(list[len(list)-1:], false)...)
	}
	b.part(func() {
		pre, v := b.value(last.X)
		out = append(append(out, pre...), &irStmt{K: "set", Pos: b.pos(last.X, last.X.Pos()), Local: result, X: v})
	})
	return out
}

// operand is one operand of a call: a try, a catch, a call, or an
// expression; under a try, a fallible call fails the frame too (the
// emitter's tries).
func (b *irBuilder) operand(x syntax.Expr) ([]*irStmt, *irExpr) {
	switch u := unparen(x).(type) {
	case *syntax.TryExpr, *syntax.CatchExpr:
		return b.failValue(u)
	case *syntax.CallExpr:
		if b.tries > 0 && b.fallibleCall(u) != nil {
			pre, v := b.try(u)
			if v == nil {
				irNo("try without a value")
			}
			return pre, v
		}
		if _, implicit := b.l.info.Implicit[x]; implicit {
			irNo("implicit conversion")
		}
		return b.call(u, false)
	}
	return nil, b.expr(x)
}

// operands evaluates list left to right (the emitter's callArgs): an
// operand before one that runs statements is spilled to a temporary, so
// it is evaluated first. A constant or a local needs no spill: no
// statement IR writes before a later operand assigns a local.
func (b *irBuilder) operands(list []syntax.Expr) ([]*irStmt, []*irExpr) {
	pres := make([][]*irStmt, len(list))
	vals := make([]*irExpr, len(list))
	for i, a := range list {
		pres[i], vals[i] = b.operand(a)
	}
	var pre []*irStmt
	for i, a := range list {
		pre = append(pre, pres[i]...)
		later := false
		for _, p := range pres[i+1:] {
			later = later || len(p) > 0
		}
		if later && vals[i].K != "const" && vals[i].K != "local" {
			p := vals[i].Pos
			t := b.temp("arg", b.goType(b.l.info.Types[a], a), p)
			pre = append(pre, &irStmt{K: "def", Pos: p, Local: t, X: vals[i]})
			vals[i] = &irExpr{K: "local", Pos: p, I: t}
		}
	}
	return pre, vals
}

// hoist binds an effectful call's value to a temporary, so it runs here,
// in order, as the emitter's call does (v := f(…)).
func (b *irBuilder) hoist(pre []*irStmt, x *irExpr, call *syntax.CallExpr) ([]*irStmt, *irExpr) {
	t := b.l.info.Types[call]
	if t == nil || isNoValue(t) {
		return pre, x
	}
	p := x.Pos
	v := b.temp("v", b.goType(t, call), p)
	return append(pre, &irStmt{K: "def", Pos: p, Local: v, X: x}), &irExpr{K: "local", Pos: p, I: v}
}

// loopPoll is the back-edge poll that ends each iteration of a loop in a
// polling frame (the emitter's loopBody). A continue would skip it.
func (b *irBuilder) loopPoll(s syntax.Stmt, body *syntax.BlockStmt) []*irStmt {
	if b.env < 0 || !b.fc.frame.polls {
		return nil
	}
	if b.fc.containsContinue(body) {
		b.gate("continue in a polling loop")
	}
	return []*irStmt{b.poll(s)}
}

// containsContinue reports a continue in body, outside a function
// literal: a nested loop's counts too (conservative: a labelled continue
// may leave the nested loop).
func (fc *funcCtx) containsContinue(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.BranchStmt:
			if n.Tok == syntax.CONTINUE {
				found = true
			}
		}
		return !found
	})
	return found
}
