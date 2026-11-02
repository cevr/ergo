package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The context owner (docs/spec/runtime.md, G1). A with whose operand the
// checker records as a context binding (With.Context, the protocol fact;
// check/context.go) lowers here and nowhere else: lowering never reads the
// operand's package or the name of the function that made it.
//
// The rule it owns: a context binding is visible in its with block and in
// every task started inside it, is immutable, and is restored on every
// exit. The operand is evaluated once, in the enclosing frame, before the
// region opens, so a failure or defect there leaves nothing bound. Its key
// and text come from the protocol's functions, called on that one value.
// Scope.OpenContext opens the region on the scope of the code at the with
// (frameScope: the frame or the innermost owning with), so the frame's End
// restores it on a typed failure that returns, a defect or a stop, the
// innermost region first; Context.End restores it at the block's normal
// end. A break, continue or return cannot leave a with block (check/
// stmt.go), so these are every exit. A task started inside the block takes
// the running task's immutable values at its start (rt.Task.WithValue), so
// it keeps them after the region ends, and siblings never see each other's
// later bindings. The region owns no task: tasks started in the block
// belong to the enclosing scope, as they would without the with.
//
// Prior art: Java's ScopedValue.where(k, v).run(…) binds for a dynamic
// extent and children inherit through StructuredTaskScope; Kotlin's
// withContext and Effect's FiberRef locally do the same with an explicit
// restore. All restore on every exit, as the frame's End does here.

// contextWith lowers `with v { block }` for a context binding cb.
func (fc *funcCtx) contextWith(x *syntax.CallExpr, cb *check.ContextBinding) operand {
	if fc.frame == nil || fc.frame.scope == "" {
		ice(x.Args[0], "a with of a context value outside a function body; the checker refuses one (S108, check/frames.go)")
	}
	list := x.Trailing.List
	var res operand
	if !fc.noValue(x) {
		res.val = fc.temp("v")
		fc.line("var " + res.val + " " + fc.goType(fc.typeOf(x)))
	}
	if fc.fallibleExpr(x) {
		res.err = fc.temp("err")
		fc.line("var " + res.err + " error")
	}
	// The operand once, in the enclosing frame; a copy, so a protocol
	// function that takes a pointer reads this value and no variable.
	val := fc.temp("cv")
	fc.line(val + " := " + fc.value(x.Args[0]))
	key := fc.contextCall(cb.Key, val, x.Args[0])
	text := fc.contextCall(cb.Text, val, x.Args[0])
	// The namespace comes from the checked binding type, even when its
	// value was made in or imported through another package.
	pkg := strconv.Quote(cb.Type.(*check.Named).Obj.Pkg.Path)
	scope := fc.frameScope(x)
	cx := fc.temp("cx")
	fc.line("{")
	fc.depth++
	fc.line(cx + " := " + fc.scopeCall("Scope.OpenContext", scope, []string{pkg, key, text}, x))
	// The block yields by the one tail rule (D-C1, D-C2), as a wire with's.
	if res.val == "" && res.err == "" {
		fc.yieldList(list, dest{kind: destStmt})
	} else {
		fc.yieldList(list, dest{kind: destAssign, name: res.val, err: res.err})
	}
	fc.line(fc.scopeCall("Context.End", cx, nil, x))
	fc.depth--
	fc.line("}")
	return res
}

// contextCall is the call of the protocol function f (contextKey or
// contextText; check.ContextBinding) on the operand's value val. The
// checker admits f only with empty rows and a string result, so the call
// cannot fail; it still takes the environment, as every lowered function
// does.
func (fc *funcCtx) contextCall(f *check.Func, val string, site syntax.Node) string {
	if len(f.Sig.TParams) > 0 {
		refuse(site, "a context value whose %s is generic is lowered after check-v1 stage E", f.Name)
	}
	if len(f.Sig.Params) != 1 || len(f.Sig.Results) != 1 {
		ice(site, "the context protocol function %s does not take one value and return its text", f.Name)
	}
	fd := fc.l.useFunc(f, site)
	if fd.extern != "" {
		refuse(site, "a context value whose %s is an extern function is lowered after check-v1 stage E", f.Name)
	}
	recv := val
	if _, byPtr := f.Sig.Params[0].Type.(*check.Pointer); byPtr {
		recv = "&" + val
	}
	env := fc.envExpr(site)
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + env + ")"
	}
	return fd.name + "(" + env + ", " + recv + ")"
}
