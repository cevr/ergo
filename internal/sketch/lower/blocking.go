package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// runtimeBindings is the catalog of std Go bindings. runtimeCall is their
// single emitter. A new binding fails closed until its waiting policy is
// stated here; the blocking property derives its primitive axis from this
// catalog. Frame release is owned separately by openFrame/Scope.End.
type waitTask uint8

const (
	noWait     waitTask = iota
	callerWait          // the hidden *rt.Task, from the invocation environment
	handleWait          // the handle's owner; Move changes the owning frame, not task
	drainWait           // uninterruptible stop and completed drain
)

type runtimeBinding struct {
	wait waitTask
	// A host-provider bridge erases one ordinary, zero-field provider
	// operand. The Go body's hidden parameters still come from BodyOf.
	hostTarget, provider string
}

var runtimeBindings = map[string]runtimeBinding{
	"Sleep":        {wait: callerWait, hostTarget: runtimeModule + "/runtime/rt.Sleep", provider: "LiveScheduler"},
	"Task.Wait":    {wait: handleWait},
	"Task.Stop":    {wait: drainWait},
	"Queue.Push":   {wait: callerWait},
	"Queue.Pop":    {wait: callerWait},
	"Queue.Values": {wait: callerWait},
	"Queue.Close":  {},
	"Go":           {},
	"OnExit":       {},
	"New":          {},
	"WithCapacity": {},
}

// runtimeCall binds a std body's hidden arguments at the point it runs.
// stdResult calls it inside the invocation environment for a deferred Seq.
// Wait uses the handle's owning task inside the primitive; Stop drains
// uninterruptibly. No extra poll is needed for any of these primitives.
func (fc *funcCtx) runtimeCall(name string, body v1.Body, typeArgs, recv string, vals []string, env string, x *syntax.CallExpr, c *check.Call, site syntax.Node) string {
	binding, ok := runtimeBindings[name]
	if !ok {
		ice(site, "runtime binding %s has no waiting policy", name)
	}
	if hasHidden(body.Hidden, v1.CallerTask) != (binding.wait == callerWait) {
		ice(site, "runtime binding %s does not receive its declared waiting task", name)
	}
	var head string
	if recv == "" {
		head = fc.l.rt() + "." + name + typeArgs
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
// The extern path has no general host signature loader yet (stage E); the
// trusted bridge records only its erased provider. BodyOf owns the Go
// signature, including its hidden task. User externs remain refused.
func (l *lowerer) hostBlockingIntrinsic(f *check.Func) func(*funcCtx, []string) string {
	target := strings.Trim(f.Decl.Extern.Target.Value, "\"`")
	for name, binding := range runtimeBindings {
		if binding.hostTarget == "" || target != binding.hostTarget {
			continue
		}
		body, err := v1.BodyOf(name)
		if err != nil {
			ice(f.Decl, "host bridge %s has no Go body: %v", target, err)
		}
		s := f.Sig
		if len(s.TParams) != 0 || len(s.Results) != 0 || len(body.Results) != 0 || len(s.Params) != len(body.Params)+1 || !isPreludeType(s.Params[0].Type, binding.provider) {
			return nil
		}
		for i, param := range body.Params {
			// A provider bridge currently admits duration operands. A new
			// Go operand shape fails closed until the host path owns it.
			if param.Type != "time.Duration" || !isBasic(s.Params[i+1].Type, check.Duration) {
				return nil
			}
		}
		return func(fc *funcCtx, params []string) string {
			return "\t" + fc.runtimeCall(name, body, "", "", params[1:], fc.envExpr(fc.site), nil, nil, fc.site) + "\n"
		}
	}
	return nil
}
