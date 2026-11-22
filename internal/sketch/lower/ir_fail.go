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
	// Vals are a "switch" case's constants (ir_match.go); Def marks the
	// default clause in its source place (no types, no values).
	Vals []*irExpr `json:"vals,omitempty"`
	Def  bool      `json:"def,omitempty"`
}

// tempNames is the namer of a body's temporaries: below the function's Go
// scope (its parameters, the package, Go's reserved names), with every
// identifier the body spells taken, so a temporary captures no source
// local, whatever the order they are declared in.
func (b *irBuilder) tempNames(body *syntax.BlockStmt) *namer {
	n := newNamer(b.fc.scope)
	syntax.Inspect(body, func(x syntax.Node) bool {
		if id, ok := x.(*syntax.Ident); ok {
			n.used[id.Name] = true
		}
		return true
	})
	return n
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
	defer b.returning(x)() // ir_own.go
	out := b.retOf(x, p)
	if b.law != nil {
		out = b.lawResult(x, out) // ir_assert.go
	}
	return out
}

// retOf is ret's statements before a law's check.
func (b *irBuilder) retOf(x syntax.Expr, p irPos) []*irStmt {
	if x == nil {
		if b.result != nil {
			irNo("bare return")
		}
		return []*irStmt{{K: "ret", Pos: p}}
	}
	if call := b.fallibleCall(x); call != nil {
		if _, implicit := b.l.info.Implicit[x]; implicit {
			// The value converts (a variant into the result's union):
			// the call's failure leaves first, then the value returns.
			pre, v := b.try(call)
			return append(pre, &irStmt{K: "ret", Pos: p, X: b.implicit(x, v)})
		}
		if c := b.l.info.Calls[call]; c != nil && (c.Kind == check.CallFuncValue || b.unitPair(call) || b.fc.frame.unit) && b.result == nil {
			// A function value's pair, a generic's whose result is no
			// value (unitPair), or any fallible call a literal with no
			// value returns (its Go form is (struct{}, error), the call's
			// is not: G6): its failure leaves, then the frame returns with
			// no value (ir_closure.go, unitReturns).
			pre, _ := b.try(call)
			return append(pre, &irStmt{K: "ret", Pos: p})
		}
		if b.hostFails(call) {
			// A Go call's error becomes go.Error (bind) before it
			// leaves, then the value returns.
			pre, v := b.try(call)
			if v == nil {
				return append(pre, &irStmt{K: "ret", Pos: p})
			}
			return append(pre, &irStmt{K: "ret", Pos: p, X: v})
		}
		if out, ok := b.hostRet(call, p); ok { // ir_golib.go: a fallible Go call
			return out
		}
		pre, e := b.rawCall(call)
		return append(pre, &irStmt{K: "ret", Pos: p, X: e, Pair: true})
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
	e = b.assignedTo(x, e, b.result) // a variant joins the result's union (ir_union.go)
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
	// A body of its own (a function literal's too) starts out of any
	// enclosing tail.
	defer func(saved bool) { b.voidTail = saved }(b.voidTail)
	b.voidTail = false
	if b.result != nil || n == 0 {
		return b.tail(list, b.result != nil)
	}
	out := b.tail(list[:n-1], false)
	b.part(func() {
		s := list[n-1]
		if es, ok := s.(*syntax.ExprStmt); ok && (b.fallibleCall(es.X) != nil || b.isFailure(es.X)) {
			// It returns: the frame's exit releases what the statement
			// owns (releasesAtExit's rule for a return).
			if b.own != nil {
				delete(b.own.keys, s)
			}
			out = append(out, b.ret(es.X, b.pos(es.X, es.X.Pos()))...)
			return
		}
		// A branch whose tail is an error value fails the frame there
		// (tailTo, voidTail).
		b.voidTail = b.failureTail(s, false)
		// The statement's own releases (R43), as tailTo's scoped: its
		// temporaries end with it, before the frame's exit.
		out = append(out, b.scoped(s, b.stmt(s), b.pos(s, s.Pos()))...) // ir_own.go
		b.voidTail = false
	})
	return out
}

// failureTail reports a branch of s whose tail is an error value: in a
// body with no value it fails the frame (the emitter's ifYield; tailTo
// returns it under voidTail). nested is s inside a branch.
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
			// try covers the whole expression: each fallible call in it
			// fails the frame where it runs (call, operand), as the
			// emitter's try does.
			return b.value(x.X)
		}
		pre, v := b.try(call)
		if v != nil {
			// The call's value, a temporary its statement owns (R43),
			// registers once the failure has left (ownTemp, ir_own.go).
			v = b.ownTemp(call, v)
		}
		return pre, v
	case *syntax.CatchExpr:
		return b.catch(x, false)
	}
	irNo("expression %T", x)
	return nil, nil
}

// try is try call: its value, after a failure leaves the frame.
func (b *irBuilder) try(call *syntax.CallExpr) ([]*irStmt, *irExpr) {
	if pre, val, ok := b.unfoldAt(call); ok {
		return pre, val // a fallible law's right side, which cannot fail (ir_unfold.go)
	}
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
	if w, ok := b.isWith(call); ok {
		return b.failWith(w) // ir_with.go
	}
	if pre, v, e, ok := b.hostBind(call); ok { // ir_golib.go: a fallible Go call
		return pre, v, e
	}
	if upre, val, ok := b.unfoldAt(call); ok {
		// A fallible law's right side, which cannot fail: its error is
		// nil (ir_unfold.go).
		p := b.pos(call, call.Pos())
		e = b.temp("err", "error", p)
		upre = append(upre, &irStmt{K: "var", Pos: p, Local: e})
		v = -1
		if t := b.l.info.Types[call]; t != nil && !isNoValue(t) {
			v = b.temp("v", b.goType(t, call), p)
			upre = append(upre, &irStmt{K: "def", Pos: p, Local: v, X: val})
		}
		return upre, v, e
	}
	pre, x := b.rawCall(call)
	p := b.pos(call, call.Pos())
	e = b.temp("err", "error", p)
	t := b.l.info.Types[call]
	if (x.K == "vcall" || b.unitPair(call)) && (t == nil || isNoValue(t) || b.fc.noValue(call)) {
		// A function value with no value returns struct{} first
		// (ir_closure.go), as a generic whose result is no value does
		// (unitPair).
		u := b.temp("unit", "struct{}", p)
		return append(pre, &irStmt{K: "def2", Pos: p, Local: u, Val: &e, X: x}), -1, e
	}
	v = -1
	if t == nil || isNoValue(t) {
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: e, X: x})
	} else {
		v = b.temp("v", b.goType(t, call), p)
		pre = append(pre, &irStmt{K: "def2", Pos: p, Local: v, Val: &e, X: x})
	}
	if b.rawErr != nil && b.rawErr == call {
		// A plain catch tests Go's error against nil alone, which the
		// conversion keeps: the emitter's goErrorOf with fc.rawErr.
		b.rawErr = nil
	} else if b.hostFails(call) {
		// A Go call's error is go.Error: the emitter's hostFallible and
		// goErrorOf (v1.GoErrorOf; the eager form in the reference).
		name := "GoErrorOf"
		if goErrorEager {
			name = "GoErrorEagerOf"
		}
		b.keepImport("var _ = " + b.l.rt() + "." + name)
		conv := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, Args: []*irExpr{{K: "local", Pos: p, I: e}}}
		pre = append(pre, &irStmt{K: "set", Pos: p, Local: e, X: conv})
	}
	return pre, v, e
}

// hostFails reports the call x of a Go function that fails go.Error (a
// user extern or import go): its Go error is converted where it is bound
// (bind), never passed on raw (SP4).
func (b *irBuilder) hostFails(x *syntax.CallExpr) bool {
	c := b.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || !c.Func.Sig.Fallible {
		return false
	}
	return userExtern(c.Func) || c.Func.Pkg != nil && c.Func.Pkg.Host()
}

// bindMatch evaluates x, the match m whose arms' tails are fallible calls,
// into a value local (-1: none) and an error local, nil unless an arm's
// call fails: each arm's tail puts its call's value and error in them, as
// a fallible with's block does (failYield, ir_with.go), the emitter's
// catch of an expression (asked).
func (b *irBuilder) bindMatch(x syntax.Expr, m syntax.Stmt) (pre []*irStmt, v, e int) {
	p := b.pos(x, x.Pos())
	e = b.temp("err", "error", p)
	pre = []*irStmt{{K: "var", Pos: p, Local: e}}
	v = -1
	t := b.l.info.Types[x]
	if t != nil && !isNoValue(t) {
		v = b.temp("v", b.goType(t, x), p)
		pre = append(pre, &irStmt{K: "var", Pos: p, Local: v})
	}
	return append(pre, b.matchStmt(m, true, &yieldTo{local: v, t: t, fails: true, err: e})...), v, e // ir_match.go
}

// unitPair reports a call of a function whose declared result is a type
// parameter: Go returns that parameter's value, struct{} when the call
// has no value (term.Raw[T] at unit: (struct{}, error)), so a fallible
// one is a pair, as a function value's is.
func (b *irBuilder) unitPair(call *syntax.CallExpr) bool {
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || len(c.Func.Sig.Results) != 1 {
		return false
	}
	_, ok := c.Func.Sig.Results[0].(*check.TypeParam)
	return ok
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
	// The caught value: a fallible call, or a match whose arms' tails
	// fail (bindMatch).
	call := b.fallibleCall(x.X)
	var src syntax.Expr = call
	bindSrc := func() ([]*irStmt, int, int) { return b.bind(call) }
	if call == nil {
		m, ok := unparen(x.X).(syntax.Stmt)
		switch m.(type) {
		case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		default:
			ok = false
		}
		if _, conv := b.l.info.Implicit[x.X]; !ok || conv {
			irNo("catch of a non-call")
		}
		src = x.X
		bindSrc = func() ([]*irStmt, int, int) { return b.bindMatch(x.X, m) }
	}
	type arm struct {
		types []string
		as    *check.Var
		asT   string
		pos   irPos
		body  []syntax.Stmt
	}
	var arms []arm
	// A catch with record patterns, `case err:` or `case _` takes the
	// emitter's match tree on the error (patternCatch; ir_tree.go).
	var alts [][]*check.Pattern
	patterned := false
	for _, a := range x.Arms {
		ar := arm{body: a.Body, pos: b.pos(x, a.Case)}
		var as []*check.Pattern
		for _, te := range a.Types {
			p := b.l.info.Patterns[te]
			if p == nil {
				irNo("catch arm without a pattern")
			}
			as = append(as, p)
			switch {
			case p.Kind == check.PatTypeTest:
				ar.types = append(ar.types, b.goType(p.Type, te))
			case p.Kind == check.PatAs && len(a.Types) == 1 && len(p.Elems) == 1 && p.Elems[0].Kind == check.PatTypeTest && p.Var != nil:
				ar.asT = b.goType(p.Elems[0].Type, te)
				ar.types = append(ar.types, ar.asT)
				ar.as = p.Var
			default:
				patterned = true
			}
		}
		arms = append(arms, ar)
		alts = append(alts, as)
	}
	// A plain catch (catch_plain.go: one arm of one type test around a
	// Go call, whose only failure is go.Error) whose arm binds nothing
	// takes Go's error as it is: no go.Error box, no type switch, hand
	// Go's `if err != nil` (GOERR-BOX; SetCatchSwitchForTest keeps the
	// switch as the reference).
	plain := call != nil && !patterned && b.hostFails(call) && b.fc.plainCatch(x, m) && catchBinding(b.l.info, x) == nil
	if plain {
		b.rawErr = call
	}
	pre, v, e := bindSrc()
	b.rawErr = nil
	p := b.pos(x, x.Catch)
	result, caught := -1, -1
	var rt string
	if m.Result != nil {
		rt = b.goType(m.Result, x)
		if v >= 0 && b.l.info.Types[src] == nil {
			irNo("catch value of another type")
		}
		result = b.temp("caught", rt, p)
		caught = len(pre)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: result, X: &irExpr{K: "zero", Pos: p, T: rt}})
	}
	k := "panic"
	if partial {
		k = "fail"
		if !b.fc.frame.fallible {
			k = "panic"
		}
	}
	sw := &irStmt{K: "tswitch", Pos: p, End: b.pos(x, x.Rbrace), X: &irExpr{K: "local", Pos: p, I: e}}
	var then []*irStmt
	if patterned {
		t := &treeIR{b: b, s: x, site: x, p: p}
		t.body = func(i int) []*irStmt { return b.armBody(x.Arms[i], result, m.Result) }
		// The tree's exit is a catch's default too: a full catch's names
		// no failure (FAIL-ELIDE-PAT, ir_catch_defect.go).
		t.exit = func(ep irPos) []*irStmt {
			n := b.st.report.CatchDefects
			d := b.catchDefault(x, k, partial, e, ep)
			if b.st.report.CatchDefects > n {
				b.st.report.CatchTreeDefects++
			}
			return []*irStmt{d}
		}
		then = t.write([]treeHead{{e, &check.Union{Variants: m.Subjects}}}, alts, sw.End)
		arms = nil
	}
	if plain {
		arms = nil
	}
	for i, a := range arms {
		c := &irCase{Pos: a.pos, Types: a.types}
		if a.as != nil {
			name := b.named(a.as.Name, a.asT, a.pos)
			b.locals[a.as] = name
			c.Body = append(c.Body, &irStmt{K: "def", Pos: a.pos, Local: name, X: &irExpr{K: "assert", Pos: a.pos, T: a.asT, X: &irExpr{K: "local", Pos: a.pos, I: e}}})
		}
		c.Body = append(c.Body, b.armBody(x.Arms[i], result, m.Result)...)
		sw.Cases = append(sw.Cases, c)
	}
	sw.ElsePos = b.pos(x, x.Rbrace)
	sw.Else = []*irStmt{b.catchDefault(x, k, partial, e, sw.ElsePos)} // ir_catch_defect.go
	if plain {
		then = b.armBody(x.Arms[0], result, m.Result)
	} else if !patterned {
		// A patterned catch's tree is its whole then, empty for a catch
		// whose one arm, `case _:`, does nothing: that swallows the
		// failure, where sw, with no cases, panics with it.
		then = []*irStmt{sw}
	}
	ifs := &irStmt{K: "if", Pos: p, End: sw.End, X: b.notNil(e, p), Then: then}
	if result >= 0 && v >= 0 {
		ifs.ElsePos, ifs.ElseEnd = sw.End, sw.End
		// The call's value joins the union of the arms' (ir_union.go).
		val := b.variantInto(b.l.info.Types[src], m.Result, &irExpr{K: "local", Pos: sw.End, I: v}, src)
		ifs.Else = []*irStmt{{K: "set", Pos: sw.End, Local: result, X: val}}
		if !patterned && val.K == "local" && val.I == v && b.localTypeOf(v) == rt && irArmsLeave(sw, result) {
			// Every arm leaves (the emitter's catch, whose arms all
			// panic or return, yields the call's value in place): the
			// value is the call's own local, with no caught copy (RED17).
			pre = append(pre[:caught], pre[caught+1:]...)
			return append(pre, &irStmt{K: "if", Pos: p, End: sw.End, X: ifs.X, Then: then}), &irExpr{K: "local", Pos: p, I: v}
		}
	}
	pre = append(pre, ifs)
	if result < 0 {
		return pre, nil
	}
	return pre, &irExpr{K: "local", Pos: p, I: result}
}

// armBody is a catch arm's statements; its last expression is the
// catch's value when the catch has one (result >= 0), else dropped.
func (b *irBuilder) armBody(a *syntax.CatchArm, result int, rt check.Type) []*irStmt {
	b.switches++ // a case of the catch's type switch (ir_match.go)
	defer func() { b.switches-- }()
	list := a.Body
	if len(list) == 0 {
		if result >= 0 {
			irNo("catch arm form")
		}
		return nil
	}
	if result < 0 {
		// No value: the statements, the last one's value dropped (the
		// emitter's stmts, discard).
		return b.tail(list, false)
	}
	// The last statement yields the catch's value into result, as the
	// emitter's yieldList with destAssign: an expression, an if or a
	// match whose arms yield, a return, or a break or continue.
	return b.tailTo(list, false, &yieldTo{local: result, t: rt})
}

// operand is one operand of a call: a try, a catch, a call, or an
// expression; under a try, a fallible call fails the frame too (the
// emitter's tries).
func (b *irBuilder) operand(x syntax.Expr) ([]*irStmt, *irExpr) {
	switch u := unparen(x).(type) {
	case *syntax.CallExpr:
		if b.tries > 0 && b.fallibleCall(u) != nil {
			pre, v := b.try(u)
			if v == nil {
				irNo("try without a value")
			}
			return pre, b.implicit(x, v) // as value converts (ir_expr.go)
		}
	}
	return b.value(x) // an if or match too, the implicit conversion too (ir_union.go)
}

// isBranch reports an if or a match: a value whose statements may assign
// a local.
func isBranch(x syntax.Expr) bool {
	switch unparen(x).(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return true
	}
	return false
}

// operands evaluates list left to right (the emitter's operands): an
// operand before one that runs statements is spilled to a temporary, so
// it is evaluated first. A constant needs no spill, nor a local unless a
// later operand is an if or a match, whose arms may assign it: no other
// statement IR writes assigns a source local.
func (b *irBuilder) operands(list []syntax.Expr) ([]*irStmt, []*irExpr) {
	pres := make([][]*irStmt, len(list))
	vals := make([]*irExpr, len(list))
	for i, a := range list {
		pres[i], vals[i] = b.operand(a)
	}
	return b.ordered(list, pres, vals), vals // ir_order.go
}

// operandType is the type of operand a's value: its implicit
// conversion's target when it has one (value applies it), else its own.
// A variant passed as its union spills into a local of the union. The one
// definition (owner: lane GP): OPC's operandFits (ir_operand.go) reads it
// too, and a ConvVariant's target is the case OPC's own copy covered.
func (b *irBuilder) operandType(a syntax.Expr) check.Type {
	if conv, ok := b.l.info.Implicit[a]; ok && conv.Kind != 0 && conv.To != nil {
		return conv.To
	}
	return b.l.info.Types[a]
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
// polling frame (the emitter's loopBody), unless every iteration polls
// through a callee (iterationPolls, pollplace.go). A continue polls before
// it leaves the iteration (continueStmt).
func (b *irBuilder) loopPoll(s syntax.Stmt, body *syntax.BlockStmt) []*irStmt {
	if b.env < 0 || !b.fc.frame.polls || b.fc.iterationPolls(body) { // pollplace.go
		return nil
	}
	if r, ok := s.(*syntax.RangeStmt); ok && b.seqLoop(r) {
		// A Seq loop's poll stands at the body's closing brace: at the
		// loop's own position, Go's range-over-func rewrite also calls the
		// adapter, and gc does not inline a call at a position already on
		// its inlining stack (ir_seq.go).
		return []*irStmt{b.pollAt(body, body.Rbrace)}
	}
	return []*irStmt{b.poll(s)}
}

// loopPolls reports a frame whose loops end each iteration in a poll.
func (b *irBuilder) loopPolls() bool { return b.env >= 0 && b.fc.frame.polls }

// continueStmt is continue: in a polling frame, the back-edge poll and
// then Go's continue. The emitter's continue jumps to the poll that ends
// the iteration (body.go branch); the poll then the next iteration is the
// same path, and needs no label.
func (b *irBuilder) continueStmt(s *syntax.BranchStmt) []*irStmt {
	var out []*irStmt
	if b.loopPolls() {
		out = append(out, b.poll(s))
	}
	return append(out, &irStmt{K: "continue", Pos: b.pos(s, s.Pos())})
}
