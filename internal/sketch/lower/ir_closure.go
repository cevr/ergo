package lower

// IR builder: function values (G6, IRG6 r2). The emitter's uniform shape
// (typeCtx.funcType): a function value takes the caller's environment
// first and returns its value, or struct{} when it has none, then an
// error, whatever its rows. A function literal is a Go closure of that
// shape (funcLitClosure) whose body is a frame of its own; a call of a
// function value passes the environment and binds both results. See
// ir.go.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sigType is the Go type of a function value of the signature s.
func (b *irBuilder) sigType(s *check.Signature, site syntax.Node) string {
	if len(s.Results) > 1 {
		irNo("function type with several results")
	}
	parts := []string{"*" + b.l.env()}
	for _, p := range s.Params {
		parts = append(parts, b.goType(p.Type, site))
	}
	res := b.fc.valueResults(nil) // the emitter's (struct{}, error)
	if len(s.Results) == 1 {
		res = " (" + b.goType(s.Results[0], site) + ", error)"
	}
	return "func(" + strings.Join(parts, ", ") + ")" + res
}

// hasSig reports a slice type whose elements are, at some depth, function
// values: its spelling is built from its parts (goType).
func hasSig(t check.Type) bool {
	for {
		switch u := t.(type) {
		case *check.Signature:
			return true
		case *check.Slice:
			t = u.Elem
		default:
			return false
		}
	}
}

// closureLit is the function literal x, or with x nil the trailing block of
// the call site (its block): the emitter's funcLit. A literal that copies
// or moves what it captures, that is a frame of its own (an owned
// parameter, a body that needs a frame) or that a known callback takes
// stays the emitter's.
func (b *irBuilder) closureLit(x *syntax.FuncLit, site *syntax.CallExpr) *irExpr {
	l := b.l
	var n syntax.Node = x
	var list []syntax.Stmt
	var ft *syntax.FuncType
	var end syntax.Pos
	var endNode syntax.Node
	if x != nil {
		list, ft, end, endNode = x.Body.List, x.Type, x.Body.Rbrace, x.Body
		if b.fc.plainLits[x] {
			irNo("function literal of a known callback")
		}
	} else {
		n = site.Trailing
		list, end, endNode = site.Trailing.List, site.Trailing.Rbrace, site.Trailing
	}
	sig := l.info.Literals[n]
	if sig == nil {
		irNo("function literal without its signature")
	}
	if len(sig.Results) > 1 {
		irNo("function type with several results")
	}
	if captureLowering != "reference" && len(l.info.ValueCaptures[n]) > 0 {
		irNo("function literal that copies what it captures")
	}
	if l.info.TaskCaptures[n] != nil {
		irNo("function literal that a task captures")
	}
	var params []*check.Var
	var ids []*syntax.Ident
	if ft != nil && ft.Params != nil {
		for _, field := range ft.Params.List {
			for _, id := range field.Names {
				v, _ := l.info.Defs[id].(*check.Var)
				if v == nil || id.Name == "_" {
					irNo("function literal with an unnamed parameter")
				}
				params, ids = append(params, v), append(ids, id)
			}
		}
	}
	if len(params) != len(sig.Params) {
		irNo("function literal with an unnamed parameter")
	}
	if l.bodyNeedsFrame(list) || l.ownsParams(params) {
		irNo("function literal that is a frame")
	}
	p := b.pos(n, n.Pos())
	typ := b.sigType(sig, n)
	var result check.Type
	if len(sig.Results) == 1 {
		result = sig.Results[0]
	}
	// The literal's body is a frame of its own (funcLitClosure): its
	// result, its environment, no loop, switch or try of the maker's.
	inner := b.fc.innerFuncCtx()
	inner.env = "env"
	inner.frame = newFrame(result, true, framePolls(l.info, n))
	inner.frame.unit = result == nil
	fc, env, res, loops, tries, switches, label := b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label
	defer func() {
		b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label = fc, env, res, loops, tries, switches, label
	}()
	envLocal := b.temp("env", "*"+l.env(), p)
	lit := &irExpr{K: "func", Pos: p, T: typ, Params: []int{envLocal}}
	for i, v := range params {
		lit.Params = append(lit.Params, b.local(v, ids[i]))
	}
	b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label = inner, envLocal, result, 0, 0, 0, ""
	if frameFootprint(l.info, n).EntryPoll && inner.frame.polls {
		lit.Body = append(lit.Body, b.poll(n))
	}
	lit.Body = append(lit.Body, b.bodyStmts(list)...) // ir_fail.go
	e := b.pos(endNode, end)
	lit.End = &e
	if result != nil {
		lit.Body = append(lit.Body, b.bodyEnd(lit.Body, e)...)
	} else {
		lit.Body = b.unitReturns(lit.Body, e)
	}
	return lit
}

// unitReturns is the body of a literal with no value: each return gives
// struct{}{} first (errReturn), and the end returns one with no error.
func (b *irBuilder) unitReturns(body []*irStmt, end irPos) []*irStmt {
	var walk func(list []*irStmt)
	walk = func(list []*irStmt) {
		for _, s := range list {
			if s.K == "ret" {
				if s.X != nil {
					// A returned failure of a call with no value (a
					// fallible call's error as the literal's).
					irNo("function literal with no value that returns a call's failure")
				}
				s.X = &irExpr{K: "zero", Pos: s.Pos, T: "struct{}"}
			}
			walk(s.Then)
			walk(s.Else)
			walk(s.Body)
			for _, c := range s.Cases {
				walk(c.Body)
			}
		}
	}
	walk(body)
	if !irListTerminates(body) {
		body = append(body, &irStmt{K: "ret", Pos: end, X: &irExpr{K: "zero", Pos: end, T: "struct{}"}})
	}
	return body
}

// irLiterals calls f on each function literal s's own expressions hold
// (not those inside a literal's body: f walks those).
func irLiterals(s *irStmt, f func(*irExpr)) {
	var expr func(e *irExpr)
	expr = func(e *irExpr) {
		if e == nil {
			return
		}
		if e.K == "func" {
			f(e)
			return
		}
		expr(e.X)
		expr(e.Y)
		for _, a := range e.Args {
			expr(a)
		}
		for _, a := range e.Elems {
			expr(a)
		}
	}
	expr(s.X)
	expr(s.Y)
	for _, c := range s.Cases {
		for _, v := range c.Vals {
			expr(v)
		}
	}
}

// valueCall is the call x of a function value (CallFuncValue): the value,
// then the environment and the operands, its (value, error) pair as it
// is. A value of a named function type, a known callback's direct call
// and a task's destination stay the emitter's.
func (b *irBuilder) valueCall(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr) {
	if _, ok := b.l.info.Types[x.Fun].(*check.Signature); !ok {
		irNo("call of a value of a named function type")
	}
	if id, ok := unparen(x.Fun).(*syntax.Ident); ok {
		if v, ok := b.l.info.Uses[id].(*check.Var); ok && b.fc.plain[v] {
			irNo("call of a known callback")
		}
	}
	if b.env < 0 {
		irNo("call of a function value from a pure function")
	}
	if c.Recv != nil {
		irNo("call of a function value with a receiver")
	}
	pre, vals := b.operands(append([]syntax.Expr{x.Fun}, x.Args...))
	args := append([]*irExpr{{K: "local", Pos: p, I: b.env}}, vals[1:]...)
	return pre, &irExpr{K: "vcall", Pos: p, X: vals[0], Args: args}
}

// valueResult binds the pair of the function value call e, the call x,
// at a site where it cannot fail: the value, its error dropped (call).
func (b *irBuilder) valueResult(x *syntax.CallExpr, e *irExpr, stmt bool) ([]*irStmt, *irExpr) {
	t := b.l.info.Types[x]
	unit := t == nil || isNoValue(t) || b.fc.noValue(x)
	if unit && !stmt {
		irNo("call of a function value with no value in a value position")
	}
	typ := "struct{}"
	if !unit {
		typ = b.goType(t, x)
	}
	// In a statement both results are bound and dropped (the emitter's
	// `_, _ = f(…)`; irUseLocals reads them).
	v := b.temp("v", typ, e.Pos)
	err := b.temp("err", "error", e.Pos)
	bind := []*irStmt{{K: "def2", Pos: e.Pos, Local: v, Val: &err, X: e}}
	if stmt {
		return bind, nil
	}
	return bind, &irExpr{K: "local", Pos: e.Pos, I: v}
}
