package lower

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Std calls (check-v1 stage D3b; docs/spec/stdlib.md, Hidden arguments). A
// std extern function and an operation of a type a std extern returns call
// their runtime/v1 Go body directly, with the shape v1.BodyOf reads from the
// embedded source: the hidden arguments first (the frame scope, the call's
// site, the caller's task), then the Ergo arguments, each function value
// adapted to its Go parameter's shape. A std type is the runtime's Go type:
// the prelude's Task[T, E] is *Task[T], queue.Queue[T] is *Queue[T] and
// queue.Closed is Closed, so a lowered catch matches the runtime's failure
// and a returned Task can Move.

// runtimeType is the runtime/v1 Go type, without type arguments, that the
// std type n lowers to, or "" when n is lowered as a declaration of the
// program (the program's own types, the prelude's other types, and a std
// record the runtime has no struct for, such as timeout.Expired). A std
// record the runtime declares is the runtime's struct of the same name; a
// std operation type is a pointer to the runtime's type of the same name,
// whose methods are its operations.
func (l *lowerer) runtimeType(n *check.Named, site syntax.Node) string {
	obj := n.Obj
	if obj.Pkg == nil || obj.Pkg == l.pkg || obj.Decl == nil {
		return ""
	}
	if obj.Pkg.Path == "prelude" {
		if obj.Name == "Task" {
			return "*" + l.rt() + ".Task"
		}
		return ""
	}
	switch spec := obj.Decl.Spec.(type) {
	case *syntax.StructType:
		if _, err := v1.StructFields(obj.Name); err != nil {
			return ""
		}
		return l.rt() + "." + obj.Name
	case *syntax.InterfaceType:
		if len(spec.Union) == 0 {
			return "*" + l.rt() + "." + obj.Name
		}
	}
	refuse(site, "the std type %s.%s is lowered after check-v1 stage E", obj.Pkg.Path, obj.Name)
	return ""
}

// isRuntimeType reports a std type the runtime's Go type stands for.
func (l *lowerer) isRuntimeType(t check.Type, site syntax.Node) bool {
	n, ok := t.(*check.Named)
	return ok && l.runtimeType(n, site) != ""
}

// stdBody is the runtime/v1 body of the std extern function f, or false
// when f's Go body is not a runtime/v1 function or f is an intrinsic, whose
// body the lowering writes (lowerer.intrinsic).
func (l *lowerer) stdBody(fd *funcDecl) (v1.Body, string, bool) {
	if fd.extern == "" || !fd.library || l.intrinsic(fd.fn) != nil {
		return v1.Body{}, "", false
	}
	i := strings.LastIndex(fd.extern, ".")
	if i < 0 || fd.extern[:i] != runtimeImport {
		return v1.Body{}, "", false
	}
	name := fd.extern[i+1:]
	body, err := v1.BodyOf(name)
	if err != nil {
		return v1.Body{}, "", false
	}
	return body, name, true
}

// needsFrame reports whether a call needs the frame scope of the function
// that makes it: its result holds a Task (Info's TaskResult, which the
// checker owns), or its std body takes the frame (scope.OnExit).
func (l *lowerer) needsFrame(x *syntax.CallExpr) bool {
	c := l.info.Calls[x]
	if c == nil {
		return false
	}
	if c.TaskResult {
		return true
	}
	if c.Kind != check.CallFunc {
		return false
	}
	body, _, ok := l.stdBody(l.funcDecl(c.Func))
	return ok && hasHidden(body.Hidden, v1.Frame)
}

func hasHidden(list []v1.Hidden, h v1.Hidden) bool {
	for _, x := range list {
		if x == h {
			return true
		}
	}
	return false
}

// bodyNeedsFrame reports whether a function body makes a call that needs
// its frame scope (Rule 2). A function literal and the trailing block of a
// call other than with are frames of their own, so their calls do not
// count; a with block runs in the frame (rows.md, With 8).
func (l *lowerer) bodyNeedsFrame(list []syntax.Stmt) bool {
	found := false
	var visit func(n syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CallExpr:
			if l.needsFrame(n) {
				found = true
				return false
			}
			if c := l.info.Calls[n]; n.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
				syntax.Inspect(n.Fun, visit)
				for _, a := range n.Args {
					syntax.Inspect(a, visit)
				}
				return false
			}
		}
		return true
	}
	for _, s := range list {
		syntax.Inspect(s, visit)
	}
	return found
}

// openFrame writes the frame scope of the function being written, its
// first statement and first defer (runtime/v1 Scope): End gets the frame's
// named error result, or nil in a frame with no error result.
func (fc *funcCtx) openFrame(errName string) {
	fs := fc.scope.alloc("fs")
	fc.frame.scope = fs
	fc.line(fs + " := " + fc.l.rt() + ".NewScope(" + fc.envExpr(fc.site) + "." + envTask + ")")
	if errName == "" {
		fc.line("defer " + fs + ".End(nil)")
	} else {
		fc.line("defer " + fs + ".End(&" + errName + ")")
	}
}

// frameScope is the address of the current frame scope, the hidden frame
// or destination argument of a call at site.
func (fc *funcCtx) frameScope(site syntax.Node) string {
	if fc.frame == nil || fc.frame.scope == "" {
		refuse(site, "a call that needs a frame scope (task.Go, scope.OnExit or a function whose result holds a Task) outside a function body is lowered after check-v1 stage E")
	}
	return "&" + fc.frame.scope
}

// namedResults is a Go result list with names, for a frame whose deferred
// End reads its error result: the value is blank, the error errName.
func (tc *typeCtx) namedResults(result check.Type, errName string) string {
	if result != nil {
		return " (_ " + tc.goType(result) + ", " + errName + " error)"
	}
	return " (" + errName + " error)"
}

// isTask reports the prelude's Task type itself, the one result shape that
// takes a destination (Rule 2); a result that only holds a Task, or a Task
// as a type argument, is check-v1 stage E's.
func isTask(t check.Type) bool { return isPreludeType(t, "Task") }

// taskDest reports whether a function of signature s takes the caller's
// frame scope as its destination, after its environment.
func taskDest(s *check.Signature) bool {
	return len(s.Results) == 1 && isTask(s.Results[0])
}

// moved is the returned value val, moved into the destination when the
// frame returns a Task (Rule 2: one step, `return v.Move(dest)`).
func (fc *funcCtx) moved(val string) string {
	if fc.frame.dest == "" {
		return val
	}
	return val + ".Move(" + fc.frame.dest + ")"
}

// returnFallible returns a fallible operand from the frame: its value and
// error as they are, or, in a frame that returns a Task, the failure first
// and then the value moved.
func (fc *funcCtx) returnFallible(x syntax.Expr, o operand) {
	val := fc.convertAt(x, o.val)
	if fc.frame.dest == "" {
		fc.line("return " + val + ", " + o.err)
		return
	}
	fc.line("if " + o.err + " != nil {")
	fc.depth++
	fc.fail(o.err)
	fc.depth--
	fc.line("}")
	fc.line("return " + fc.moved(val) + ", nil")
}

// stdCall is a call of a std extern function with a runtime/v1 body.
func (fc *funcCtx) stdCall(x *syntax.CallExpr, c *check.Call, args []syntax.Expr, body v1.Body, name string) (string, bool, bool) {
	f := c.Func
	if len(body.Params) != len(args) {
		ice(x, "the std function %s has %d Go parameters for %d arguments", f.Name, len(body.Params), len(args))
	}
	vals := fc.callArgs(args, x)
	for i, p := range body.Params {
		if p.Func != nil {
			vals[i] = fc.adaptFunc(vals[i], p.Func, fc.argSig(args[i], x), x)
		}
	}
	head := fc.l.rt() + "." + name + fc.stdTypeArgs(body, f.Sig, c.TypeArgs, x)
	text := fc.stdResult(body, fc.typeOf(x), vals, x, func(env string) string {
		return head + "(" + strings.Join(append(fc.hiddenArgs(body.Hidden, x, c, env), vals...), ", ") + ")"
	})
	return text, len(f.Sig.Results) > 0, goFallible(body)
}

// stdOpCall is a call of an operation of a std type, the method of that
// name on the runtime's Go type.
func (fc *funcCtx) stdOpCall(x *syntax.CallExpr, c *check.Call, n *check.Named, args []syntax.Expr) (string, bool, bool) {
	body, err := v1.BodyOf(n.Obj.Name + "." + c.Op.Name)
	if err != nil {
		ice(x, "the std operation %s.%s has no runtime/v1 method: %v", n.Obj.Name, c.Op.Name, err)
	}
	if len(body.Params) != len(args) {
		ice(x, "the std operation %s.%s has %d Go parameters for %d arguments", n.Obj.Name, c.Op.Name, len(body.Params), len(args))
	}
	vals := fc.callArgs(append([]syntax.Expr{c.Recv}, args...), x)
	for i, p := range body.Params {
		if p.Func != nil {
			vals[i+1] = fc.adaptFunc(vals[i+1], p.Func, fc.argSig(args[i], x), x)
		}
	}
	text := fc.stdResult(body, fc.typeOf(x), vals, x, func(env string) string {
		return "(" + vals[0] + ")." + memberName(c.Op.Name) + "(" + strings.Join(append(fc.hiddenArgs(body.Hidden, x, c, env), vals[1:]...), ", ") + ")"
	})
	return text, len(c.Op.Sig.Results) > 0, goFallible(body)
}

// stdResult is the Go text of a std body's call, written by call from the
// environment whose task the call takes, adapted to the Ergo type result.
// A call whose result is a value is made here, on the current environment.
// Go's iter.Seq[T] (queue.Queue's Values) is a deferred call: the result
// is the uniform function shape of the prelude's Seq[T] (typeCtx.funcType),
// and the body is called inside it with the environment the Seq is
// iterated on, so the wait takes the iterating task, never the task that
// made the Seq (Rule 3; stage D3b round 1 review M1: a hidden task is bound
// where the blocking happens). The receiver and arguments, vals, are
// evaluated once, here, in source order: each is bound to a temporary the
// Seq captures.
func (fc *funcCtx) stdResult(body v1.Body, result check.Type, vals []string, site syntax.Node, call func(env string) string) string {
	if len(body.Results) != 1 || !strings.HasPrefix(body.Results[0], "iter.Seq[") {
		return call(fc.envExpr(site))
	}
	n, ok := result.(*check.Named)
	if !ok || !isPreludeType(result, "Seq") || len(n.TArgs) != 1 {
		ice(site, "a std body returns %s for the Ergo type %s", body.Results[0], result)
	}
	if hasHidden(body.Hidden, v1.Frame) {
		ice(site, "a std body that returns a Seq takes the frame scope")
	}
	for i := range vals {
		vals[i] = fc.spill(vals[i])
	}
	elem := fc.goType(n.TArgs[0])
	e, yield, v, ok2, err := fc.scope.alloc("e"), fc.scope.alloc("yield"), fc.scope.alloc("v"), fc.scope.alloc("ok"), fc.scope.alloc("err")
	ind := strings.Repeat("\t", fc.depth)
	return "func(" + e + " *" + fc.l.env() + ", " + yield + " func(*" + fc.l.env() + ", " + elem + ") (bool, error)) error {\n" +
		ind + "\tfor " + v + " := range " + call(e) + " {\n" +
		ind + "\t\t" + ok2 + ", " + err + " := " + yield + "(" + e + ", " + v + ")\n" +
		ind + "\t\tif " + err + " != nil {\n" +
		ind + "\t\t\treturn " + err + "\n" +
		ind + "\t\t}\n" +
		ind + "\t\tif !" + ok2 + " {\n" +
		ind + "\t\t\treturn nil\n" +
		ind + "\t\t}\n" +
		ind + "\t}\n" +
		ind + "\treturn nil\n" +
		ind + "}"
}

// goFallible reports whether a std body's Go form returns an error: its
// last result is error. It is the body's shape, not the site's row: a
// declared row such as Task's E may be a row parameter, and a call whose
// row is empty at its site drops the Go error where every call does
// (funcCtx.call, from Info's Fails).
func goFallible(body v1.Body) bool {
	return len(body.Results) > 0 && body.Results[len(body.Results)-1] == "error"
}

// hiddenArgs are the hidden arguments of a std body at the call x, in the
// body's order; the caller's task is env's.
func (fc *funcCtx) hiddenArgs(list []v1.Hidden, x *syntax.CallExpr, c *check.Call, env string) []string {
	var out []string
	for _, h := range list {
		switch h {
		case v1.Frame:
			out = append(out, fc.frameScope(x))
		case v1.CallSite:
			line, column := fc.l.file.Position(x.Pos())
			out = append(out, fc.l.rt()+".Site("+fmt.Sprintf("%q", fmt.Sprintf("%s at %s:%d:%d", fc.callee(x, c), fc.l.src, line, column))+")")
		case v1.CallerTask:
			out = append(out, env+"."+envTask)
		default:
			ice(x, "the hidden argument %s of a std body", h)
		}
	}
	return out
}

// stdTypeArgs writes a std body's Go type arguments, each the call's type
// argument for the Ergo type parameter of the same name.
func (fc *funcCtx) stdTypeArgs(body v1.Body, sig *check.Signature, args []check.Type, site syntax.Node) string {
	if len(body.TypeParams) == 0 {
		return ""
	}
	out := make([]string, len(body.TypeParams))
	for i, gp := range body.TypeParams {
		j := -1
		for k, tp := range sig.TParams {
			if tp.Name == gp.Name {
				j = k
			}
		}
		if j < 0 || j >= len(args) {
			ice(site, "the Go type parameter %s of a std body has no Ergo type argument", gp.Name)
		}
		out[i] = fc.goType(args[j])
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// argSig is the Ergo signature of a function argument: a trailing block's
// (a nil arg) Literals entry, else the argument's type.
func (fc *funcCtx) argSig(arg syntax.Expr, x *syntax.CallExpr) *check.Signature {
	if arg == nil {
		if s := fc.l.info.Literals[x.Trailing]; s != nil {
			return s
		}
		ice(x, "a trailing block with no Literals entry")
	}
	t := fc.typeOf(arg)
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	s, ok := t.(*check.Signature)
	if !ok {
		ice(arg, "a function argument of a std body that is not a function")
	}
	return s
}

// adaptFunc adapts fv, an Ergo function value of the uniform shape
// (typeCtx.funcType), to shape, a function parameter of a std body: its
// hidden task runs fv on a copy of the environment that holds that task
// (Rule 1: a task.Go body runs on the child's task); a Go function with no
// results drops fv's error, which a checked block with no error row never
// returns.
func (fc *funcCtx) adaptFunc(fv string, shape *v1.Body, sig *check.Signature, site syntax.Node) string {
	if len(shape.Params) != 0 || len(sig.Params) != 0 || len(sig.Results) > 1 {
		ice(site, "a std function parameter with parameters of its own")
	}
	env := fc.envExpr(site)
	ind := strings.Repeat("\t", fc.depth+1)
	var params []string
	var pre string
	for _, h := range shape.Hidden {
		if h != v1.CallerTask {
			ice(site, "the hidden argument %s of a std function parameter", h)
		}
		t := fc.scope.alloc("t")
		e := fc.scope.alloc("e")
		params = append(params, t+" *"+fc.l.taskPkg()+".Task")
		pre = ind + e + " := *" + env + "\n" + ind + e + "." + envTask + " = " + t + "\n"
		env = "&" + e
	}
	call := fv + "(" + env + ")"
	var results, body string
	switch len(shape.Results) {
	case 0:
		if len(sig.Results) == 1 {
			body = ind + "_, _ = " + call + "\n"
		} else {
			body = ind + "_ = " + call + "\n"
		}
	case 1:
		if len(sig.Results) != 0 {
			ice(site, "a std function parameter returns error alone and the block a value")
		}
		results, body = " error", ind+"return "+call+"\n"
	case 2:
		if len(sig.Results) != 1 {
			refuse(site, "a block with no value where the std function needs one is lowered after check-v1 stage E")
		}
		results, body = " ("+fc.goType(sig.Results[0])+", error)", ind+"return "+call+"\n"
	default:
		ice(site, "a std function parameter with %d results", len(shape.Results))
	}
	return "func(" + strings.Join(params, ", ") + ")" + results + " {\n" + pre + body + strings.Repeat("\t", fc.depth) + "}"
}
