package lower

// IR builder: calls, operation calls and polls. See ir.go.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) poll(site syntax.Node) *irStmt {
	p := b.pos(site, site.Pos())
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	return &irStmt{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Check", X: task}}
}

// call is a call x that cannot fail at its site; stmt reports a call in
// statement position, where a call with no result may stand. A call with
// effects stays in place: the emitter's `v := f(…)` runs it before its
// expression, and Go runs it there too (calls before the other operands;
// an earlier operand before a later one's statements is spilled, operands).
func (b *irBuilder) call(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if b.fallibleCall(x) != nil {
		irNo("untried fallible call")
	}
	return b.callParts(x, stmt)
}

// rawCall is the fallible call x: its Go call, whose result is the value
// and the error (ir_fail.go binds or returns them).
func (b *irBuilder) rawCall(x *syntax.CallExpr) ([]*irStmt, *irExpr) {
	return b.callParts(x, true)
}

// callParts is the call x and the statements before it.
func (b *irBuilder) callParts(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if x.Trailing != nil {
		irNo("trailing block")
	}
	c := b.l.info.Calls[x]
	if c == nil {
		irNo("call without a Calls entry")
	}
	if len(c.TypeArgs) > 0 || c.StartsTask {
		irNo("generic or task call")
	}
	if !stmt && b.l.info.Types[x] == nil {
		irNo("call without a value")
	}
	p := b.pos(x, x.Pos())
	switch c.Kind {
	case check.CallLen:
		if len(x.Args) != 1 {
			irNo("len form")
		}
		switch t := b.l.info.Types[x.Args[0]].(type) {
		case *check.Slice:
		case *check.Basic:
			if t.Kind != check.String {
				irNo("len of a non-slice")
			}
		default:
			irNo("len of a non-slice")
		}
		return nil, &irExpr{K: "len", Pos: p, X: b.expr(x.Args[0])}
	case check.CallFunc:
		f := c.Func
		if f == nil || f.Pkg != b.l.pkg || check.GoBuiltin(f) != "" || userExtern(f) {
			irNo("call of a function outside the package")
		}
		fd := b.l.useFunc(f, x)
		switch {
		case fd.extern != "":
			irNo("call of an extern function")
		case fd.ptr:
			irNo("call of a pointer-receiver method")
		case len(b.l.ownedParams(f)) > 0:
			irNo("call that passes owned values")
		case len(witnessParams(f.Sig)) > 0:
			irNo("call that passes witnesses")
		case c.Site != nil:
			irNo("call with a call site")
		case len(b.fc.captureOwners(x)) > 0:
			irNo("call that captures owners")
		}
		if fd.fallible && !failsHere(c) {
			irNo("fallible call with an empty row at its site")
		}
		if !fd.pure && b.env < 0 {
			irNo("call of an impure function from a pure one")
		}
		args := x.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		pre, irArgs := b.operands(args)
		if !fd.pure {
			// The environment first, after a method's receiver
			// (callText, taskDestArgs).
			env := &irExpr{K: "local", Pos: p, I: b.env}
			at := 0
			if fd.method != nil {
				at = 1
			}
			irArgs = append(irArgs[:at:at], append([]*irExpr{env}, irArgs[at:]...)...)
		}
		if fd.method != nil {
			return pre, &irExpr{K: "mcall", Pos: p, Name: fd.name, X: irArgs[0], Args: irArgs[1:]}
		}
		return pre, &irExpr{K: "call", Pos: p, Name: fd.name, Args: irArgs}
	case check.CallProviderOp:
		switch {
		case b.env < 0 || c.Op == nil:
			irNo("operation call form")
		case b.fc.entersRelease(x):
			irNo("operation call that enters a release")
		}
		// A test build calls a capability's operation as a program does
		// (calls.go: only a law's own operation, CallLawOp, differs).
		if len(rowElems(c.Op.Sig.Errors)) > 0 && !failsHere(c) {
			irNo("fallible call with an empty row at its site")
		}
		sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
		if !ok {
			irNo("operation call spelling")
		}
		named, ok := b.l.info.Types[sel.X].(*check.Named)
		if !ok {
			irNo("capability without a named type")
		}
		// The interruption point, before the operands (callText).
		var pre []*irStmt
		if b.fc.frame.polls {
			pre = append(pre, b.poll(x))
		}
		argPre, irArgs := b.operands(x.Args)
		pre = append(pre, argPre...)
		env := &irExpr{K: "local", Pos: p, I: b.env}
		args := append([]*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}, irArgs...)
		capField := &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}
		return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}
	}
	irNo("call kind %d", c.Kind)
	return nil, nil
}
