package lower

import (
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Named points (std testing's At and Point, runtime/v1 hook.go; RT-HOOK)
// exist only in the test build: in the run build a call of either lowers
// to nothing, so a program's binary holds no hook code and no lookup on
// its paths (TestHooksStayOutOfRunBuilds). An argument that is not a
// literal is still evaluated, as the call would have evaluated it, and
// its value dropped.

// hookTargets are the Go bodies of a named point: std testing's At and
// Point bind them, and so may any package's extern.
var hookTargets = sync.OnceValue(func() map[string]bool {
	return map[string]bool{
		runtimeModule + "/runtime/v1.HookAt":    true,
		runtimeModule + "/runtime/v1.HookPoint": true,
	}
})

// hookFunc reports whether f is an extern bound to a named point's Go
// body. It reads the binding, never the package that declares it, so a
// user's extern of HookAt or HookPoint drops from a run build as std
// testing's does (STD HAS NO PRIVILEGES).
func hookFunc(f *check.Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern != nil && f.Decl.Extern.Target != nil &&
		hookTargets()[strings.Trim(f.Decl.Extern.Target.Value, "\"`")]
}

// dropHook writes, in the run build, what a call statement of At or Point
// leaves: the evaluation of each argument that is not a literal. It
// reports whether call was one; the test build lowers the call.
func (fc *funcCtx) dropHook(call *syntax.CallExpr) bool {
	if fc.l.tests != nil {
		return false
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !hookFunc(c.Func) {
		return false
	}
	for _, a := range call.Args {
		switch unparen(a).(type) {
		case *syntax.BasicLit, *syntax.FuncLit:
		default:
			fc.discard(a)
		}
	}
	return true
}
