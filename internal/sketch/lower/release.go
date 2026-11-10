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
	if fc.frame.unit {
		if len(vals) != 1 {
			ice(nil, "a return of %d results from a function value with no result", len(vals))
		}
		fc.line(fc.errReturn(vals[0]))
		return
	}
	if fl := fc.frame.flat; fl != nil {
		// The flat step (machinerun.go): a decision held as the union,
		// returned through a with's flow, is given as a value.
		if len(vals) != 1 {
			ice(nil, "a return of %d results from a machine's flat step", len(vals))
		}
		vals = []string{fl.flat + "{}.decide(" + vals[0] + ")"}
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

// Owned values (#197 L8 B1; resources R5-R10, R21, R41, R43). The checker
// gives each owned local, taken parameter and temporary its release
// (Info.OwnedVars, Info.OwnedTemps), marks each hand-off (Info.Handoffs)
// and records the release plan (Info.Releases). The lowering registers
// each value on its frame's one list in the step that receives it
// (v1.Owning, R8); a hand-off removes the entry (v1.Hand), a return keeps
// it until the frame's exit is known (v1.Returned); a scope that ends
// inside the frame releases what the plan names there (Owned.Release),
// and the frame's End releases the rest on every edge that leaves the
// function, each release after an earlier defect, every defect kept (R9).
// The entry's presence is the drop flag (R21). This is the plain
// lowering; P-L8 adds the static fast path and keeps this one selectable.

// ownBinding is the v1 body of an ownership binding.
func (fc *funcCtx) ownBinding(name string, site syntax.Node) v1.Body {
	body, err := v1.BodyOf(name)
	if err != nil {
		ice(site, "the ownership binding %s has no runtime/v1 body: %v", name, err)
	}
	return body
}

// ownRelease is the Go release of an owned value of type t: a function of
// the value that calls its checked Close, which masks its own body.
func (fc *funcCtx) ownRelease(cl *check.Close, t check.Type, site syntax.Node) string {
	v := fc.temp("owned")
	text := fc.providerRelease(&check.Acquisition{Close: cl}, fc.envExpr(site), v, site)
	return "func(" + v + " " + fc.goType(t) + ") { " + text + " }"
}

// own registers val, a value of type t that key's owner owns, and returns
// the Go expression that yields it: the step that receives the value.
func (fc *funcCtx) own(key any, cl *check.Close, t check.Type, val string, site syntax.Node) string {
	if fc.l.owned == nil {
		fc.l.owned = map[any]string{}
	}
	h := fc.temp("own")
	fc.l.owned[key] = h
	fc.line("var " + h + " *" + fc.l.rt() + ".Owned")
	return fc.owning(h, val, fc.ownRelease(cl, t, site), site)
}

// owning is the registration of val in the entry h on the frame scope
// (v1.Owning): its hidden frame is the code's frame scope, written here
// since the call has no call site of its own (hiddenArgs).
func (fc *funcCtx) owning(h, val, rel string, site syntax.Node) string {
	body := fc.ownBinding("Owning", site)
	if len(body.Hidden) != 1 || body.Hidden[0] != v1.Frame {
		ice(site, "the binding Owning does not take the hidden frame alone")
	}
	if _, ok := runtimeBindings["Owning"]; !ok {
		ice(site, "the binding Owning is not in the runtime catalog")
	}
	return fc.l.rt() + ".Owning(" + fc.frameScope(site) + ", &" + h + ", " + val + ", " + rel + ")"
}

// ownVar registers the local or parameter v, already bound to its Go
// variable, when the checker gives it an owner.
func (fc *funcCtx) ownVar(v *check.Var, site syntax.Node) {
	cl := fc.l.info.OwnedVars[v]
	if cl == nil {
		return
	}
	fc.line(fc.own(v, cl, v.Type, fc.locals[v], site))
}

// ownTemp registers the value of x, a temporary its statement owns (R43),
// in the operand o: on success only, when x may fail.
func (fc *funcCtx) ownTemp(x syntax.Expr, o operand) operand {
	cl := fc.l.info.OwnedTemps[x]
	if cl == nil || o.val == "" {
		return o
	}
	if o.err == "" {
		o.val = fc.own(x, cl, fc.typeOf(x), o.val, x)
		return o
	}
	val := fc.temp("t")
	fc.line(val + " := " + o.val)
	reg := fc.own(x, cl, fc.typeOf(x), val, x)
	fc.line("if " + o.err + " == nil {")
	fc.line("\t" + reg)
	fc.line("}")
	o.val = val
	return o
}

// handoff is the Go value of id, a use of an owned value: as it is, or, at
// a hand-off, with its entry removed (v1.Hand), or kept until the frame's
// exit is known when the frame returns it (v1.Returned).
func (fc *funcCtx) handoff(id *syntax.Ident, val string, returned bool) string {
	if !fc.l.info.Handoffs[id] {
		return val
	}
	v, _ := fc.l.info.Uses[id].(*check.Var)
	h := fc.l.owned[v]
	if h == "" {
		return val // a value of a type parameter: released by its instance (B2)
	}
	name := "Hand"
	if returned {
		name = "Returned"
	}
	return fc.runtimeCall(name, fc.ownBinding(name, id), "", "", []string{h, val}, "", nil, nil, id)
}

// releaseAt writes the releases the plan records at key, a place that
// ends inside the frame: last acquired first. A value whose entry left
// the list (a hand-off on this path) is skipped by its entry (R21).
func (fc *funcCtx) releaseAt(key syntax.Node) {
	for _, r := range fc.l.info.Releases[key] {
		var h string
		if r.Var != nil {
			h = fc.l.owned[r.Var]
		} else {
			h = fc.l.owned[r.Temp]
		}
		if h == "" {
			continue // a value of a type parameter (B2)
		}
		fc.line(fc.runtimeCall("Owned.Release", fc.ownBinding("Owned.Release", key), "", h, nil, "", nil, nil, key))
	}
}

// reassignOwned writes x = val, x an owned local (R41): the new value is
// registered first, then the old one is released where the plan says it
// is still owned, then x takes the new value and its entry. So a defect
// in the old value's release leaves the new one owned. It reports false
// for any other target.
func (fc *funcCtx) reassignOwned(lhs syntax.Expr, target, val string) bool {
	id, ok := unparen(lhs).(*syntax.Ident)
	if !ok {
		return false
	}
	v, _ := fc.l.info.Uses[id].(*check.Var)
	cl := fc.l.info.OwnedVars[v]
	h := fc.l.owned[v]
	if cl == nil || h == "" {
		return false
	}
	t := fc.temp("t")
	fc.line(t + " := " + val)
	next := fc.temp("own")
	fc.line("var " + next + " *" + fc.l.rt() + ".Owned")
	fc.line(fc.owning(next, t, fc.ownRelease(cl, v.Type, id), id))
	fc.releaseAt(id)
	fc.line(target + " = " + t)
	fc.line(h + " = " + next)
	return true
}

// scoped writes a scope's statements through write and, when they can
// end normally, the releases of the scope's end (Info.Releases[key]).
func (fc *funcCtx) scoped(key syntax.Node, write func() bool) bool {
	t := write()
	if !t {
		fc.releaseAt(key)
	}
	return t
}

// bodyOwns reports a body that owns a value: a frame scope holds its
// entries.
func (l *lowerer) bodyOwns(list []syntax.Stmt) bool {
	found := false
	for _, s := range list {
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.CallExpr:
				if c := l.info.Calls[n]; n.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
					syntax.Inspect(n.Fun, func(m syntax.Node) bool { found = found || l.ownsNode(m); return !found })
					for _, a := range n.Args {
						syntax.Inspect(a, func(m syntax.Node) bool { found = found || l.ownsNode(m); return !found })
					}
					found = found || l.ownsNode(n)
					return false
				}
			}
			found = found || l.ownsNode(n)
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// ownsNode reports a binding or temporary with an owner.
func (l *lowerer) ownsNode(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.Ident:
		v, ok := l.info.Defs[n].(*check.Var)
		return ok && l.info.OwnedVars[v] != nil
	case syntax.Expr:
		return l.info.OwnedTemps[n] != nil
	}
	return false
}

// ownsParams reports a signature with a taken parameter that has an owner.
func (l *lowerer) ownsParams(params []*check.Var) bool {
	for _, v := range params {
		if l.info.OwnedVars[v] != nil {
			return true
		}
	}
	return false
}
