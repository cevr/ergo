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
// effects (an impure function, an operation) runs before the expression
// that holds it, into a temporary, as the emitter's call does. Go would
// run a call in place before the expression's other operands (a slice
// index that panics, say); as a statement before it, the call is a
// pre-statement, which an operand spills around (operands) or a binary
// expression refuses ("operation call inside an expression").
func (b *irBuilder) call(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if b.fallibleCall(x) != nil {
		irNo("untried fallible call")
	}
	pre, e, effect := b.callParts(x, stmt)
	if e != nil && e.K == "vcall" {
		// A function value returns its (value, error) pair (ir_closure.go).
		vpre, v := b.valueResult(x, e, stmt)
		return append(pre, vpre...), v
	}
	if effect && !stmt {
		return b.hoist(pre, e, x)
	}
	return pre, e
}

// rawCall is the fallible call x: its Go call, whose result is the value
// and the error (ir_fail.go binds or returns them).
func (b *irBuilder) rawCall(x *syntax.CallExpr) ([]*irStmt, *irExpr) {
	pre, e, _ := b.callParts(x, true)
	return pre, e
}

// callParts is the call x and the statements before it; effect reports a
// call with effects.
func (b *irBuilder) callParts(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr, bool) {
	c := b.l.info.Calls[x]
	if c == nil {
		irNo("call without a Calls entry")
	}
	// A trailing block is a function literal, the call's last operand:
	// a call of a function of the program takes it (ir_closure.go).
	if x.Trailing != nil && (c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg.Std() || c.Func.Pkg.Host()) {
		if c.Kind == check.CallFunc && c.Func != nil && c.Func.Pkg.Std() {
			// timeout.After { … }: the literal is built, the generic std
			// extern that takes it is not (stdCall).
			irNo("std call that adapts a function")
		}
		irNo("trailing block")
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
		return nil, &irExpr{K: "len", Pos: p, X: b.expr(x.Args[0])}, false
	case check.CallFunc:
		f := c.Func
		switch {
		case f == nil:
			irNo("call without a function")
		case check.GoBuiltin(f) != "":
			irNo("call of a Go builtin")
		case userExtern(f):
			irNo("call of a user extern")
		case f.Pkg.Host():
			// A function of a Go package read from its binding (import
			// go; gostd's runtime.NumCPU): the emitter calls its Go
			// target, and useFunc would ask the naming pass for a name it
			// never gave (examples/onebrc).
			irNo("call of a Go package function")
		}
		// A function of another Ergo package of the program, or a std
		// function with an Ergo body, is a function of the one Go package
		// the program lowers to, by its Go name; a std extern calls its
		// runtime/v1 body (ir_pkg.go).
		if f.Pkg.Std() && f.Decl != nil && f.Decl.Extern != nil {
			return b.stdCall(x, c, f, p)
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
		if x.Trailing != nil {
			irArgs = append(irArgs, b.funcLit(nil, x)) // ir_closure.go
		}
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
			return pre, &irExpr{K: "mcall", Pos: p, Name: fd.name, X: irArgs[0], Args: irArgs[1:]}, !fd.pure
		}
		return pre, &irExpr{K: "call", Pos: p, Name: fd.name, Args: irArgs}, !fd.pure
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
		return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}, true
	case check.CallFuncValue:
		pre, e := b.valueCall(x, c, p) // ir_closure.go
		return pre, e, true
	}
	irNo("call kind %d", c.Kind)
	return nil, nil, false
}
