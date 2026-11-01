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
// environment of this invocation. Uniform functions take it as their first
// parameter; task callbacks replace the task in a copy. The remaining host
// callbacks run in the captured environment. Callers supply only the other
// parameters, results and body, never the environment parameter or task copy.
func (fc *funcCtx) envClosure(binding envBinding, name string, site syntax.Node, build func(env string) (params []string, results, body string)) string {
	var env, param, pre string
	switch binding {
	case callerEnv:
		env = name
		if name != "_" {
			env = fc.scope.alloc(name)
		}
		param = env + " *" + fc.l.env()
	case childEnv:
		task := fc.scope.alloc("t")
		copy := fc.scope.alloc(name)
		param = task + " *" + fc.l.taskPkg() + ".Task"
		ind := strings.Repeat("\t", fc.depth+1)
		pre = ind + copy + " := *" + fc.envExpr(site) + "\n" + ind + copy + "." + envTask + " = " + task + "\n"
		env = "&" + copy
	case capturedEnv:
		env = fc.envExpr(site)
	default:
		ice(site, "an unknown closure environment binding %d", binding)
	}
	params, results, body := build(env)
	if param != "" {
		params = append([]string{param}, params...)
	}
	return "func(" + strings.Join(params, ", ") + ")" + results + " {\n" + pre + body + strings.Repeat("\t", fc.depth) + "}"
}
