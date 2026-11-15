package lower

import (
	"reflect"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// runtimeBindings is the catalog of runtime Go bindings, derived from the
// policy marks of runtime/v1's bodies (bodycontract.go). runtimeCall is
// their single emitter. A new body fails closed until its Go declaration
// states its waiting policy (//ergo:wait); the blocking property derives
// its primitive axis from this catalog. Its release routes are the release owner's (release.go); the
// mask property derives its release axis from them.
type waitTask uint8

const (
	noWait     waitTask = iota
	callerWait          // the hidden *rt.Task, from the invocation environment
	handleWait          // the handle's owner; Move changes the owning frame, not task
	drainWait           // uninterruptible stop and completed drain
	// childWait is the hidden *rt.Task of the invocation environment as the
	// parent of the children the body starts and joins (timeout.After,
	// through rt.AfterSpan). It parks on no primitive of its own: the
	// children's exits and the parent's stop end its wait, and the
	// runtime/v1 laws of After (timeout_test.go) are its native property.
	childWait
	// memberTask is the hidden *rt.Task of the invocation environment, which
	// the body makes the first member of an auto clock's domain
	// (testing.NewVirtualClock); the body never waits on it.
	memberTask
	// parentTask is the hidden *rt.Task of the invocation environment as
	// the parent of the task the body starts and the owner it checks
	// (tasks.Go); the body never waits on it.
	parentTask
	// settleWait is the hidden *rt.Task of the invocation environment,
	// which waits until every other member of a manual clock's domain is
	// parked (time.Manual's Advance, fua-b). It parks on no primitive of
	// its own; a stop of the caller ends the wait, and runtime/v1
	// TestManualAdvanceSettles states its laws.
	settleWait
	// endWait is the hidden *rt.Task of the invocation environment, which
	// waits uninterruptibly for an actor's runner to end (Actor.Stop and
	// Actor.Drain): a stop of the caller does not end it, and in a clock's
	// domain the caller waits parked on the actor, so a manual clock's
	// Advance does not wait for it (fua-b, batch-13 round 2 B1).
	endWait
)

type runtimeBinding struct {
	wait waitTask
	// release is the binding's release route (release.go), notRelease for
	// every binding that is not an entry into runtime/v1's release boundary.
	release releaseRoute
	// A host-provider bridge erases one ordinary, zero-field provider
	// operand. The Go body's hidden parameters still come from BodyOf.
	hostTarget, provider string
	// reads marks a binding that reads the invocation task's context
	// values (runtime.md, G1): it takes the task and reading package as
	// hidden arguments and never waits.
	reads bool
	// task marks a binding that takes the invocation's task to know where
	// it runs and never waits: actor.NewHost asks whether it runs in a
	// step of an actor (runtime.md, Machines and actors 16).
	// It also preserves the ordinary operation-type ABI on native values
	// that implement a source operation type (embed.FS implements fs.Files).
	task bool
	// body is the Go body the policy was read from (bodyCatalog).
	body v1.Body
}

// takesCaller reports a binding whose Go body receives the invocation's
// task as its hidden CallerTask argument.
func (b runtimeBinding) takesCaller() bool {
	return b.wait == callerWait || b.wait == childWait || b.wait == memberTask || b.wait == parentTask || b.wait == settleWait || b.wait == endWait || b.release == callerRelease || b.reads || b.task
}

// goDurationName is the Go type a body's duration operand has, as the body
// declares it (v1.Param.Type): Go's time.Duration, read from Go itself.
var goDurationName = reflect.TypeFor[time.Duration]().String()

// runtimeBindings is the catalog, derived from the marks of runtime/v1's
// bodies (bodyCatalog, body.go): no body name is written here.
var runtimeBindings = bodyCatalog()

// runtimeCall binds a std body's hidden arguments at the point it runs.
// stdResult calls it inside the invocation environment for a deferred Seq.
// Wait uses the handle's owning task inside the primitive; Stop drains
// uninterruptibly. No extra poll is needed for any of these primitives.
func (fc *funcCtx) runtimeCall(name string, body v1.Body, typeArgs, recv string, vals []string, env string, x *syntax.CallExpr, c *check.Call, site syntax.Node) string {
	binding, ok := runtimeBindings[name]
	pkg := fc.l.rt()
	if i := strings.LastIndex(name, "."); i >= 0 && strings.Contains(name[:i], "/") {
		// A body of the module's own Go package (check.ModuleBody), named
		// by its full target: the policy it states, read with its shape.
		binding, ok = bindingOf(body), body.Wait != ""
		pkg, name = fc.l.hostPkg(name[:i], site), name[i+1:]
	}
	if !ok {
		ice(site, "runtime binding %s has no waiting policy", name)
	}
	if hasHidden(body.Hidden, v1.CallerTask) != binding.takesCaller() {
		ice(site, "runtime binding %s does not receive its declared waiting task", name)
	}
	if hasHidden(body.Hidden, v1.ReadingPackage) != binding.reads {
		ice(site, "runtime binding %s does not receive its declared context package", name)
	}
	if x != nil && binding.wait != noWait && fc.l.info.Closes[x] != nil {
		// A std Close is a release (R9). Today std declares no Close (the
		// queue's half-close is End, #197 q53); a waiting one, such
		// as Task.Close (L8 B3), needs a release route first.
		ice(site, "the std Close binding %s waits, and no release route masks it", name)
	}
	var head string
	if recv == "" {
		head = pkg + "." + name + typeArgs
	} else {
		i := strings.LastIndex(name, ".")
		if i < 0 || typeArgs != "" {
			ice(site, "runtime method binding %s has an invalid call shape", name)
		}
		head = "(" + recv + ")." + memberName(name[i+1:])
	}
	return head + "(" + strings.Join(append(fc.hiddenArgs(body.Hidden, x, c, env, site), vals...), ", ") + ")"
}

// hostBlockingIntrinsic adapts the catalog's finite host-provider bridges.
// Identity is the declared Go target, never the source function's name.
// The extern path has no host signature loader (D-E5); the trusted bridge
// records only its erased provider. BodyOf owns the Go signature, including
// its hidden task. A user extern is never a bridge: its call is a direct Go
// call (hostCall).
func (l *lowerer) hostBlockingIntrinsic(f *check.Func) func(*funcCtx, []string) string {
	target := strings.Trim(f.Decl.Extern.Target.Value, "\"`")
	for name, binding := range runtimeBindings {
		if binding.hostTarget == "" || target != binding.hostTarget {
			continue
		}
		body := binding.body
		s := f.Sig
		if len(s.TParams) != 0 || len(s.Results) != 0 || len(body.Results) != 0 || len(s.Params) != len(body.Params)+1 || !isPreludeType(s.Params[0].Type, binding.provider) {
			return nil
		}
		for i, param := range body.Params {
			// A provider bridge currently admits duration operands. A new
			// Go operand shape fails closed until the host path owns it.
			if param.Type != goDurationName || !isBasic(s.Params[i+1].Type, check.Duration) {
				return nil
			}
		}
		return func(fc *funcCtx, params []string) string {
			return "\t" + fc.runtimeCall(name, body, "", "", params[1:], fc.envExpr(fc.site), nil, nil, fc.site) + "\n"
		}
	}
	return nil
}
