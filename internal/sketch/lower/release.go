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
