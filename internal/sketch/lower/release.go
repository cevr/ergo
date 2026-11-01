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
// to a checked Close adds one. A Close this route cannot call by its Go
// name is refused with its owner.
func (fc *funcCtx) providerRelease(a *check.Acquisition, outer, val string, site syntax.Node) string {
	cl := a.Close
	if cl.Func == nil {
		refuse(site, "a provider whose Close is an operation of its type is released after check-v1 stage E")
	}
	if len(cl.Func.Sig.TParams) > 0 {
		refuse(site, "a provider whose Close is generic is released after check-v1 stage E")
	}
	if !isClose(cl.Func) || len(cl.Func.Sig.Params) != 1 {
		ice(site, "a provider's Close with no checked Close identity")
	}
	fd := fc.l.useFunc(cl.Func, site)
	if _, _, std := fc.l.stdBody(fd); std || fd.extern != "" {
		refuse(site, "a provider whose Close is a std or extern function is released after check-v1 stage E")
	}
	_, byPtr := cl.Func.Sig.Params[0].Type.(*check.Pointer)
	_, ptrValue := cl.Receiver.(*check.Pointer)
	recv := val
	switch {
	case byPtr && !ptrValue:
		refuse(site, "a provider whose Close takes a pointer to its value is released after check-v1 stage E")
	case !byPtr && ptrValue:
		recv = "*" + val
	}
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + outer + ")"
	}
	return fd.name + "(" + outer + ", " + recv + ")"
}

// withClose registers, in ws, the scope of the owning with, the release of
// the provider val that the with's builder has just built. The callback
// runs in outer, the environment outside the with (a captured env: the
// scope ends on the task that opened it, as a frame does), and calls the
// provider's checked Close (providerRelease).
func (fc *funcCtx) withClose(a *check.Acquisition, outer, ws, val string, site syntax.Node) {
	saved := fc.env
	fc.env = outer
	callback := fc.envClosure(capturedEnv, "", site, func(env string) ([]string, string, string) {
		return nil, "", strings.Repeat("\t", fc.depth+1) + fc.providerRelease(a, env, val, site) + "\n"
	})
	fc.env = saved
	fc.line(fc.scopeCall("Scope.WithClose", ws, []string{callback}, site))
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
