package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

type envBinding uint8

const (
	callerEnv   envBinding = iota // the uniform function parameter
	childEnv                      // a host callback's task, in a copy of the current env
	capturedEnv                   // a host callback with no hidden task
)

// envClosure owns closure environment binding. The body builder receives the
// environment of this invocation, also bound in fc while it runs. Uniform
// functions take it as their first parameter; task callbacks replace the task
// in a copy. The remaining host
// callbacks run in the captured environment. Callers supply only the other
// parameters, results and body, never the environment parameter or task copy.
func (fc *funcCtx) envClosure(binding envBinding, name string, site syntax.Node, build func(env string) (params []string, results, body string)) string {
	var env, param, pre string
	bound := ""
	switch binding {
	case callerEnv:
		env = name
		if name != "_" {
			env = fc.scope.alloc(name)
			bound = env
		}
		param = env + " *" + fc.l.env()
	case childEnv:
		task := fc.scope.alloc("t")
		copy := fc.scope.alloc(name)
		param = task + " *" + fc.l.taskPkg() + ".Task"
		ind := strings.Repeat("\t", fc.depth+1)
		pre = ind + copy + " := *" + fc.envExpr(site) + "\n" + ind + taskOf(copy) + " = " + task + "\n"
		env = "&" + copy
		bound = env
	case capturedEnv:
		env = fc.envExpr(site)
		bound = env
	default:
		ice(site, "an unknown closure environment binding %d", binding)
	}
	// While the builder runs, fc names only the closure's own environment:
	// a discarded parameter binds none (so envExpr is a located ICE). A
	// uniform function or a task callback is a frame of its own, so no frame
	// is open until the builder opens one and the maker's frame is not
	// reachable through fc. A captured callback is not a source frame: its
	// lexical control targets still belong to the maker's frame (acquireFlow;
	// check/frame.go, WireLit), so that frame stays open.
	outerEnv, outerFrame := fc.env, fc.frame
	fc.env = bound
	if binding != capturedEnv {
		fc.frame = nil
	}
	defer func() { fc.env, fc.frame = outerEnv, outerFrame }()
	params, results, body := build(env)
	if param != "" {
		params = append([]string{param}, params...)
	}
	return "func(" + strings.Join(params, ", ") + ")" + results + " {\n" + pre + body + strings.Repeat("\t", fc.depth) + "}"
}
