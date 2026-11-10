package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// captureLowering selects how an escaping frame captures a let mut of an
// enclosing frame. Empty in every build: by value, a Go variable bound to
// the local's value in the statement that makes the literal, as the
// checker's Info.ValueCaptures lists them. No write to such a local
// follows the capture (types.md, Function values 3; owner q60), so the
// copy holds what the local holds whenever the literal runs, and the
// local, which Go would move to the heap when a closure that may outlive
// the frame captures a variable that is assigned, stays on the stack.
// "reference" is the reference lowering, Go's capture by reference,
// which TestCaptureRuleModel proves equal to it on the admitted programs;
// a build selects it with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.captureLowering=reference.
var captureLowering string

// snapshot lowers the escaping frame n with build, which returns its Go
// function literal. Each let mut the frame captures by value is bound to
// a new Go variable in the statement before, in the frame that makes the
// literal (a task's block runs its literal in the new task, so a copy
// inside it would race with the creator), and the literal is lowered with
// each capture's Go name bound to its copy. The copy shares a slice's or
// Map's storage: no write follows, through the local or the literal.
func (fc *funcCtx) snapshot(n syntax.Node, build func() string) string {
	vars := fc.l.info.ValueCaptures[n]
	if captureLowering == "reference" || len(vars) == 0 {
		return build()
	}
	saved := make([]string, len(vars))
	for i, v := range vars {
		saved[i] = fc.locals[v]
		name := fc.scope.alloc(v.Name)
		fc.line(name + " := " + saved[i])
		fc.locals[v] = name
	}
	closure := build()
	for i, v := range vars {
		fc.locals[v] = saved[i]
	}
	return closure
}

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
	// check/frame.go, ContextLit), so that frame stays open.
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
