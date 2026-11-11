package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// The release owner (runtime.md, I2; resources.md, R1 and R9). A release
// runs masked from its first instruction, its first capability checkpoint
// included, to its last, on the task that runs it. Its defect joins the
// exit it ends with, and a stop it masks stays pending for the caller's
// next checkpoint. runtime/v1 holds the one mask and cause boundary
// (release in task.go). Each lowered release reaches it through one
// binding of runtimeBindings whose release route is not notRelease: those
// bindings are the release catalog, and runtimeCall is their one emitter.
// TestRuntime199MaskProperty derives its release axis from the catalog;
// the owner scan fails a route that masks or releases on its own.
type releaseRoute uint8

const (
	notRelease releaseRoute = iota
	// callerRelease is the body of a checked Close (Info.Closes on its
	// declaration), masked on the invocation's task (v1.Release). Its Go
	// function is the one body every dispatch path reaches: a call, a dot
	// call, an operation of an operation type or a capability (whose
	// adapter calls it), a function value and a method value. So the mask
	// is in the body and no dispatch path holds a copy of the rule.
	callerRelease
	// frameRelease is an exit action (v1.OnExit), masked on its frame's
	// task when the frame ends. Its callback runs in the environment
	// captured where it was registered; a frame never changes its task, so
	// that is the ending task.
	frameRelease
)

// No generated Close route exists: aggregate Close synthesis is #197. A
// generated Close declares its route here, or lowers as a checked Close.

// isClose reports a checked Close: the checker's identity fact on f's
// declaration (lesson 2), never its spelling.
func isClose(f *check.Func) bool {
	return f != nil && f.Decl != nil && f.Pkg != nil && f.Pkg.Info != nil && f.Pkg.Info.Closes[f.Decl] != nil
}

// providerRelease is the release of a provider an owning with built: the
// call of its checked Close (Acquisition.Close, the checker's fact) on val,
// the provider's value, in outer, the environment outside the with, where
// the checker charged the release's needs. The Close's Go function masks
// its own body (closeBody), so the call adds no mask, as no dispatch path
// to a checked Close adds one. Each form of C29 releases (lower-v1 stage
// 4):
//   - a Close that is an operation of the provider's operation type calls
//     the operation, whose implementation is a checked Close that masks
//     its own body;
//   - a generic Close is called with the type arguments that its
//     parameter's type binds against the provider's type;
//   - a Close that takes a pointer to a provider that is a value takes the
//     address of the with's own copy of the provider (publishProvider
//     spills it), which no operation of the block sees.
func (fc *funcCtx) providerRelease(a *check.Acquisition, outer, val string, site syntax.Node) string {
	cl := a.Close
	if cl.Func == nil {
		if cl.Op == nil {
			ice(site, "a provider's Close with neither a function nor an operation")
		}
		text, _, _ := fc.opCall(cl.Receiver, cl.Op, nil, []string{val}, outer, "", nil, nil, site)
		return text
	}
	if !isClose(cl.Func) || len(cl.Func.Sig.Params) != 1 {
		ice(site, "a provider's Close with no checked Close identity")
	}
	fd := fc.l.useFunc(cl.Func, site)
	if _, _, std := fc.l.stdBody(fd); std || fd.extern != "" {
		// A Close takes a named type, outside a user extern's domain (C26),
		// and no std package declares a Close function.
		ice(site, "a provider whose Close is a std or extern function, which the checker refuses (C26)")
	}
	_, byPtr := cl.Func.Sig.Params[0].Type.(*check.Pointer)
	_, ptrValue := cl.Receiver.(*check.Pointer)
	recv := val
	switch {
	case byPtr && !ptrValue:
		recv = "&" + val
	case !byPtr && ptrValue:
		recv = "*" + val
	}
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + outer + ")"
	}
	targs := ""
	if len(cl.Func.Sig.TParams) > 0 {
		if len(witnessParams(cl.Func.Sig)) > 0 {
			ice(site, "a provider whose generic Close has a type parameter with operations, which the checker refuses (S109, closeWitnessed; TestCloseRefusals)")
		}
		targs = fc.typeArgs(cl.Func.Sig, closeTypeArgs(cl, site), site)
	}
	return fd.name + targs + "(" + outer + ", " + recv + ")"
}

// closeTypeArgs are the type arguments of a generic Close's call: each of
// its type parameters, bound by matching its parameter's type against the
// provider's type (the receiver G[int] against the parameter G[T] or
// *G[T]). Rows are erased (typeArgs skips them).
func closeTypeArgs(cl *check.Close, site syntax.Node) []check.Type {
	sig := cl.Func.Sig
	binds := map[*check.TypeParam]check.Type{}
	param, recv := sig.Params[0].Type, cl.Receiver
	if p, ok := param.(*check.Pointer); ok {
		param = p.Elem
	}
	if p, ok := recv.(*check.Pointer); ok {
		recv = p.Elem
	}
	var match func(p, r check.Type)
	match = func(p, r check.Type) {
		switch p := p.(type) {
		case *check.TypeParam:
			binds[p] = r
		case *check.Named:
			rn, ok := r.(*check.Named)
			if !ok || len(rn.TArgs) != len(p.TArgs) {
				ice(site, "the Close parameter %s does not match the provider %s", p, r)
			}
			for i := range p.TArgs {
				match(p.TArgs[i], rn.TArgs[i])
			}
		case *check.Pointer:
			if rp, ok := r.(*check.Pointer); ok {
				match(p.Elem, rp.Elem)
			}
		case *check.Slice:
			if rs, ok := r.(*check.Slice); ok {
				match(p.Elem, rs.Elem)
			}
		case *check.Array:
			if ra, ok := r.(*check.Array); ok {
				match(p.Elem, ra.Elem)
			}
		case *check.Map:
			if rm, ok := r.(*check.Map); ok {
				match(p.Key, rm.Key)
				match(p.Elem, rm.Elem)
			}
		}
	}
	match(param, recv)
	args := make([]check.Type, len(sig.TParams))
	for i, tp := range sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		t, ok := binds[tp]
		if !ok {
			ice(site, "the generic Close %s does not bind its type parameter %s from the provider %s", cl.Func.Name, tp.Name, cl.Receiver)
		}
		args[i] = t
	}
	return args
}

// providerCloser returns the checked release callback of this acquisition.
// The environment is captured from outside the with, where its release
// needs were charged. A plain provider has no release.
func (fc *funcCtx) providerCloser(a *check.Acquisition, outer, val string, site syntax.Node) string {
	if a.Close == nil {
		return "nil"
	}
	saved := fc.env
	fc.env = outer
	callback := fc.envClosure(capturedEnv, "", site, func(env string) ([]string, string, string) {
		return nil, "", strings.Repeat("\t", fc.depth+1) + fc.providerRelease(a, env, val, site) + "\n"
	})
	fc.env = saved
	return callback
}

// acquireFlow transports lexical control through a native callback. It is
// not a source frame: its return/failure and loop targets still belong to
// the enclosing source frame (check/frame.go, ContextLit). A completed
// construction alone returns a release to WithAcquire for registration.
type acquireFlow struct {
	flow, value, err string // 0 completes, 1 returns, 2 breaks, 3 continues
	loops            int
}

// sourceReturn writes the return of the source frame. Acquisition uses a
// native callback solely to delimit rt's acquiring region, so a lexical
// return inside an inline provider argument crosses it explicitly.
func (fc *funcCtx) sourceReturn(vals ...string) {
	if a := fc.acquiring; a != nil {
		var dest []string
		if a.value != "" {
			dest = append(dest, a.value)
		}
		if a.err != "" {
			dest = append(dest, a.err)
		}
		if len(dest) > 0 {
			fc.line(strings.Join(dest, ", ") + " = " + strings.Join(vals, ", "))
		}
		fc.line(a.flow + " = 1")
		fc.line("return nil, nil")
		return
	}
	if fc.frame.inline != nil {
		fc.inlineReturn(vals)
		return
	}
	if fc.frame.unit {
		if len(vals) != 1 {
			ice(nil, "a return of %d results from a function value with no result", len(vals))
		}
		fc.line(fc.errReturn(vals[0]))
		return
	}
	fc.line("return " + strings.Join(vals, ", "))
}

// withClose extends the with lifetime owner to construction and release
// registration as one acquisition. No other lowering path registers a
// provider. WithAcquire reuses v1's list and rt's acquiring region, and
// restores the mask before this code handles an error or lexical transfer.
func (fc *funcCtx) withClose(a *check.Acquisition, target, outer, ws string, site syntax.Node) {
	f := fc.frame
	flow := &acquireFlow{flow: fc.temp("flow"), loops: len(fc.loops)}
	fc.line("var " + flow.flow + " int")
	if f.result != nil {
		flow.value = fc.temp("returned")
		fc.line("var " + flow.value + " " + fc.goType(f.result))
	}
	if f.fallible {
		flow.err = fc.temp("returnedErr")
		fc.line("var " + flow.err + " error")
	}
	open := fc.envClosure(capturedEnv, "", site, func(env string) ([]string, string, string) {
		inner := *fc
		inner.out = &strings.Builder{}
		inner.depth++
		inner.acquiring = flow
		val := inner.publishProvider(a, target)
		inner.line("return " + inner.providerCloser(a, outer, val, site) + ", nil")
		return nil, " (func(), error)", inner.out.String()
	})
	err := fc.temp("acquireErr")
	fc.line(err + " := " + fc.scopeCall("Scope.WithAcquire", ws, []string{open}, site))
	fc.line("if " + err + " != nil {")
	fc.depth++
	fc.line(fc.scopeCall("Scope.EndWith", ws, []string{"&" + err}, site))
	fc.fail(err)
	fc.depth--
	fc.line("}")
	fc.line("if " + flow.flow + " == 1 {")
	fc.depth++
	errArg := "nil"
	var vals []string
	if flow.value != "" {
		vals = append(vals, flow.value)
	}
	if flow.err != "" {
		errArg = "&" + flow.err
		vals = append(vals, flow.err)
	}
	fc.line(fc.scopeCall("Scope.EndWith", ws, []string{errArg}, site))
	fc.sourceReturn(vals...)
	fc.depth--
	fc.line("}")
	if flow.loops > 0 {
		for _, branch := range []struct {
			code string
			tok  syntax.Token
		}{{"2", syntax.BREAK}, {"3", syntax.CONTINUE}} {
			fc.line("if " + flow.flow + " == " + branch.code + " {")
			fc.depth++
			fc.line(fc.scopeCall("Scope.EndWith", ws, []string{"nil"}, site))
			fc.branch(&syntax.BranchStmt{Tok: branch.tok})
			fc.depth--
			fc.line("}")
		}
	}
}

// entersRelease reports a call whose callee is a checked Close. The call
// starts a release, so it is not an interruption point of its own: a
// capability's Close writes no poll before its operation, as a direct
// Close call has none, and the stop stays pending for the caller's next
// checkpoint after the release.
func (fc *funcCtx) entersRelease(x *syntax.CallExpr) bool {
	return fc.l.info.Closes[x] != nil
}

// closeBody writes emit's statements, the whole body of the function f,
// inside the release of a checked Close: frame scope, entry poll and
// source body, so no checkpoint precedes the mask. Any other function's
// body is written as it is.
func (fc *funcCtx) closeBody(f *check.Func, site syntax.Node, emit func()) {
	if !isClose(f) {
		emit()
		return
	}
	if len(f.Sig.Results) != 0 || len(rowElems(f.Sig.Errors)) != 0 {
		ice(site, "a checked Close with a result or a failure row")
	}
	const name = "Release"
	if runtimeBindings[name].release != callerRelease {
		ice(site, "the binding %s is not the caller release route", name)
	}
	body, err := v1.BodyOf(name)
	if err != nil {
		ice(site, "the release route %s has no runtime/v1 body: %v", name, err)
	}
	outer, depth := fc.out, fc.depth
	fc.out = &strings.Builder{}
	fc.depth = depth + 1
	emit()
	inner := fc.out.String()
	fc.out, fc.depth = outer, depth
	block := "func() {\n" + inner + strings.Repeat("\t", depth) + "}"
	fc.line(fc.runtimeCall(name, body, "", "", []string{block}, fc.envExpr(site), nil, nil, site))
}
