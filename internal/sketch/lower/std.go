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
	fc.bindDeferred(body, vals)
	text := fc.stdResult(body, fc.typeOf(x), fc.envExpr(x), x, func(env string) string {
		return fc.runtimeCall(name, body, fc.stdTypeArgs(body, f.Sig, c.TypeArgs, x), "", vals, env, x, c, x)
	})
	return text, len(f.Sig.Results) > 0, goFallible(body)
}

// opBody is the runtime/v1 method of the operation name of the std type n,
// which takes params Ergo arguments.
func opBody(n *check.Named, name string, params int, site syntax.Node) v1.Body {
	body, err := v1.BodyOf(n.Obj.Name + "." + name)
	if err != nil {
		ice(site, "the std operation %s.%s has no runtime/v1 method: %v", n.Obj.Name, name, err)
	}
	if len(body.Params) != params {
		ice(site, "the std operation %s.%s has %d Go parameters for %d arguments", n.Obj.Name, name, len(body.Params), params)
	}
	return body
}

// opCallExpr is the call x of an operation of an operation-type value
// (CallOp): the receiver and the arguments are evaluated here, in order,
// and the call is made on the current environment (opCall).
func (fc *funcCtx) opCallExpr(x *syntax.CallExpr, c *check.Call, args []syntax.Expr) (string, bool, bool) {
	t := fc.typeOf(c.Recv)
	vals := fc.callArgs(append([]syntax.Expr{c.Recv}, args...), x)
	if n, ok := t.(*check.Named); ok && fc.l.runtimeType(n, x) != "" {
		body := opBody(n, c.Op.Name, len(args), x)
		for i, p := range body.Params {
			if p.Func != nil {
				vals[i+1] = fc.adaptFunc(vals[i+1], p.Func, fc.argSig(args[i], x), x)
			}
		}
		fc.bindDeferred(body, vals)
	}
	return fc.opCall(t, c.Op, fc.typeOf(x), vals, fc.envExpr(x), x, c, x)
}

// methodValue lowers recv.M, an operation of an operation type used as a
// value (stage D3b round 1 review M3): a function value of the uniform
// shape (typeCtx.funcType) that makes the call opCall writes. The receiver
// is evaluated once, here; the call takes the task of the environment the
// value is called with, never the task of the function that made it (the
// common rule: a hidden task is bound where the blocking happens).
func (fc *funcCtx) methodValue(x *syntax.SelectorExpr, op *check.Method) string {
	sig, ok := fc.typeOf(x).(*check.Signature)
	if !ok {
		ice(x, "the method value %s has no function type", x.Sel.Name)
	}
	if sig.ResultIsTask() {
		refuse(x, "a method value of an operation that returns a Task is lowered after check-v1 stage E")
	}
	if len(sig.Results) > 1 {
		refuse(x, "a method value with several results is a tuple, lowered after check-v1 stage B (196t)")
	}
	recv := fc.spill(fc.value(x.X))
	return fc.envClosure(callerEnv, "e", x, func(e string) (params []string, results, body string) {
		vals := []string{recv}
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			vals = append(vals, name)
		}
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		fc.depth++
		call, hasResult, fallible := fc.opCall(fc.typeOf(x.X), op, result, vals, e, nil, nil, x)
		ind := strings.Repeat("\t", fc.depth)
		fc.depth--
		return params, fc.resultList(result, true), uniformBody(ind, call, hasResult, result != nil, fallible)
	})
}

// uniformBody is the body of a function value of the uniform shape
// (typeCtx.funcType) that makes the Go call call: goVal and goErr are the
// call's Go form (a value, an error), and value whether the function value
// returns a value. A Go value the function value does not return is
// struct{}, a no-value type argument's (types.md, Inference), discarded.
func uniformBody(ind, call string, goVal, value, goErr bool) string {
	switch {
	case goVal && !value && goErr:
		return ind + "_, err := " + call + "\n" + ind + "return err\n"
	case goVal && !value:
		return ind + "_ = " + call + "\n" + ind + "return nil\n"
	case goErr:
		return ind + "return " + call + "\n"
	case goVal:
		return ind + "return " + call + ", nil\n"
	}
	return ind + call + "\n" + ind + "return nil\n"
}

// opCall is the Go call of the operation op on a value of the operation
// type t, the one place a call (opCallExpr) and a method value
// (methodValue) write it: vals are the receiver's and the arguments' Go
// text, evaluated, and env is the environment the call is made on. An
// operation of a std type is the method of that name on the runtime's Go
// type, with its body's hidden arguments; an operation of the program's own
// operation type is the method of the Go interface it emits, which takes
// the task first (rows.md, Rows 7). result is the call's Ergo result type;
// x and c are the call, nil for a method value. It returns the text,
// whether the call has a value, and whether its Go form returns an error.
func (fc *funcCtx) opCall(t check.Type, op *check.Method, result check.Type, vals []string, env string, x *syntax.CallExpr, c *check.Call, site syntax.Node) (string, bool, bool) {
	if n, ok := t.(*check.Named); ok && fc.l.runtimeType(n, site) != "" {
		body := opBody(n, op.Name, len(vals)-1, site)
		if x == nil {
			for _, p := range body.Params {
				if p.Func != nil {
					ice(site, "a method value of the std operation %s.%s, which takes a function", n.Obj.Name, op.Name)
				}
			}
		}
		text := fc.stdResult(body, result, env, site, func(env string) string {
			return fc.runtimeCall(n.Obj.Name+"."+op.Name, body, "", vals[0], vals[1:], env, x, c, site)
		})
		// The Go form decides: Wait on a Task[T] whose T is no value returns
		// struct{}, which the caller discards (goValue).
		return text, goValue(body), goFallible(body)
	}
	// The checker refuses every other way to reach a std operation: a type
	// defined over a std operation type (S108 at the definition) and a
	// pointer to one (S108 at the call, stage D3b Rule 5).
	if n := fc.l.foreignOwner(t); n != nil {
		ice(site, "the std operation %s.%s reached through %s, which the checker refuses", n.Obj.Name, op.Name, t)
	}
	rest := append([]string{env + "." + envTask}, vals[1:]...)
	return "(" + vals[0] + ")." + memberName(op.Name) + "(" + strings.Join(rest, ", ") + ")", len(op.Sig.Results) > 0, len(rowElems(op.Sig.Errors)) > 0
}

// defersCall reports a std body whose result defers its call: Go's
// iter.Seq[T] (queue.Queue's Values), which stdResult adapts.
func defersCall(body v1.Body) bool {
	return len(body.Results) == 1 && strings.HasPrefix(body.Results[0], "iter.Seq[")
}

// bindDeferred binds each of a call's evaluated operands, vals, to a
// temporary when body defers its call, so the receiver and the arguments
// are evaluated once, where the deferred value is made, in source order.
func (fc *funcCtx) bindDeferred(body v1.Body, vals []string) {
	if defersCall(body) {
		for i := range vals {
			vals[i] = fc.spill(vals[i])
		}
	}
}

// stdResult is the Go text of a std body's call, written by call from the
// environment whose task the call takes, adapted to the Ergo type result.
// A call whose result is a value is made here, on env. Go's iter.Seq[T]
// (queue.Queue's Values) is a deferred call: the result is the uniform
// function shape of the prelude's Seq[T] (typeCtx.funcType), and the body
// is called inside it with the environment the Seq is iterated on, so the
// wait takes the iterating task, never the task that made the Seq (Rule 3;
// stage D3b round 1 review M1: a hidden task is bound where the blocking
// happens). Its operands are evaluated already (bindDeferred).
func (fc *funcCtx) stdResult(body v1.Body, result check.Type, env string, site syntax.Node, call func(env string) string) string {
	if !defersCall(body) {
		return call(env)
	}
	n, ok := result.(*check.Named)
	if !ok || !isPreludeType(result, "Seq") || len(n.TArgs) != 1 {
		ice(site, "a std body returns %s for the Ergo type %s", body.Results[0], result)
	}
	if hasHidden(body.Hidden, v1.Frame) {
		ice(site, "a std body that returns a Seq takes the frame scope")
	}
	return fc.envClosure(callerEnv, "e", site, func(e string) (params []string, results, text string) {
		yield, v, ok2, err := fc.scope.alloc("yield"), fc.scope.alloc("v"), fc.scope.alloc("ok"), fc.scope.alloc("err")
		ind := strings.Repeat("\t", fc.depth)
		// The yield parameter has the Seq's checked function type. It is a
		// type, not another emitted closure or another env-binding owner.
		yieldType := fc.goType(n.Underlying().(*check.Signature).Params[0].Type)
		return []string{yield + " " + yieldType}, " error",
			ind + "\tfor " + v + " := range " + call(e) + " {\n" +
				ind + "\t\t" + ok2 + ", " + err + " := " + yield + "(" + e + ", " + v + ")\n" +
				ind + "\t\tif " + err + " != nil {\n" +
				ind + "\t\t\treturn " + err + "\n" +
				ind + "\t\t}\n" +
				ind + "\t\tif !" + ok2 + " {\n" +
				ind + "\t\t\treturn nil\n" +
				ind + "\t\t}\n" +
				ind + "\t}\n" +
				ind + "\treturn nil\n"
	})
}

// goFallible reports whether a std body's Go form returns an error: its
// last result is error. It is the body's shape, not the site's row: a
// declared row such as Task's E may be a row parameter, and a call whose
// row is empty at its site drops the Go error where every call does
// (funcCtx.call, from Info's Fails).
func goFallible(body v1.Body) bool {
	return len(body.Results) > 0 && body.Results[len(body.Results)-1] == "error"
}

// goValue reports whether a std body's Go form returns a value before its
// error. A value whose Ergo type is no value (a Task[T] whose T a block
// with no value bound) is struct{}, and the call site discards it.
func goValue(body v1.Body) bool {
	n := len(body.Results)
	if goFallible(body) {
		n--
	}
	return n > 0
}

// hiddenArgs are the hidden arguments of a std body at the call x, in the
// body's order; the caller's task is env's. A method value (x nil) takes
// only the caller's task: the frame and the call's site belong to a call.
func (fc *funcCtx) hiddenArgs(list []v1.Hidden, x *syntax.CallExpr, c *check.Call, env string, site syntax.Node) []string {
	var out []string
	for _, h := range list {
		if x == nil && h != v1.CallerTask {
			ice(site, "a method value of a std operation that takes the hidden argument %s", h)
		}
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

// bindFunc binds a function value's Go text to a temporary unless it is a
// Go function literal, which reads no variable when it is evaluated: an
// adapter that calls the value later calls the value as it was here.
func (fc *funcCtx) bindFunc(fv string) string {
	if strings.HasPrefix(fv, "func(") {
		return fv
	}
	return fc.spill(fv)
}

// adaptFunc adapts fv, an Ergo function value of the uniform shape
// (typeCtx.funcType), to shape, a function parameter of a std body: its
// hidden task runs fv on a copy of the environment that holds that task
// (Rule 1: a task.Go body runs on the child's task); a Go function with no
// results drops fv's error, which a checked block with no error row never
// returns. fv is bound once, here, where the call is made: the adapter runs
// later (a task.Go body runs on its own task), and a later assignment to
// the variable fv names must not change the function it runs.
func (fc *funcCtx) adaptFunc(fv string, shape *v1.Body, sig *check.Signature, site syntax.Node) string {
	if len(shape.Params) != 0 || len(sig.Params) != 0 || len(sig.Results) > 1 {
		ice(site, "a std function parameter with parameters of its own")
	}
	fv = fc.bindFunc(fv)
	binding := capturedEnv
	for _, h := range shape.Hidden {
		if h != v1.CallerTask || binding != capturedEnv {
			ice(site, "the hidden argument %s of a std function parameter", h)
		}
		binding = childEnv
	}
	return fc.envClosure(binding, "e", site, func(env string) (params []string, results, body string) {
		ind := strings.Repeat("\t", fc.depth+1)
		call := fv + "(" + env + ")"
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
			switch len(sig.Results) {
			case 0:
				// A block with no value binds the body's T to no value, which
				// is struct{} (types.md, Inference; stage D3b round 1 review M2).
				results, body = " (struct{}, error)", ind+"return struct{}{}, "+call+"\n"
			case 1:
				results, body = " ("+fc.goType(sig.Results[0])+", error)", ind+"return "+call+"\n"
			default:
				ice(site, "a std function parameter's block with %d results", len(sig.Results))
			}
		default:
			ice(site, "a std function parameter with %d results", len(shape.Results))
		}
		return nil, results, body
	})
}
