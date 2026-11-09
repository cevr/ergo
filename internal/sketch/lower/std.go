package lower

import (
	"fmt"
	"strconv"
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

// stdRecord is the lowering's lookup in the binding table of std records:
// v1.StdRecord. It is a variable only so that a test can bind fixture
// records through rows whose Go names differ from the records' names
// (TestStdRecordsBindTheRowsGoName); nothing else assigns it.
var stdRecord = v1.StdRecord

// runtimeType is the runtime/v1 Go type, without type arguments, that the
// std type n lowers to, or "" when n is lowered as a declaration of the
// program (the types of the program's own packages, the prelude's other
// types, and a std record with no row in the binding table, such as
// strings.Halves). A std record with a row is the runtime struct the row
// names, keyed by the record's package and name (v1.StdRecord), never by
// its name alone; a sealed std operation type (check.SealedStd) is a
// pointer to the runtime's type of the same name, whose methods are its
// operations, and an open one is a declaration of the program.
func (l *lowerer) runtimeType(n *check.Named, site syntax.Node) string {
	obj := n.Obj
	if check.IsGoError(n) {
		// The predeclared go.Error, a user extern's failure (G4).
		return l.rt() + ".GoError"
	}
	if obj.Pkg == nil || !obj.Pkg.Std() || obj.Decl == nil {
		return "" // a user package's type is a declaration of the program
	}
	if obj.Pkg.Path == "prelude" {
		if obj.Name == "Task" {
			return "*" + l.rt() + ".Task"
		}
		return ""
	}
	switch spec := obj.Decl.Spec.(type) {
	case *syntax.StructType:
		goName, ok := stdRecord(obj.Pkg.Path, obj.Name)
		if !ok {
			return ""
		}
		if _, err := v1.StructFields(goName); err != nil {
			ice(site, "the std record %s.%s binds v1.%s: %v", obj.Pkg.Path, obj.Name, goName, err)
		}
		return l.rt() + "." + goName
	case *syntax.InterfaceType:
		if len(spec.Union) == 0 {
			if !check.SealedStd(n) {
				return "" // an open std protocol, such as os.Process: a program value implements it
			}
			return "*" + l.rt() + "." + obj.Name
		}
		// A closed union of std records, such as flag.Failure (std-align
		// S1d) or time.Layout (S2b): a declaration of the program, as the
		// prelude's Option is. Its variants are std records, each the
		// runtime's struct or a declaration of the program by the rule
		// above.
		return ""
	}
	// Every std type is a record, a union or an operation type
	// (TestStdTypesHaveRuntimeForms).
	ice(site, "the std type %s.%s is neither a record nor an operation type", obj.Pkg.Path, obj.Name)
	return ""
}

// isRuntimeType reports a std type the runtime's Go type stands for.
func (l *lowerer) isRuntimeType(t check.Type, site syntax.Node) bool {
	n, ok := t.(*check.Named)
	return ok && l.runtimeType(n, site) != ""
}

// stdBody is the runtime/v1 body of the std extern function f, or false
// when f's Go body is not a runtime/v1 function or f is an intrinsic, whose
// body the lowering writes (lowerer.intrinsic). A std extern that binds a
// runtime/v1 body BodyOf cannot read (missing, or a pure mark the reviewed
// rules refuse) is an internal compiler error with BodyOf's error: std and
// runtime/v1 ship with the compiler, so no user program can cause it
// (TestStdExternsHaveBodies holds every std extern to a body).
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
		ice(fd.fn.Decl, "the std function %s binds %s, which has no readable runtime/v1 body: %v", fd.fn.Name, fd.extern, err)
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
	if c.Kind != check.CallFunc || c.Func.Pkg.Host() {
		return false // a Go call of import go takes no frame (gocall.go)
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
// its frame scope (Rule 2) or holds an owning with. A function literal and the trailing block of a
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
			// An owning with operand opens its scope on the frame (calls.go,
			// with), and a context value operand its region (context.go).
			for w := l.info.Withs[n]; w != nil; w = w.Next {
				if ownsResources(w) || w.Value != nil {
					found = true
					return false
				}
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
	fc.line(fs + " := " + fc.l.rt() + ".NewScope(" + taskOf(fc.envExpr(fc.site)) + ")")
	if errName == "" {
		fc.line("defer " + fs + ".End(nil)")
	} else {
		fc.line("defer " + fs + ".End(&" + errName + ")")
	}
}

// frameScope is the address of the current frame scope, the hidden frame
// or destination argument of a call at site. Inside an owning with it is
// the with's scope (v1.OpenWith), so the tasks the block starts or
// receives drain before the with's providers close (resources.md, Context
// ownership); v1.OnExit routes an exit action to the function's frame.
func (fc *funcCtx) frameScope(site syntax.Node) string {
	if fc.frame == nil || fc.frame.scope == "" {
		ice(site, "a call that needs a frame scope (task.Go, scope.OnExit or a call whose result holds a Task) outside a function body; the checker refuses one (S108, check/frames.go)")
	}
	if fc.frame.with != "" {
		return fc.frame.with
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
		fc.sourceReturn(val, o.err)
		return
	}
	fc.line("if " + o.err + " != nil {")
	fc.depth++
	fc.fail(o.err)
	fc.depth--
	fc.line("}")
	fc.sourceReturn(fc.moved(val), "nil")
}

// stdCall is a call of a std extern function with a runtime/v1 body.
func (fc *funcCtx) stdCall(x *syntax.CallExpr, c *check.Call, args []syntax.Expr, body v1.Body, name string) (string, bool, bool) {
	f := c.Func
	if len(body.Params) != len(args) {
		ice(x, "the std function %s has %d Go parameters for %d arguments", f.Name, len(body.Params), len(args))
	}
	if s := fc.refSiteOf(x); s != nil && name == "RefModify" && len(args) == 2 {
		// sync.Modify with a literal that owns the held value (refown.go).
		owned, err := v1.BodyOf("RefModifyOwned")
		if err != nil {
			ice(x, "RefModifyOwned: %v", err)
		}
		return fc.ownedRefCall(x, c, s, "RefModifyOwned", args[0], args[1], fc.stdTypeArgs(owned, f.Sig, c.TypeArgs, x), true)
	}
	vals := fc.callArgs(args, x)
	for i, p := range body.Params {
		if p.Func != nil {
			vals[i] = fc.adaptFunc(vals[i], p.Func, fc.argSig(args[i], x), x)
		}
	}
	fc.bindDeferred(body, vals)
	// A body with no hidden argument whose call is made here reads no
	// environment: a pure function calls it with none bound (pure.go).
	env := ""
	if len(body.Hidden) > 0 || defersCall(body) {
		env = fc.envExpr(x)
	}
	text := fc.stdResult(body, fc.typeOf(x), env, x, func(env string) string {
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
	if s := fc.refSiteOf(x); s != nil && len(args) == 1 {
		// Ref.Update with a literal that owns the held value (refown.go).
		return fc.ownedRefCall(x, c, s, "RefUpdateOwned", c.Recv, args[0], "["+fc.goType(s.elem)+"]", false)
	}
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
	return fc.opCall(t, c.Op, fc.typeOf(x), vals, fc.envExpr(x), fc.opDest(x, c), x, c, x)
}

// opDest is the destination argument of an operation call x: the frame
// scope when the call's result holds a Task (Info's TaskResult; stage D3b,
// Rule 2, as a function's call; stage E2, C15), else "".
func (fc *funcCtx) opDest(x *syntax.CallExpr, c *check.Call) string {
	if !c.TaskResult {
		return ""
	}
	return fc.frameScope(x)
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
	if len(sig.Results) > 1 {
		unreachable(x, ruleOneResult, "the method value %s has %d results", x.Sel.Name, len(sig.Results))
	}
	recv := fc.spill(fc.value(x.X))
	return fc.envClosure(callerEnv, "e", x, func(e string) (params []string, results, body string) {
		vals := []string{recv}
		// The uniform shape of a function that returns a Task takes the
		// destination after the environment (typeCtx.funcType), which the
		// operation's call passes on (stage E2, C15).
		dest := ""
		if sig.ResultIsTask() {
			dest = fc.scope.alloc("dest")
			params = append(params, dest+" *"+fc.l.rt()+".Scope")
		}
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
		call, hasResult, fallible := fc.opCall(fc.typeOf(x.X), op, result, vals, e, dest, nil, nil, x)
		ind := strings.Repeat("\t", fc.depth)
		fc.depth--
		return params, fc.valueResults(result), uniformBody(ind, call, hasResult, result != nil, fallible)
	})
}

// uniformBody is the body of a function value of the uniform shape
// (typeCtx.funcType) that makes the Go call call: goVal and goErr are the
// call's Go form (a value, an error), and value whether the function value
// returns a value. A Go value the function value does not return is
// struct{}, a no-value type argument's (types.md, Inference), discarded; a
// function value with no value returns struct{}{} in its place.
func uniformBody(ind, call string, goVal, value, goErr bool) string {
	switch {
	case goVal && !value && goErr:
		return ind + "_, err := " + call + "\n" + ind + "return struct{}{}, err\n"
	case goVal && !value:
		return ind + "_ = " + call + "\n" + ind + "return struct{}{}, nil\n"
	case goVal && goErr:
		return ind + "return " + call + "\n"
	case goVal:
		return ind + "return " + call + ", nil\n"
	case goErr:
		return ind + "return struct{}{}, " + call + "\n"
	}
	return ind + call + "\n" + ind + "return struct{}{}, nil\n"
}

// opCall is the Go call of the operation op on a value of the operation
// type t, the one place a call (opCallExpr) and a method value
// (methodValue) write it: vals are the receiver's and the arguments' Go
// text, evaluated, and env is the environment the call is made on. An
// operation of a std type is the method of that name on the runtime's Go
// type, with its body's hidden arguments; an operation of the program's own
// operation type is the method of the Go interface it emits, which takes
// the task first (rows.md, Rows 7). result is the call's Ergo result type;
// dest is the destination of an operation that returns a Task, else "".
// x and c are the call, nil for a method value. It returns the text,
// whether the call has a value, and whether its Go form returns an error.
func (fc *funcCtx) opCall(t check.Type, op *check.Method, result check.Type, vals []string, env, dest string, x *syntax.CallExpr, c *check.Call, site syntax.Node) (string, bool, bool) {
	if (dest != "") != op.Sig.ResultIsTask() {
		ice(site, "the operation %s returns a Task %v, with a destination %q", op.Name, op.Sig.ResultIsTask(), dest)
	}
	// A type parameter's operation is its witness's (witness.go); a *T
	// receiver passes the value it points to.
	if p, ok := t.(*check.Pointer); ok {
		if tp, ok := p.Elem.(*check.TypeParam); ok {
			return fc.witnessCall(tp, op, append([]string{"*(" + vals[0] + ")"}, vals[1:]...), env, dest, site)
		}
	}
	if tp, ok := t.(*check.TypeParam); ok {
		return fc.witnessCall(tp, op, vals, env, dest, site)
	}
	if n, ok := t.(*check.Named); ok && fc.l.runtimeType(n, site) != "" {
		if dest != "" {
			ice(site, "the std operation %s.%s returns a Task", n.Obj.Name, op.Name)
		}
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
	return fc.programOpCall(t, op, vals, env, dest, site)
}

// programOpCall is a call of an operation of a program-declared operation
// type: the receiver's Go method, the task first (rows.md, Rows 7), then
// the destination of a returned Task (typeCtx.methodSig). It is never a
// runtime binding, which only runtimeCall emits: it refuses every runtime
// type, so the blocking owner scan allows it by name.
func (fc *funcCtx) programOpCall(t check.Type, op *check.Method, vals []string, env, dest string, site syntax.Node) (string, bool, bool) {
	if n, ok := t.(*check.Named); ok && fc.l.runtimeType(n, site) != "" {
		ice(site, "the std operation %s.%s bypasses its runtime binding", n.Obj.Name, op.Name)
	}
	// The checker refuses every other way to reach a sealed std operation:
	// a type defined over a std operation type (S108 at the definition)
	// and a pointer to one (S108 at the call, stage D3b Rule 5). An open
	// one (check.SealedStd), such as os.Process, is an interface of the
	// program, so its operation is the Go method below.
	if n := fc.l.foreignOwner(t); n != nil && check.SealedStd(n) {
		ice(site, "the std operation %s.%s reached through %s, which the checker refuses", n.Obj.Name, op.Name, t)
	}
	rest := []string{taskOf(env)}
	if dest != "" {
		rest = append(rest, dest)
	}
	rest = append(rest, vals[1:]...)
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
	if commaOk(body) {
		// Go's comma-ok (T, bool) is the prelude's Option[T] (stdlib.md,
		// Types 5; queue.Queue's Poll): Some of the value when ok, else
		// None, built by the helper the prelude's Get uses. The function
		// literal is called where it is made, so Go inlines it.
		if !isPreludeType(result, "Option") {
			ice(site, "a std body returns (%s, bool) for the Ergo type %s", body.Results[0], result)
		}
		ind := strings.Repeat("\t", fc.depth)
		return "func() " + fc.goType(result) + " {\n" + fc.optionReturn(result, ind+"\t", call(env), site) + ind + "}()"
	}
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
		// A Seq has no result: the uniform shape returns struct{}{} first
		// (typeCtx.funcType).
		return []string{yield + " " + yieldType}, " (struct{}, error)",
			ind + "\tfor " + v + " := range " + call(e) + " {\n" +
				ind + "\t\t" + ok2 + ", " + err + " := " + yield + "(" + e + ", " + v + ")\n" +
				ind + "\t\tif " + err + " != nil {\n" +
				ind + "\t\t\treturn struct{}{}, " + err + "\n" +
				ind + "\t\t}\n" +
				ind + "\t\tif !" + ok2 + " {\n" +
				ind + "\t\t\treturn struct{}{}, nil\n" +
				ind + "\t\t}\n" +
				ind + "\t}\n" +
				ind + "\treturn struct{}{}, nil\n"
	})
}

// commaOk reports a std body whose Go form is (T, bool): the Go form of an
// Option[T] result (stdResult).
func commaOk(body v1.Body) bool {
	return len(body.Results) == 2 && body.Results[1] == "bool"
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
			src, line, column := fc.l.locate(x, x.Pos())
			out = append(out, fc.l.rt()+".Site("+fmt.Sprintf("%q", fmt.Sprintf("%s calls %s at %s:%d:%d", fc.owner, fc.callee(x, c), src, line, column))+")")
		case v1.CallerTask:
			out = append(out, taskOf(env))
		case v1.CallerScheduler:
			// The caller's Scheduler provider (Q-S4-7): the environment's
			// field for the prelude's Scheduler, which the called
			// signature needs, so the checker has bound it at the call.
			out = append(out, env+"."+fc.l.envField(schedulerCap(c, site)))
		case v1.ReadingPackage:
			if c == nil || c.Func == nil || c.Func.Pkg == nil {
				ice(site, "a context read without its declaring package")
			}
			out = append(out, strconv.Quote(contextNamespace(c.Func.Pkg)))
		default:
			ice(x, "the hidden argument %s of a std body", h)
		}
	}
	return out
}

// schedulerCap is the prelude's Scheduler in the needs row of the std
// function the call c calls: the capability whose provider a body's hidden
// scheduler argument is.
func schedulerCap(c *check.Call, site syntax.Node) *check.TypeName {
	if c != nil && c.Func != nil {
		for _, e := range rowElems(c.Func.Sig.Uses) {
			if isPreludeType(e, "Scheduler") {
				return e.(*check.Named).Obj
			}
		}
	}
	ice(site, "a std body takes the caller's Scheduler, and the called signature needs no Scheduler")
	return nil
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

// adaptPlainFunc adapts fv, an Ergo function value of the uniform shape
// (typeCtx.funcType), to shape, a Go function parameter of a std body that
// has parameters of its own and one result, such as the comparator
// func(a T, b T) int of slices.SortFunc (std-align S2a; the G5c gap). The
// Ergo parameter's type has an empty row, and fv's row is a subset of it,
// so fv never fails and needs no capability: the adapter calls fv with the
// caller's environment, captured here, and drops its error, which a
// checked value with no error row never returns. A defect in fv is a Go
// panic, which unwinds through the Go body as through any call. fv is
// bound once, here, as adaptFunc binds it.
//
// A parameter whose Go shape takes the hidden task first and returns an
// error last, such as the f of tasks.Transform, func(*rt.Task, A) (B, error)
// (batch-12 C2), takes a function value with a row: the adapter runs fv
// on a copy of the environment that holds that task, as adaptFunc runs a
// task.Go block, and returns fv's error.
func (fc *funcCtx) adaptPlainFunc(fv string, shape *v1.Body, sig *check.Signature, site syntax.Node) string {
	tasked := len(shape.Hidden) == 1 && shape.Hidden[0] == v1.CallerTask && len(shape.Results) == 2 && shape.Results[1] == "error"
	switch {
	case len(shape.Hidden) != 0 && !tasked:
		ice(site, "a std function parameter with parameters takes the hidden argument %s", shape.Hidden[0])
	case len(shape.Params) != len(sig.Params):
		ice(site, "a std function parameter has %d Go parameters for %d Ergo parameters", len(shape.Params), len(sig.Params))
	case tasked && len(sig.Results) != 1:
		ice(site, "a std function parameter with a task returns %v for %d Ergo results", shape.Results, len(sig.Results))
	case !tasked && (len(shape.Results) != 1 || shape.Results[0] == "error" || len(sig.Results) != 1):
		ice(site, "a std function parameter with parameters returns %v for %d Ergo results", shape.Results, len(sig.Results))
	case !tasked && (len(rowElems(sig.Errors)) != 0 || len(rowElems(sig.Uses)) != 0):
		ice(site, "a std function parameter with parameters takes a function value with a row")
	}
	fv = fc.bindFunc(fv)
	binding := capturedEnv
	if tasked {
		binding = childEnv
	}
	return fc.envClosure(binding, "e", site, func(env string) (params []string, results, body string) {
		args := []string{env}
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			args = append(args, name)
		}
		ind := strings.Repeat("\t", fc.depth+1)
		call := fv + "(" + strings.Join(args, ", ") + ")"
		if tasked {
			return params, " (" + fc.goType(sig.Results[0]) + ", error)", ind + "return " + call + "\n"
		}
		r := fc.scope.alloc("r")
		return params, " " + fc.goType(sig.Results[0]), ind + r + ", _ := " + call + "\n" + ind + "return " + r + "\n"
	})
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
	if len(shape.Params) != 0 || len(sig.Params) != 0 {
		return fc.adaptPlainFunc(fv, shape, sig, site)
	}
	if len(sig.Results) > 1 {
		ice(site, "a std function parameter's block with %d results", len(sig.Results))
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
		// fv has the uniform shape: a value, struct{} when the block has
		// none, and an error (typeCtx.funcType).
		call := fv + "(" + env + ")"
		switch len(shape.Results) {
		case 0:
			body = ind + "_, _ = " + call + "\n"
		case 1:
			if len(sig.Results) != 0 {
				ice(site, "a std function parameter returns error alone and the block a value")
			}
			err := fc.scope.alloc("err")
			results, body = " error", ind+"_, "+err+" := "+call+"\n"+ind+"return "+err+"\n"
		case 2:
			switch len(sig.Results) {
			case 0:
				// A block with no value binds the body's T to no value, which
				// is struct{} (types.md, Inference; stage D3b round 1 review
				// M2): the block's own shape.
				results, body = " (struct{}, error)", ind+"return "+call+"\n"
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
