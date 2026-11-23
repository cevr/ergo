package lower

// IR builder: function values (G6, IRG6 r2). The emitter's uniform shape
// (typeCtx.funcType): a function value takes the caller's environment
// first and returns its value, or struct{} when it has none, then an
// error, whatever its rows. A function literal is a Go closure of that
// shape (funcLitClosure) whose body is a frame of its own; a call of a
// function value passes the environment and binds both results. See
// ir.go.

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sigHead is the parameter half of the Go type of a function value of the
// signature s, `func(*Env, P...)`, and the Go type of its value, "" when
// it has none. It is the one reader of the env type for both shapes
// (sigType, frameSigType), and writes no body.
func (b *irBuilder) sigHead(s *check.Signature, site syntax.Node) (head, value string) {
	if len(s.Results) > 1 {
		irNo("function type with several results")
	}
	parts := []string{"*" + b.l.env()}
	for _, p := range s.Params {
		parts = append(parts, b.goType(p.Type, site))
	}
	if len(s.Results) == 1 {
		value = b.goType(s.Results[0], site)
	}
	return "func(" + strings.Join(parts, ", ") + ")", value
}

// sigType is the Go type of a function value of the signature s.
func (b *irBuilder) sigType(s *check.Signature, site syntax.Node) string {
	head, value := b.sigHead(s, site)
	if value == "" {
		return head + b.fc.valueResults(nil) // the emitter's (struct{}, error)
	}
	return head + " (" + value + ", error)"
}

// frameSigType is sigType with its results named, as the emitter's
// funcLitClosure writes a frame literal's (namedResults): `(_ R, err
// error)`, struct{} for R with no value, so the frame's deferred End sets
// the literal's error result after its return ("result" 1).
func (b *irBuilder) frameSigType(s *check.Signature, site syntax.Node) string {
	head, value := b.sigHead(s, site)
	if value == "" {
		value = "struct{}" // the emitter's unit shape
	}
	return head + " (_ " + value + ", ergoFrameErr error)"
}

// litParam is the local of a function literal's parameter: v's, written
// id, or for a parameter named _ (v nil) a fresh local of its type t that
// no statement reads, as the emitter's _p (funcLitClosure).
func (b *irBuilder) litParam(v *check.Var, id *syntax.Ident, t check.Type, site syntax.Node, p irPos) int {
	if v == nil {
		return b.temp("p", b.goType(t, site), p)
	}
	return b.local(v, id)
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
	capture := l.info.TaskCaptures[n]
	if capture != nil && !taskAdmits(capture) {
		irNo("function literal that a task captures")
	}
	// A literal run once owns what it moves: a frame of its own whose
	// captures register at its open (ir_taskmove.go; funcLitClosure).
	var owns []*check.Var
	if capture != nil && !capture.Repeated {
		owns = capture.Vars
	}
	var params []*check.Var
	var ids []*syntax.Ident
	if ft != nil && ft.Params != nil {
		for _, field := range ft.Params.List {
			for _, id := range field.Names {
				// A parameter named _ has no variable: a local of its own
				// that nothing reads (litParam), the emitter's _p.
				v, _ := l.info.Defs[id].(*check.Var)
				switch {
				case id.Name == "_":
					v = nil
				case v == nil:
					irNo("function literal with an unnamed parameter")
				}
				params, ids = append(params, v), append(ids, id)
			}
		}
	}
	if len(params) != len(sig.Params) {
		irNo("function literal with an unnamed parameter")
	}
	// A literal that is a frame of its own (stage D3b, Rule 2: an owned
	// parameter, a body that needs a frame) opens its scope first, and
	// its Go results are named for the scope's End (frameSigType).
	framed := l.bodyNeedsFrame(list) || l.ownsParams(params) || len(owns) > 0
	if x != nil && b.fc.plainLits[x] {
		if framed {
			irNo("function literal that is a frame")
		}
		return b.plainLitIR(x, sig, params, ids, list, end) // a known callback (ir_kcb.go)
	}
	p := b.pos(n, n.Pos())
	typ := b.sigType(sig, n)
	if framed {
		typ = b.frameSigType(sig, n)
	}
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
	if framed {
		inner.frame.plain = !l.bodyNeedsScope(list) // the emitter's funcLitClosure
	}
	fc, env, res, loops, tries, switches, label, law, memos := b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law, b.memos
	own, frame, exits, exitsFC := b.own, b.frame, b.exits, b.exitsFC
	amort, sites := b.amort, b.pollSites
	b.amort, b.pollSites = nil, nil // a frame of its own: its nests count apart (ir_poll.go)
	defer func() {
		b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law, b.memos = fc, env, res, loops, tries, switches, label, law, memos
		b.own, b.frame, b.exits, b.exitsFC = own, frame, exits, exitsFC
		b.amort, b.pollSites = amort, sites
	}()
	// The literal's body is a frame of its own: no loop of the maker's
	// memoizes a call of it (ir_memo.go).
	b.memos = nil
	envLocal := b.temp("env", "*"+l.env(), p)
	lit := &irExpr{K: "func", Pos: p, T: typ, Params: []int{envLocal}}
	for i, v := range params {
		lit.Params = append(lit.Params, b.litParam(v, ids[i], sig.Params[i].Type, n, p))
	}
	b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law = inner, envLocal, result, 0, 0, 0, "", nil
	if framed {
		// The literal's own frame scope, its End on the named error
		// result (frameSigType's second), then its taken parameters
		// (the emitter's openFrame and ownVar), before the entry poll.
		b.own, b.frame, b.exits, b.exitsFC = nil, 0, false, nil
		var body *syntax.BlockStmt
		if x != nil {
			body = x.Body
		} else {
			body = site.Trailing
		}
		errArg := &irExpr{K: "addr", Pos: p, X: &irExpr{K: "result", Pos: p, I: 1}}
		lit.Body = append(lit.Body, b.openScopeOf(body, n, n, append(slices.Clone(params), owns...), errArg, false)...) // ir_own.go
	}
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
// is. A value of a type defined over a function type (Seq[T]) calls
// through its signature, as Go calls it and as the emitter does
// (GP-DEFINED, ir_defined.go). A known callback's direct call and a task's
// destination stay the emitter's.
func (b *irBuilder) valueCall(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr) {
	if definedSig(b.l.info.Types[x.Fun]) == nil {
		irNo("call of a value of a named function type")
	}
	if id, ok := unparen(x.Fun).(*syntax.Ident); ok {
		if v, ok := b.l.info.Uses[id].(*check.Var); ok && b.fc.plain[v] {
			return b.plainCall(x, p) // ir_kcb.go
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
