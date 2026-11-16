package lower

import (
	v1 "ergo.local/prototype/runtime/v1"
)

// The lowering's body contract (STD-NOPRIV NP4d; docs/spec/interop.md,
// Trusted Go; GLOSSARY.md, Body contract): this file is where the lowering
// reads what runtime/v1 states about its Go bodies (v1.BodyOf, v1.BodyNames),
// as check/body.go is for the checker. A body's calling policy is the
// directive lines on its Go declaration (//ergo:wait, //ergo:release,
// //ergo:bridge), read with its signature, so no table of the lowering
// names a body. TestCompilerReadsNoStdMembership exempts this file from the
// rule on runtime/v1's fact reads and holds it to every other rule.
//
// Prior art: Go's compiler reads a runtime function's ABI from its
// declaration in the runtime package (cmd/compile's typecheck builtin
// table is generated from runtime's builtin declarations, mkbuiltin.go),
// not from a hand table beside the code that calls it.

// bodyCatalog is the binding catalog: every runtime/v1 body that states a
// waiting policy (Body.Wait), with its policy. A body with no mark is not
// in it, so runtimeCall refuses it (fails closed).
func bodyCatalog() map[string]runtimeBinding {
	out := map[string]runtimeBinding{}
	for _, name := range v1.BodyNames() {
		body, err := v1.BodyOf(name)
		if err != nil || body.Wait == "" {
			continue
		}
		out[name] = bindingOf(body)
	}
	return out
}

// waitKinds are the lowering's names of the wait kinds v1.WaitKinds lists.
var waitKinds = map[string]waitTask{
	"none": noWait, "caller": callerWait, "handle": handleWait, "drain": drainWait, "child": childWait,
	"member": memberTask, "parent": parentTask, "settle": settleWait, "end": endWait,
}

// releaseRoutes are the lowering's names of the routes v1.ReleaseRoutes
// lists.
var releaseRoutes = map[string]releaseRoute{"": notRelease, "caller": callerRelease, "frame": frameRelease}

// bindingOf is the calling policy body states. Its hidden arguments give
// the rest: a body that takes the context package reads context values,
// and one that takes the caller's task for no wait or release of its own
// takes it to know where it runs (task).
func bindingOf(body v1.Body) runtimeBinding {
	wait, ok := waitKinds[body.Wait]
	if !ok {
		panic("lower: runtime/v1 wait kind " + body.Wait + " has no lowering name")
	}
	release, ok := releaseRoutes[body.Release]
	if !ok {
		panic("lower: runtime/v1 release route " + body.Release + " has no lowering name")
	}
	b := runtimeBinding{wait: wait, release: release, hostTarget: body.BridgeTarget, provider: body.BridgeProvider, body: body}
	b.reads = hasHidden(body.Hidden, v1.ReadingPackage)
	b.task = hasHidden(body.Hidden, v1.CallerTask) && !b.takesCaller()
	return b
}

// runtimeBody is the runtime/v1 body name: a body the lowering itself
// calls for a language construct (a with's scope, a checked Close's
// release, the frame's ownership list, an embedded tree), or the body an
// extern's target or a runtime-bound type's operation names.
func runtimeBody(name string) (v1.Body, error) {
	return v1.BodyOf(name)
}
