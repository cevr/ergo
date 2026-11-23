package lower

// IR builder: calls, operation calls and polls. See ir.go.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) poll(site syntax.Node) *irStmt { return b.pollAt(site, site.Pos()) }

// pollAt is the poll at position at of site.
func (b *irBuilder) pollAt(site syntax.Node, at syntax.Pos) *irStmt {
	p := b.pos(site, at)
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
	if !stmt {
		if e := b.fold(x); e != nil {
			return nil, e
		}
		if pre, e := b.template(x); e != nil { // ir_template.go
			return pre, e
		}
	}
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
	if pre, e, ok := b.fuseAt(x); ok {
		return pre, e // a site of a law over function parameters (ir_fusion.go)
	}
	if b.asWrittenCall(x) {
		// A call tree inside AsWritten is kept as written (ir_rewrite.go).
		b.keep++
		defer func() { b.keep-- }()
	}
	pre, e, effect := b.callParts(x, stmt)
	b.markPure(x, e) // ir_merge.go
	if e != nil {
		b.callFacts(x, e, stmt) // ir_rewrite_pass.go: the rewrite passes read them
	}
	if e != nil && (e.K == "vcall" && !b.plainCallee(x) || b.dropsErr[e] && (!stmt || b.hasValue(x))) {
		// A function value returns its (value, error) pair (ir_closure.go;
		// a known callback's is plain, ir_kcb.go),
		// as a fallible function does where its row is empty (dropErr): the
		// value is bound and the error, nil, dropped. A statement binds
		// both to blanks, as the emitter's `_, _ = f(…)` does: Go reads
		// no call statement of a pair (I-FLIP red #14, lru's
		// `let _ = m.Get(k, Square)`). A fallible call with no value
		// returns the error alone, which a call statement drops.
		vpre, v := b.valueResult(x, e, stmt)
		return append(pre, vpre...), v
	}
	if effect && !stmt {
		return b.hoist(pre, e, x)
	}
	return pre, e
}

// hasValue reports a call x with a value, whose Go form returns it beside
// the error.
func (b *irBuilder) hasValue(x *syntax.CallExpr) bool {
	t := b.l.info.Types[x]
	return t != nil && !isNoValue(t) && !b.fc.noValue(x)
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

// externFunc reports a function declared extern, whose body is Go's.
func externFunc(f *check.Func) bool { return f.Decl != nil && f.Decl.Extern != nil }

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
	c = b.knownField(x, c)  // a known record's field in its copy (ir_known.go)
	c = b.knownWitnessOp(c) // a known witness's operation in its copy (ir_witness.go)
	// A trailing block is a function literal, the call's last operand:
	// a call of a function with an Ergo body takes it as any function
	// value (ir_closure.go), whichever package declares it (SP4: no std
	// read); an extern that binds the Ergo runtime adapts it (stdCall,
	// ir_adapt.go: timeout.After { … }; OPC-ADAPT2). An extern whose
	// target is Go's library (hostCall) takes none.
	if x.Trailing != nil && (c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg.Host() || userExtern(c.Func)) {
		irNo("trailing block")
	}
	if c.StartsTask && !b.startsOnFrame(c) && !ergoBodyCall(c) { // ir_own.go
		irNo("task call")
	}
	if len(c.TypeArgs) > 0 && c.Kind != check.CallFunc && !(c.Kind == check.CallOp && c.Op != nil && len(c.Op.Sig.TParams) > 0) {
		irNo("generic call kind %d", c.Kind)
	}
	if !stmt && b.l.info.Types[x] == nil {
		irNo("call without a value")
	}
	p := b.pos(x, x.Pos())
	if check.IsUnreachable(c) {
		return b.unreachableValue(x) // ir_defect.go (FLIP OPC-unreach)
	}
	if check.IsInForce(c) {
		// Written at each call, never called (ir_inforce.go).
		return b.inForce(x, c, p)
	}
	// The call keeps its Ergo identity in the call table, as the Go text's
	// identify records it (calls.go): the IR's position of the call
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
	case check.CallTestSeed:
		// The run's --seed, for a harnessed test (check harness.go): the
		// runtime reads it from the test binary's own arguments.
		b.keepImport("var _ = " + b.l.rt() + ".TestSeed")
		return nil, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "TestSeed"}, false
	case check.CallTestShown:
		pre, e := b.shownCall(x, p) // ir_assert.go
		return pre, e, false
	case check.CallTestFile:
		// The test's file as the runner names it (check harness.go): the
		// file part of the call's siteText.
		site := b.l.siteText(x)
		for range 2 {
			site = site[:strings.LastIndex(site, ":")]
		}
		return nil, &irExpr{K: "const", Pos: p, T: "string", V: site}, false
	case check.CallTestReport:
		// A harnessed test's failure (check harness.go): the harness's
		// $Reported, which fails the test with the message as its report.
		if b.l.tests == nil {
			irNo("ergo·report outside a harnessed test")
		}
		pre, vals := b.operands(x.Args) // ir_fail.go
		return pre, &irExpr{K: "call", Pos: p, Name: b.l.helper("Reported"), Args: vals}, false
	case check.CallTestSource:
		// The path a harnessed test's replay names (check harness.go):
		// the binary's --source, or the test's file.
		b.keepImport("var _ = " + b.l.rt() + ".TestSource")
		pre, vals := b.operands(x.Args) // ir_fail.go
		return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "TestSource", Args: vals}, false
	case check.CallHash:
		return b.hashCall(x, p) // ir_mapprovider.go
	case check.CallLen:
		if len(x.Args) != 1 {
			irNo("len form")
		}
		if _, ok := b.intMapOf(b.l.info.Types[x.Args[0]]); ok { // ir_intmap.go
			pre, m := b.value(x.Args[0])
			return pre, tableCall(p, "Len", m), false
		}
		// A named type over a slice or Map (a Go package's url.Values)
		// is Go's len of it too (IR-GOCALLS-3).
		switch t := b.l.info.Types[x.Args[0]]; underEqual(t).(type) {
		case *check.Slice, *check.Map:
		default:
			// A string, or a named type over one (std fs.Text, the
			// runtime's `type Text string`): Go's len of its bytes.
			if bt := b.scalarBasic(t); bt == nil || bt.Kind != check.String {
				irNo("len of a non-slice")
			}
		}
		return nil, b.foldConst(x, &irExpr{K: "len", Pos: p, X: b.expr(x.Args[0])}), false // ir_opfold.go: a constant's len
	case check.CallFunc:
		f := c.Func
		switch {
		case f == nil:
			irNo("call without a function")
		case check.GoBuiltin(f) != "":
			return b.goBuiltinCall(x, c, f, p, stmt) // ir_builtin.go
		case userExtern(f), f.Pkg != nil && f.Pkg.Host():
			// A function of a Go package: an extern whose target is in
			// Go's library, or one read from its binding (import go;
			// gostd's runtime.NumCPU). The emitter calls its Go target
			// (hostCall), and useFunc would ask the naming pass for a
			// name it never gave (examples/station-stats). The IR calls the
			// target by its import path, in the plain domain, by the
			// call's shape and types alone (ir_tuple.go; SP4).
			return b.hostCall(x, c, f, p)
		}
		// A function of another Ergo package of the program, or a std
		// function with an Ergo body, is a function of the one Go package
		// the program lowers to, by its Go name; an extern that binds the
		// Ergo runtime calls its runtime/v1 body (ir_pkg.go), whichever
		// package declares it (SP4: no std read).
		if g := b.fc.uiGroup(x, c); g != nil {
			// A component call goes through its frame's group, as the
			// emitter's does (ir_uigroup.go).
			return b.groupCall(x, c, g, p)
		}
		if pre, e, ok := b.intMapPrelude(x, c, f, p, stmt); ok { // ir_intmap.go
			return pre, e, false
		}
		switch check.CollOpOf(f) {
		case check.CollGet, check.CollPut, check.CollTake:
			return b.mapOp(x, c, p, stmt)
		}
		intrinsic := b.l.intrinsic(f) != nil
		if _, own := check.ModuleBodySource(f); own {
			return b.moduleCall(x, c, f, p) // ir_modulebody.go (LAWFFI LF2m)
		}
		if externFunc(f) && !intrinsic {
			return b.stdCall(x, c, f, p)
		}
		fd := b.l.useFunc(f, x)
		switch {
		case fd.extern != "" && !intrinsic:
			irNo("call of an extern function")
		case len(witnessParams(f.Sig)) > 0 && fd.method != nil:
			irNo("call that passes witnesses")
		case len(b.fc.captureOwners(x)) > 0:
			irNo("call that captures owners")
		case fd.ptr && !b.addrRecv(c) && !b.passesPointer(c, x): // ir_place.go, ir_pkg.go
			irNo("call of a pointer-receiver method on a value")
		}
		mark := func(e *irExpr) *irExpr { return e }
		if fd.fallible && !failsHere(c) {
			mark = b.dropErr
		}
		if !fd.envFree && b.env < 0 {
			irNo("call of an impure function from a pure one")
		}
		if pre, e, ok := b.sinkCall(x, c, f, fd, p); ok {
			return pre, e, false // ir_sink.go: written in place, pure
		}
		// Before the operands: a known site's literals are built plain.
		known := b.knownCall(x, c, fd) // ir_kcb.go
		args := x.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		var pre []*irStmt
		var irArgs []*irExpr
		if b.addrRecv(c) { // ir_place.go
			pre, irArgs = b.recvAddrCall(c, x.Args)
		} else {
			pre, irArgs = b.operands(args) // a row parameter's operand goes in by implicit (ir_rowgap.go)
		}
		b.valueSource(x, c, args, irArgs)   // ir_pkg.go
		b.l.intMapCall(x, c.Recv, f, false) // intmap.go: a generic signature's Map is Go's
		if x.Trailing != nil {
			irArgs = append(irArgs, b.closureLit(nil, x)) // ir_closure.go
		}
		if c.Site != nil {
			irArgs = append(irArgs, b.siteValue(x, c.Site, p))
		}
		if !fd.envFree {
			// The environment first, after a method's receiver
			// (callText, taskDestArgs).
			env := &irExpr{K: "local", Pos: p, I: b.env}
			at := 0
			if fd.method != nil {
				at = 1
			}
			irArgs = append(irArgs[:at:at], append([]*irExpr{env}, irArgs[at:]...)...)
		}
		if wc := b.witnessCopy(x, c); wc != "" && known == "" { // ir_witness.go
			return pre, mark(&irExpr{K: "call", Pos: p, Name: wc, Args: irArgs}), !fd.envFree
		}
		if known == "" && b.copyTakesNoWitness(x, c, fd) {
			// A copy for an instance whose witnesses are known (mono.go
			// monoWitnesses): its operations are its own.
		} else if wits := b.witnessArgs(f, x, p); len(wits) > 0 { // ir_witness.go
			// After the environment, before the operands (witnessArgs).
			at := 0
			if !fd.envFree {
				at = 1
			}
			irArgs = append(irArgs[:at:at], append(wits, irArgs[at:]...)...)
		}
		// A call of a known instance goes to its plain copy (mono.go), as
		// callText's does: a plain function, which Go compiles once, in
		// the copy's package, where an instance of the generic is
		// compiled again in each package that names it (MONO-HOOKS-MIN).
		// It takes the instance's witnesses, as callText's copy does.
		if known == "" && b.copyCall(x, c) == "" {
			if mono := b.fc.monoAt(x, c, f, c.TypeArgs); mono != nil {
				if fd.method != nil && !fd.envFree {
					// The copy of a method takes the environment
					// before its receiver (callText).
					irArgs = append([]*irExpr{irArgs[1], irArgs[0]}, irArgs[2:]...)
				}
				// The copy's own IR body is the inliner's to copy, as
				// the text path's OPT4a writes the instance's body at
				// its call (lru's Churn: Cache.Get; IR-GETMATCH).
				return pre, b.inlineMark(x, c, fd, mark(&irExpr{K: "call", Pos: p, Name: mono.name, Args: irArgs})), !fd.envFree // ir_forms_inline.go
			}
		}
		if known != "" && fd.method != nil {
			return pre, mark(&irExpr{K: "mcall", Pos: p, Name: known, X: irArgs[0], Args: irArgs[1:]}), !fd.envFree
		} else if known != "" {
			// The variant takes the function's type arguments (callText).
			targs := b.typeArgs(c, x)
			return pre, b.inlineMark(x, c, fd, mark(&irExpr{K: "call", Pos: p, Name: known, T: targs, Args: irArgs})), !fd.envFree // ir_forms_inline.go (KNOWN-FILTER-INLINE)
		}
		if fd.method != nil {
			return pre, b.droppedCall(x, c, fd, b.inlineMark(x, c, fd, mark(&irExpr{K: "mcall", Pos: p, Name: b.ownedCallName(x, f, fd.name), X: irArgs[0], Args: irArgs[1:]}))), !fd.envFree // ir_forms_inline.go, ir_forms_drop.go
		}
		// Append is Go's builtin append on the value rule's operands
		// (FLIP F3): Go then sees a place appended back to itself.
		if intrinsic && b.builtinAppend(f) { // ir_pkg.go
			return pre, &irExpr{K: "append", Pos: p, Args: irArgs}, false
		}
		// A generic function's call names its Go type arguments (the
		// emitter's typeArgs), never left to Go's inference (ir_pkg.go).
		if sp := b.copyCall(x, c); sp != "" {
			// The copy for the known record or provider: one instance
			// (ir_known.go), a provider's adapter at its parameter.
			env := 1
			if fd.envFree {
				env = 0
			}
			return pre, mark(&irExpr{K: "call", Pos: p, Name: sp, Args: b.provArg(x, c, irArgs, env)}), !fd.envFree
		}
		if sp := b.instanceCopy(x, c); sp != "" {
			// The copy for the instance (ir_instance.go): no type
			// arguments; no inliner mark and no variant with no result,
			// which name the generic function.
			return pre, mark(&irExpr{K: "call", Pos: p, Name: sp, Args: irArgs}), !fd.pure
		}
		name := b.ownedCallName(x, f, fd.name)
		if fd.pure && name == fd.name && x.Trailing == nil && c.Site == nil {
			// The function's table, as the emitter's call reads it (ir_tab.go).
			if tpre, v := b.tabCall(x, c, irArgs, p); v != nil {
				return append(pre, tpre...), v, false
			}
		}
		return pre, b.droppedCall(x, c, fd, b.inlineMark(x, c, fd, mark(&irExpr{K: "call", Pos: p, Name: name, T: b.typeArgs(c, x), Args: irArgs}))), !fd.envFree // ir_forms_inline.go, ir_forms_drop.go
	case check.CallOp:
		return b.opCall(x, c, p) // ir_pkg.go
	case check.CallConversion:
		return b.conversion(x, c, p) // ir_pkg.go
	case check.CallEncode, check.CallDecode:
		return b.codecCall(x, c, p) // ir_codec.go
	case check.CallPattern:
		return b.patternCall(x, c, p) // refinematch.go
	case check.CallProviderOp, check.CallLawOp:
		switch {
		case b.env < 0 || c.Op == nil:
			irNo("operation call form")
		case b.fc.entersRelease(x):
			irNo("operation call that enters a release")
		}
		// A test build calls a capability's operation as a program does
		// (calls.go: only a law's own operation, CallLawOp, differs).
		named := b.lawOpCap(c) // ir_assert.go: a law's bare operation call
		if named == nil {
			sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
			if !ok {
				irNo("operation call spelling")
			}
			named, ok = b.l.info.Types[sel.X].(*check.Named)
			if !ok {
				irNo("capability without a named type")
			}
		}
		// The interruption point, before the operands (callText).
		var pre []*irStmt
		if b.fc.frame.polls {
			pre = append(pre, b.poll(x))
		}
		env := &irExpr{K: "local", Pos: p, I: b.env}
		if c.Kind == check.CallLawOp && b.l.runtimeNamed(named, x) {
			recv := &irExpr{K: "local", Pos: p, I: b.lawOwn}
			if b.lawOwn == 0 {
				recv = &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}
			}
			argPre, e, effect := b.boundOpCall(x, c, named, p, recv)
			return append(pre, argPre...), e, effect
		}
		argPre, irArgs := b.operands(x.Args)
		pre = append(pre, argPre...)
		args := append([]*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}, irArgs...)
		if c.Kind == check.CallLawOp && b.lawOwn > 0 {
			// A law of a generic operation type calls the provider it
			// takes, as a call on a value of the type does (calls.go).
			own := &irExpr{K: "local", Pos: p, I: b.lawOwn}
			return pre, b.opResult(c, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: own, Args: args}), true
		}
		capField := b.knownOp(x, named, &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}) // ir_forms_op.go
		return pre, b.opResult(c, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}), true
	case check.CallGoError:
		return b.goBuiltin(x, c, p) // ir_golib.go: go.New, go.Zero, go.Is, go.As (IR-GOCALLS)
	case check.CallFuncValue:
		pre, e := b.valueCall(x, c, p) // ir_closure.go
		// A known callback's call is pure (ir_kcb.go).
		return pre, e, !b.plainCallee(x)
	}
	irNo("call kind %d", c.Kind)
	return nil, nil, false
}
