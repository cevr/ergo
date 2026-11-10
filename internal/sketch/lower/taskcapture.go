package lower

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// taskSnapshot binds a moving body's captures by value in the sender.
// Ownership handles are lexical to a frame, even when two frames use the
// same source Var, so lowering the child restores the sender's handles.
func (fc *funcCtx) taskSnapshot(n syntax.Node, build func() string) string {
	c := fc.l.info.TaskCaptures[n]
	if c == nil {
		return build()
	}
	names, owners := map[*check.Var]string{}, map[*check.Var]string{}
	for _, v := range c.Vars {
		names[v], owners[v] = fc.locals[v], fc.l.owned[v]
		name := fc.temp(v.Name)
		fc.line(name + " := " + fc.locals[v])
		fc.locals[v] = name
	}
	if c.Repeated {
		if fc.l.captureRoots == nil {
			fc.l.captureRoots = map[syntax.Node]string{}
		}
		fc.l.captureRoots[n] = fc.taskCaptureRoot(n, c)
	}
	text := build()
	for _, v := range c.Vars {
		fc.locals[v], fc.l.owned[v] = names[v], owners[v]
	}
	return text
}

// taskCaptureRoot creates a Set member's root owner. The runtime calls
// it once, before any run of the restartable body, and ends the scope
// after the last run. Its releases bind the child's task environment.
func (fc *funcCtx) taskCaptureRoot(n syntax.Node, c *check.TaskCapture) string {
	inner := fc.innerFuncCtx()
	return fc.envClosure(childEnv, "rootEnv", n, func(env string) ([]string, string, string) {
		// env is &copy; a release that passes the task selects through it
		// (taskOf), so the root names it parenthesized.
		inner.env, inner.site = "("+env+")", n
		inner.depth = fc.depth + 1
		inner.frame = newFrame(nil, true, false)
		fs := fc.temp("root")
		inner.frame.scope = fs
		inner.line(fs + " := " + fc.l.rt() + ".NewOwnerScope(" + taskOf(inner.env) + ")")
		for _, v := range c.Vars {
			inner.ownVar(v, n)
		}
		inner.line("return &" + fs)
		return nil, " *" + fc.l.rt() + ".Scope", inner.out.String()
	})
}

// captureNodes lists only callbacks passed directly to this call. A
// nested start settles its own captures, not the outer call's.
func (fc *funcCtx) captureNodes(x *syntax.CallExpr) []syntax.Node {
	var nodes []syntax.Node
	for _, a := range x.Args {
		if lit, ok := unparen(a).(*syntax.FuncLit); ok && fc.l.info.TaskCaptures[lit] != nil {
			nodes = append(nodes, lit)
		}
	}
	if x.Trailing != nil && fc.l.info.TaskCaptures[x.Trailing] != nil {
		nodes = append(nodes, x.Trailing)
	}
	return nodes
}

func (fc *funcCtx) captureOwners(x *syntax.CallExpr) []string {
	var owners []string
	for _, n := range fc.captureNodes(x) {
		for _, v := range fc.l.info.TaskCaptures[n].Vars {
			if h := fc.l.owned[v]; h != "" {
				owners = append(owners, h)
			}
		}
	}
	return owners
}

func (fc *funcCtx) handCaptures(owners []string, site syntax.Node) {
	for _, h := range owners {
		fc.line(fc.runtimeCall("Hand", fc.ownBinding("Hand", site), "[struct{}]", "", []string{h, "struct{}{}"}, "", nil, nil, site))
	}
}

// captureCall settles ownership in the admission step: a successful Go
// hands captures to the started root; a Closed Set releases them now.
// A panic before admission leaves the sender's entries for its unwind.
func (fc *funcCtx) captureCall(x *syntax.CallExpr, c *check.Call, owners []string, text string, result, fallible bool) operand {
	if fallible {
		if result {
			ice(x, "a moving callback with a fallible result")
		}
		slices.Reverse(owners) // failed admission releases in root LIFO order
		return fc.settleTaken(owners, text, failsHere(c), x)
	}
	v := ""
	if result {
		v = fc.temp("v")
		fc.line(v + " := " + text)
	} else {
		fc.line(text)
	}
	fc.handCaptures(owners, x)
	return operand{val: v}
}

// movingRootArg is the generated root callback of a repeated body.
func (fc *funcCtx) movingRootArg(x *syntax.CallExpr) string {
	var roots []string
	for _, n := range fc.captureNodes(x) {
		if root := fc.l.captureRoots[n]; root != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) > 1 {
		ice(x, "a call with more than one moving root")
	}
	return strings.Join(roots, "")
}
