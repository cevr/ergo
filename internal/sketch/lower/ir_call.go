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
// pre-statement, which earlier operands spill around (operands, inOrder in
// ir_order.go).
func (b *irBuilder) call(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if b.fallibleCall(x) != nil {
		if b.tries == 0 {
			irNo("untried fallible call")
		}
		// Inside a try: the call's failure leaves the frame (ir_fail.go
		// try), as the emitter's value propagates it where the call is.
		pre, v := b.try(x)
		if v == nil && !stmt {
			irNo("try without a value")
		}
		return pre, v
	}
	if b.asWrittenCall(x) {
		// A call tree inside AsWritten is kept as written (ir_rewrite.go).
		b.keep++
		defer func() { b.keep-- }()
	}
	pre, e, effect := b.callParts(x, stmt)
	if e != nil {
		b.callFacts(x, e, stmt) // ir_rewrite_pass.go: the rewrite passes read them
	}
	if e != nil && (e.K == "vcall" || b.dropsErr[e] && !stmt) {
		// A function value returns its (value, error) pair (ir_closure.go),
		// as a fallible function does where its row is empty (dropErr): the
		// value is bound and the error, nil, dropped. A statement keeps the
		// Go call, whose results Go drops.
		vpre, v := b.valueResult(x, e, stmt)
		return append(pre, vpre...), v
	}
	if effect && !stmt {
		return b.hoist(pre, e, x)
	}
	return pre, e
}

// dropErr marks e, a call whose Go form returns an error where the row at
// its site is empty: the error is nil there, and the emitter binds the
// value and drops the error (calls.go call, failsHere).
func (b *irBuilder) dropErr(e *irExpr) *irExpr {
	if b.dropsErr == nil {
		b.dropsErr = map[*irExpr]bool{}
	}
	b.dropsErr[e] = true
	return e
}

// opResult is e, the call of c's operation: an operation declared with
// failures returns an error, which a site whose row is empty drops.
func (b *irBuilder) opResult(c *check.Call, e *irExpr) *irExpr {
	if len(rowElems(c.Op.Sig.Errors)) > 0 && !failsHere(c) {
		return b.dropErr(e)
	}
	return e
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
	c = b.knownField(x, c) // a known record's field in its copy (ir_known.go)
	// A trailing block is a function literal, the call's last operand:
	// a call of a function of the program takes it (ir_closure.go).
	if x.Trailing != nil && (c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg.Std() || c.Func.Pkg.Host()) {
		stdExtern := c.Kind == check.CallFunc && c.Func != nil && c.Func.Pkg.Std() && c.Func.Decl != nil && c.Func.Decl.Extern != nil
		if !stdExtern {
			irNo("trailing block")
		}
		// timeout.After { … }: stdCall adapts the block, its last operand
		// (ir_adapt.go).
	}
	if c.StartsTask {
		irNo("task call")
	}
	if len(c.TypeArgs) > 0 && c.Kind != check.CallFunc {
		irNo("generic call kind %d", c.Kind)
	}
	if !stmt && b.l.info.Types[x] == nil {
		irNo("call without a value")
	}
	p := b.pos(x, x.Pos())
	if check.IsUnreachable(c) {
		return b.unreachableValue(x) // ir_defect.go (FLIP OPC-unreach)
	}
	// The call keeps its Ergo identity in the call table, as the Go text's
	// identify records it (calls.go, #230): the IR's position of the call
	// is the Go frame's line that a defect's report looks up.
	switch c.Kind {
	case check.CallFunc, check.CallOp, check.CallProviderOp, check.CallLawOp, check.CallFuncValue:
		if b.fc.directives && x.Pos().IsValid() {
			b.l.recordCall(x, b.fc.owner, b.fc.callee(x, c))
		}
	}
	if check.IsUnreachable(c) {
		return b.unreachableValue(x) // ir_defect.go
	}
	switch c.Kind {
	case check.CallLen:
		if len(x.Args) != 1 {
			irNo("len form")
		}
		switch t := b.l.info.Types[x.Args[0]].(type) {
		case *check.Slice, *check.Map:
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
		case f.Pkg != nil && f.Pkg.Host():
			// A function of a Go package read from its binding (import
			// go; gostd's runtime.NumCPU): the emitter calls its Go
			// target, and useFunc would ask the naming pass for a name it
			// never gave (examples/onebrc). The IR calls the target by
			// its import path, in the plain domain (ir_tuple.go).
			return b.hostCall(x, c, f, p)
		}
		// A function of another Ergo package of the program, or a std
		// function with an Ergo body, is a function of the one Go package
		// the program lowers to, by its Go name; a std extern calls its
		// runtime/v1 body (ir_pkg.go).
		// A prelude intrinsic (Append) is a Go function of main.go that
		// the lowering writes (decls.go intrinsic): a call like a user
		// function's.
		intrinsic := b.l.intrinsic(f) != nil
		if f.Pkg.Std() && f.Decl != nil && f.Decl.Extern != nil && !intrinsic {
			return b.stdCall(x, c, f, p)
		}
		fd := b.l.useFunc(f, x)
		switch {
		case fd.extern != "" && !intrinsic:
			irNo("call of an extern function")
		case len(witnessParams(f.Sig)) > 0:
			irNo("call that passes witnesses")
		case c.Site != nil:
			irNo("call with a call site")
		case len(b.fc.captureOwners(x)) > 0:
			irNo("call that captures owners")
		case fd.ptr && !b.addrRecv(c) && !b.passesPointer(c, x): // ir_place.go, ir_pkg.go
			irNo("call of a pointer-receiver method on a value")
		}
		mark := func(e *irExpr) *irExpr { return e }
		if fd.fallible && !failsHere(c) {
			mark = b.dropErr
		}
		if !fd.pure && b.env < 0 {
			irNo("call of an impure function from a pure one")
		}
		args := x.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		var pre []*irStmt
		var irArgs []*irExpr
		if b.addrRecv(c) { // ir_place.go
			pre, irArgs = b.recvAddrCall(c, x.Args)
		} else {
			pre, irArgs = b.operands(args)
		}
		b.valueSource(x, c, args, irArgs) // ir_pkg.go
		if x.Trailing != nil {
			irArgs = append(irArgs, b.closureLit(nil, x)) // ir_closure.go
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
			return pre, mark(&irExpr{K: "mcall", Pos: p, Name: b.ownedCallName(x, f, fd.name), X: irArgs[0], Args: irArgs[1:]}), !fd.pure
		}
		// Append is Go's builtin append on the value rule's operands
		// (FLIP F3): Go then sees a place appended back to itself.
		if intrinsic && b.builtinAppend(f) { // ir_pkg.go
			return pre, &irExpr{K: "append", Pos: p, Args: irArgs}, false
		}
		// A generic function's call names its Go type arguments (the
		// emitter's typeArgs), never left to Go's inference (ir_pkg.go).
		if sp := b.copyCall(x, c); sp != "" {
			// The copy for the known record: one instance (ir_known.go).
			return pre, mark(&irExpr{K: "call", Pos: p, Name: sp, Args: irArgs}), !fd.pure
		}
		return pre, mark(&irExpr{K: "call", Pos: p, Name: b.ownedCallName(x, f, fd.name), T: b.typeArgs(c, x), Args: irArgs}), !fd.pure
	case check.CallOp:
		return b.opCall(x, c, p) // ir_pkg.go
	case check.CallConversion:
		return b.conversion(x, c, p) // ir_pkg.go
	case check.CallEncode, check.CallDecode:
		return b.codecCall(x, c, p) // ir_codec.go
	case check.CallProviderOp:
		switch {
		case b.env < 0 || c.Op == nil:
			irNo("operation call form")
		case b.fc.entersRelease(x):
			irNo("operation call that enters a release")
		}
		// A test build calls a capability's operation as a program does
		// (calls.go: only a law's own operation, CallLawOp, differs).
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
		return pre, b.opResult(c, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}), true
	case check.CallFuncValue:
		pre, e := b.valueCall(x, c, p) // ir_closure.go
		return pre, e, true
	}
	irNo("call kind %d", c.Kind)
	return nil, nil, false
}
