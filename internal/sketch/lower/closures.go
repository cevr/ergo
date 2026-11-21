package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
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
	if capture := fc.l.info.TaskCaptures[n]; capture != nil {
		// Resource captures move by R12 (#197 L8 B3t); taskSnapshot binds
		// their one moved value, so the value rule does not copy them.
		moved := map[*check.Var]bool{}
		for _, v := range capture.Vars {
			moved[v] = true
		}
		for _, v := range capture.Borrowed {
			moved[v] = true // a lend retains resource identity, never a value clone
		}
		var plain []*check.Var
		for _, v := range vars {
			if !moved[v] {
				plain = append(plain, v)
			}
		}
		vars = plain
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

// envLowering, when "copy", writes a host callback's environment as a
// fresh copy at each call, as before ENV-COPY: the reference lowering. A
// whole build selects it with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.envLowering=copy.
var envLowering string

// envFrom is the environment's field that names the environment a copy
// was made from (envMemoText), written only when a callback uses it.
const envFrom = "from"

// envMemoText is the start of a host callback (childEnv) that runs an
// Ergo body on the host's task: it binds e to an environment whose task
// is task and whose capabilities are outer's. That is outer itself when
// the host calls back on outer's task (actor.ActorKeep, a release on the
// releasing task); else the copy kept for the task (rt.Task.EnvMemo) when
// it is that task's and was made from outer (an actor's step on the
// actor's task, from the second signal on); else a new copy, which is then
// kept. The runtime's slot may hold another task's copy, so both are
// checked. Without
// it every call copies the environment, and the copy, which the callee
// takes by pointer, is on the heap. Sharing a copy is sound because no
// lowering writes an environment once a body can see it: every write is
// to a fresh copy (here, contextvalues.go, ir_with.go, the operations
// conversions in calls.go). A copy holds outer through its from field, so
// a new environment never has outer's address while the copy is held.
func envMemoText(ind, e, outer, task, envType, c string) string {
	return ind + e + " := " + outer + "\n" +
		ind + "if " + taskOf(e) + " != " + task + " {\n" +
		ind + "\t" + e + ", _ = " + task + ".EnvMemo().(*" + envType + ")\n" +
		ind + "\tif " + e + " == nil || " + taskOf(e) + " != " + task + " || " + e + "." + envFrom + " != " + outer + " {\n" +
		ind + "\t\t" + c + " := *" + outer + "\n" +
		ind + "\t\t" + taskOf(c) + ", " + c + "." + envFrom + " = " + task + ", " + outer + "\n" +
		ind + "\t\t" + e + " = &" + c + "\n" +
		ind + "\t\t" + task + ".SetEnvMemo(" + e + ")\n" +
		ind + "\t}\n" +
		ind + "}\n"
}

type envBinding uint8

const (
	callerEnv   envBinding = iota // the uniform function parameter
	childEnv                      // a host callback's task, in a copy of the current env
	capturedEnv                   // a host callback with no hidden task
	plainEnv                      // a known pure literal: the caller's environment, if any (known.go)
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
		outer := fc.envExpr(site)
		if envLowering == "copy" {
			pre = ind + copy + " := *" + outer + "\n" + ind + taskOf(copy) + " = " + task + "\n"
			env = "&" + copy
		} else {
			// ENV-COPY: on the maker's task, or on a task that already
			// holds a copy of this environment, the callback makes none
			// (envMemoText).
			fc.l.envFrom = true
			pre = envMemoText(ind, copy, outer, task, fc.l.env(), fc.scope.alloc("c"))
			env = copy
		}
		bound = env
	case capturedEnv:
		env = fc.envExpr(site)
		bound = env
	case plainEnv:
		// A known pure literal is its own frame, called only while the
		// call that takes it runs, on the caller's task.
		env, bound = fc.env, fc.env
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
