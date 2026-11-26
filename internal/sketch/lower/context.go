package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The context owner (docs/spec/runtime.md, G1). A with whose operand the
// checker records as a context binding (With.Value, the protocol fact;
// check/contextvalue.go) lowers here and nowhere else: lowering never reads the
// operand's package or the name of the function that made it.
//
// The rule it owns: a context binding is visible in its with block and in
// every task started inside it, is immutable, and is restored on every
// exit. The operand is evaluated once, in the enclosing frame, before the
// region opens, so a failure or defect there leaves nothing bound. Its key
// and text come from the protocol's functions, called on that one value.
// Scope.OpenContext opens the region on the scope of the code at the with
// (frameScope: the frame or the innermost owning with), so the frame's End
// restores it on a return that leaves the block (grammar.md, Statements
// 5), a typed failure that returns, a defect or a stop, the innermost
// region first; Context.End restores it at the block's normal end. A
// break or continue cannot leave a with block (check/stmt.go), so these
// are every exit. A task started inside the block takes
// the running task's immutable values at its start (rt.Task.WithValue), so
// it keeps them after the region ends, and siblings never see each other's
// later bindings. The region owns no task: tasks started in the block
// belong to the enclosing scope, as they would without the with.
//
// Prior art: Java's ScopedValue.where(k, v).run(…) binds for a dynamic
// extent and children inherit through StructuredTaskScope; Kotlin's
// withContext and Effect's FiberRef locally do the same with an explicit
// restore. All restore on every exit, as the frame's End does here.

// contextWith lowers the with operand arg, a context binding cb, around
// inner: the operands after it and the block, which yield to the with's
// destination (calls.go, with).
func (fc *funcCtx) contextWith(arg syntax.Expr, cb *check.ContextBinding, inner func()) {
	if fc.frame == nil || fc.frame.scope == "" {
		ice(arg, "a with of a context value outside a function body; the checker refuses one (S108, check/frames.go)")
	}
	// The operand once, in the enclosing frame; a copy, so a protocol
	// function that takes a pointer reads this value and no variable.
	val := fc.temp("cv")
	fc.line(val + " := " + fc.value(arg))
	key := fc.contextCall(cb.Key, cb.KeyInst, val, arg)
	text := fc.contextCall(cb.Text, cb.TextInst, val, arg)
	// The namespace comes from the checked binding type, even when its
	// value was made in or imported through another package.
	pkg := strconv.Quote(contextNamespace(cb.Type.(*check.Named).Obj.Pkg))
	scope := fc.frameScope(arg)
	cx := fc.temp("cx")
	fc.line("{")
	fc.depth++
	fc.line(cx + " := " + fc.scopeCall("Scope.OpenContext", scope, []string{pkg, key, text}, arg))
	inner()
	fc.line(fc.scopeCall("Context.End", cx, nil, arg))
	fc.depth--
	fc.line("}")
}

// contextNamespace is the namespace of the context values whose binding
// type pkg declares, and of the context reads pkg declares (std.go,
// hiddenArgs): a key is private to it (runtime/v1, contextKey). It keys on
// the package's identity, not its path (C30, 196x): a std
// package's is "std:" and its path, which no program package can have,
// since an import path holds no ":" (grammar.md, Literals 6). So a program
// package checked under a std path, such as log, never shares std log's
// keys.
func contextNamespace(pkg *check.Package) string {
	if pkg.Std() {
		return "std:" + pkg.Path
	}
	return pkg.Path
}

// contextCall is the call of the protocol function f (contextKey or
// contextText; check.ContextBinding) on the operand's value val. The
// checker admits f only with empty rows and a string result, so the call
// cannot fail; it still takes the environment, as every lowered function
// does. A generic f is called at its instance inst (ContextBinding.KeyInst,
// TextInst): its type arguments, which a Go method takes from its receiver,
// and its witnesses, passed after the environment as at any call.
func (fc *funcCtx) contextCall(f *check.Func, inst *check.Instance, val string, site syntax.Node) string {
	if (len(f.Sig.TParams) > 0) != (inst != nil) || inst != nil && inst.Func != f {
		ice(site, "the context protocol function %s has no instance that matches it", f.Name)
	}
	if len(f.Sig.Params) != 1 || len(f.Sig.Results) != 1 {
		ice(site, "the context protocol function %s does not take one value and return its text", f.Name)
	}
	fd := fc.l.useFunc(f, site)
	if fd.extern != "" {
		// A context value's type is named, outside a user extern's domain
		// (C26), and std declares no extern protocol function.
		ice(site, "a context value whose %s is an extern function, which the checker refuses (C26)", f.Name)
	}
	recv := val
	if _, byPtr := f.Sig.Params[0].Type.(*check.Pointer); byPtr {
		recv = "&" + val
	}
	// A pure protocol function takes no environment (pure.go).
	lead := fc.witnessArgs(f, inst, site)
	if !fd.envFree {
		lead = append([]string{fc.envExpr(site)}, lead...)
	}
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + strings.Join(lead, ", ") + ")"
	}
	head := fd.name
	if inst != nil {
		head += fc.typeArgs(f.Sig, inst.TypeArgs, site)
	}
	return head + "(" + strings.Join(append(lead, recv), ", ") + ")"
}

// contextWith is the IR of the context-owner with operand arg, whose
// binding is cb: the operand evaluated once into cv, protocol Key and
// Text called on it, Scope.OpenContext opened on the current scope
// (frameArg), and rest evaluated before Context.End when rest does not
// always leave the frame (early exits restore through Scope.End popping
// Scope.withs in reverse order).
func (b *irBuilder) contextWith(x *syntax.CallExpr, i int, w *check.With, arg syntax.Expr, ret bool, to *yieldTo) []*irStmt {
	cb := w.Value
	if cb == nil {
		irNo("with of a context owner without its binding")
	}
	if b.with == 0 && b.frame == 0 {
		irNo("with of a context value without a frame scope")
	}
	named, ok := cb.Type.(*check.Named)
	if !ok || named.Obj == nil || named.Obj.Pkg == nil {
		irNo("with of a context owner without a named binding type")
	}
	pre, val := b.value(arg)
	p := past(b.pos(arg, arg.Pos()), pre)
	cv := b.temp("cv", b.goType(cb.Type, arg), p)
	key := b.contextCall(cb.Key, cb.KeyInst, cv, arg, false, p)
	text := b.contextCall(cb.Text, cb.TextInst, cv, arg, true, p)
	pkg := irStringConst(p, "string", contextNamespace(named.Obj.Pkg))
	cx := b.temp("cx", b.spelling("*"+b.l.rt()+".Context"), p)
	out := append(pre,
		&irStmt{K: "def", Pos: p, Local: cv, X: val},
		&irStmt{K: "def", Pos: p, Local: cx, X: &irExpr{K: "mcall", Pos: p, Name: "OpenContext", X: b.frameArg(p), Args: []*irExpr{pkg, key, text}}},
	)
	rest := b.withRest(x, i, w, b.env, ret, to)
	out = append(out, rest...)
	if !irListEnds(rest) {
		end := b.pos(x.Trailing, x.Trailing.Rbrace)
		out = append(out, &irStmt{K: "expr", Pos: end, X: &irExpr{K: "mcall", Pos: end, Name: "End", X: &irExpr{K: "local", Pos: end, I: cx}}})
	}
	return out
}

// contextCall is the IR call of contextWith's protocol function f
// (contextKey or contextText) on the value in local cv: optional env
// first, then f's witnesses (ir_witness.go), then receiver (by address
// when f takes *T). Methods use mcall; functions use call with sigTypeArgs.
func (b *irBuilder) contextCall(f *check.Func, inst *check.Instance, cv int, arg syntax.Expr, text bool, p irPos) *irExpr {
	if (len(f.Sig.TParams) > 0) != (inst != nil) || inst != nil && inst.Func != f {
		ice(arg, "the context protocol function %s has no instance that matches it", f.Name)
	}
	if len(f.Sig.Params) != 1 || len(f.Sig.Results) != 1 {
		ice(arg, "the context protocol function %s does not take one value and return its text", f.Name)
	}
	fd := b.l.useFunc(f, arg)
	if fd.extern != "" {
		ice(arg, "a context value whose %s is an extern function, which the checker refuses (C26)", f.Name)
	}
	recv := &irExpr{K: "local", Pos: p, I: cv}
	if _, byPtr := f.Sig.Params[0].Type.(*check.Pointer); byPtr {
		recv = b.addrOf(cv, p)
	}
	var mono *specCopy
	if inst != nil {
		mono = b.fc.monoForAt(f, inst.TypeArgs, !copiesOnly(f) && !b.l.monoTaken())
		if mono != nil {
			b.fc.monoWitnesses(mono, inst)
		}
	}
	var lead []*irExpr
	if mono == nil || mono.wits == nil {
		lead = b.contextWitnessArgs(f, inst, arg, text, p, mono.claimParams())
	}
	if !fd.envFree {
		lead = append([]*irExpr{{K: "local", Pos: p, I: b.env}}, lead...)
	}
	if mono != nil {
		return &irExpr{K: "call", Pos: p, Name: mono.name, Args: append(lead, recv)}
	}
	if fd.method != nil {
		return &irExpr{K: "mcall", Pos: p, Name: fd.name, X: recv, Args: lead}
	}
	var targs string
	if inst != nil {
		targs = b.sigTypeArgs(f.Sig, inst.TypeArgs, arg)
	}
	return &irExpr{K: "call", Pos: p, Name: fd.name, T: targs, Args: append(lead, recv)}
}
