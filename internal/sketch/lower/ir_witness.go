package lower

// IR builder: the witnesses a call passes (F7 lane B). See ir_call.go.
//
// A call of a function whose type parameter a method set (or the codec
// constraint) constrains passes, after the environment, one witness per
// such parameter (witness.go; a codec's in the codec lowering): a struct of function fields the
// emitter writes as a literal at the call. A witness whose type arguments
// name no type parameter of the caller holds only closures that take
// their environment as a parameter (envClosure, callerEnv) and package
// functions, so it is a constant: the IR passes it as a package variable,
// `var ergoWitness = <the emitter's literal>`, written once, and the call
// reads it ("global"). The naming pass reserves one name per witness of
// each instance of the root package and of every library it reaches
// (reserveWitnessValues), in package then source order, only in an IR
// lowering.
//
// The callee side (B-WITNESS): a body with witness parameters takes each
// as a local, after the environment and before the parameters, as the
// emitter's head (decls.go); an operation call on a value of the type
// parameter calls the witness's field, `w.Op(env, x, args…)`, a function
// value of the uniform shape, (value, error), as any function value
// (witnessCall). A call that passes the caller's own witness on, the type
// argument the caller's type parameter itself, passes the caller's local
// when the callee's witness type is the caller's (the same operations
// for that parameter); else the call keeps the gate "call that passes
// the caller's witness". A call that the emitter sends to a copy for its
// known witness (witspec.go) goes to that copy, with no witness, as
// callText does: the IR never passes a witness the emitter's call does
// not.
//
// A copy for a known witness (G4, fc.static) takes the IR under its own
// name (copyName): an operation call on that witness is a direct call of
// the implementing function (knownWitnessOp), as the emitter's
// witnessCall calls it through the closure the witness would hold.
//
// A codec parameter (PORT-WITNESS) is a witness local too: the codec
// calls read its Enc and Dec fields and pass it to a generic codec
// function (ir_codec.go), and a call passes it on as any other.
//
// Still gated: in a copy, an operation whose function is generic, extern
// or owns its parameters, and a use of the known witness itself (the
// struct the copy builds); and a witness built from the caller's own for
// another type (a codec of []T passed to a callee), a literal the IR does
// not build yet.
//
// Prior art: Swift emits a protocol witness table for each concrete
// conformance once, as a global constant, and passes its address; GHC
// floats a dictionary for a known instance to the top level (a CAF), so a
// call passes a pointer to it; Go's own GC-shape stenciling reads a
// per-instantiation dictionary that the linker lays out statically.

import (
	"maps"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// witnessSite is the i-th witness that the use x of a generic function
// passes.
type witnessSite struct {
	x syntax.Expr
	i int
}

// reserveWitnessValues names the package variable of every witness an
// instance of the root package and its libraries passes, in package then
// source order.
func (l *lowerer) reserveWitnessValues() {
	type site struct {
		x      syntax.Expr
		n      int
		path   string
		line   int
		col    int
		pkgIdx int
	}
	var sites []site
	for pi, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
		if p.Info == nil {
			continue
		}
		for x, inst := range p.Info.Instances {
			if inst == nil || inst.Func == nil {
				continue
			}
			n := len(witnessParams(inst.Func.Sig))
			if n == 0 {
				continue
			}
			path, line, col := l.locate(x, x.Pos())
			sites = append(sites, site{x: x, n: n, path: path, line: line, col: col, pkgIdx: pi})
		}
	}
	slices.SortFunc(sites, func(a, b site) int {
		switch {
		case a.pkgIdx != b.pkgIdx:
			return a.pkgIdx - b.pkgIdx
		case a.path != b.path:
			if a.path < b.path {
				return -1
			}
			return 1
		case a.line != b.line:
			return a.line - b.line
		case a.col != b.col:
			return a.col - b.col
		}
		// A call and its callee start at one place: the longer first.
		return int(b.x.End()) - int(a.x.End())
	})
	for _, s := range sites {
		for i := 0; i < s.n; i++ {
			l.pkgName(pkgKey{"witness value", witnessSite{s.x, i}}, "ergoWitness")
		}
	}
	for _, key := range slices.Sorted(maps.Keys(l.adapterConvs)) {
		c := l.adapterConvs[key]
		if len(convParams(c)) > 0 {
			continue
		}
		to, ok := c.To.(*check.Named)
		if !ok {
			continue
		}
		iface, ok := to.Underlying().(*check.Interface)
		if !ok {
			continue
		}
		wi := 0
		for _, op := range iface.Methods {
			if f := c.Table[op.Name]; f != nil {
				wi += len(witnessParams(f.Sig))
			}
		}
		for i := 0; i < wi; i++ {
			l.pkgName(pkgKey{"ops witness", opsWitnessKey{adapter: key, idx: i}}, "ergoOpsWitness")
		}
	}
}

// witnessArgs are the witnesses the call x of f passes, as package
// variables, in f's witness parameter order; nil when f takes none.
func (b *irBuilder) witnessArgs(f *check.Func, x *syntax.CallExpr, p irPos, skip map[*check.TypeParam]bool) []*irExpr {
	params := witnessParams(f.Sig)
	if !hasWitnessArgs(params, skip) {
		return nil
	}
	inst := b.l.info.Instances[x]
	if inst == nil || inst.Func != f {
		irNo("call that passes witnesses without its instance")
	}
	open := false
	for _, t := range inst.TypeArgs {
		if len(freeTypeParams(t)) > 0 {
			open = true
		}
	}
	if open {
		return b.passedWitnesses(f, inst, slices.DeleteFunc(params, func(tp *check.TypeParam) bool { return skip[tp] }), p)
	}
	texts := b.fc.witnessArgsExcept(f, inst, x, skip)
	out := make([]*irExpr, 0, len(texts))
	for i, tp := range params {
		if skip[tp] {
			continue
		}
		text := texts[len(out)]
		key := pkgKey{"witness value", witnessSite{x, i}}
		name, ok := b.l.pkgNames[key]
		if !ok {
			irNo("witness without its reserved name")
		}
		if b.st.census == nil && !b.l.texts[key] {
			b.l.texts[key] = true
			b.l.gen.WriteString("var " + name + " = " + text + "\n\n")
		}
		out = append(out, &irExpr{K: "global", Pos: p, Name: name})
	}
	return out
}

// passedWitnesses are the witnesses a call of f at inst passes when its
// type arguments name the caller's type parameters: each the caller's
// own witness local, when the type argument is the caller's type
// parameter itself and the callee's witness type for it is the caller's
// (the emitter's literal of the caller's fields would be the same
// struct). Any other passes a literal the IR does not build yet.
func (b *irBuilder) passedWitnesses(f *check.Func, inst *check.Instance, params []*check.TypeParam, p irPos) []*irExpr {
	out := make([]*irExpr, len(params))
	for i, tp := range params {
		own, isParam := instArg(f, inst, tp, f.Decl).(*check.TypeParam)
		l, has := b.wits[own]
		if !isParam || !has || !b.sameWitness(f, inst, tp, own) {
			irNo("call that passes the caller's witness")
		}
		out[i] = &irExpr{K: "local", Pos: p, I: l}
	}
	return out
}

// sameWitness reports that the witness the callee f takes for tp at inst
// is the caller's own witness for its type parameter own: the same Go
// struct. A constraint that names only its own parameter compares by its
// witness type. One that names other parameters of the callee
// (`O Ord[T]`) compares by its operation type and its type arguments at
// inst, each a type of the caller: in a copy for an instance a Go type,
// and in a generic caller its own type parameters too (the callee's T is
// read at inst, never written in the caller).
func (b *irBuilder) sameWitness(f *check.Func, inst *check.Instance, tp, own *check.TypeParam) bool {
	if witnessClosed(tp) {
		return b.fc.witnessType(tp, own) == b.fc.witnessType(own, own)
	}
	cn, ok := tp.Constraint.(*check.Named)
	on, ok2 := own.Constraint.(*check.Named)
	if !ok || !ok2 || cn.Obj != on.Obj || len(cn.TArgs) != len(on.TArgs) {
		return false
	}
	for i, a := range cn.TArgs {
		if p, isParam := a.(*check.TypeParam); isParam {
			a = instArg(f, inst, p, f.Decl)
		} else if mentionsTypeParam(a) {
			return false // a type of the callee's parameters, no Go type here
		}
		if b.fc.goType(a) != b.fc.goType(on.TArgs[i]) {
			return false
		}
	}
	return true
}

// witnessClosed reports a constraint of tp whose operations name no type
// parameter but tp: its witness type, written in the caller, names no
// type parameter of the callee. Ord[T] of `O Ord[T]` names the callee's
// T, which is no Go type in the caller (CONSTRAINTS C4: spread calling
// below with its own T and O stopped the lowering).
func witnessClosed(tp *check.TypeParam) bool {
	it := constraintOf(tp)
	if it == nil {
		return false
	}
	for _, m := range it.Methods {
		for _, p := range freeTypeParams(m.Sig) {
			if p != tp {
				return false
			}
		}
	}
	return true
}

// witnessGate stops a body with witness parameters when the callee side
// is off (witnessIR). A codec parameter's witness is a local as any
// other's (PORT-WITNESS), read by the codec calls (codecCall). A copy for
// a known witness keeps "copy for an instance" only under
// witnessCopyIR=off (decls.go), and its operation calls on the known
// witness that knownWitnessOp cannot make direct theirs (witnessOpCall).
func (b *irBuilder) witnessGate() {
	if witnessIR == "off" {
		b.gate("witness parameter")
	}
}

// knownWitnessOp is c, the call x, as a direct call of the function that
// implements its operation, when x calls an operation on a value of a type
// parameter whose witness the copy being built knows (fc.static, G4): the
// emitter's witnessCall calls the same function, through the closure the
// witness would hold, where it is written. Else c. A generic implementing
// function (op.Inst), an extern, one with owned parameters, or a call
// through a pointer to the parameter of any but a mut operation whose
// function takes the pointer, keeps c, which witnessOpCall refuses.
// The function's fallibility is the operation's: the checker admits no
// other implementation (types.md, Interface assignability 1).
func (b *irBuilder) knownWitnessOp(c *check.Call) *check.Call {
	if c.Kind != check.CallOp || c.Recv == nil || c.Op == nil || len(b.fc.static) == 0 || witnessCopyIR == "off" || c.StartsTask {
		return c
	}
	tp, ptr := paramSource(b.l.info.Types[c.Recv])
	st := b.fc.static[tp]
	if tp == nil || st == nil {
		return c
	}
	own := staticOp(st, c.Op.Name)
	if own == nil || own.Func == nil || own.Inst != nil || len(own.Func.Sig.TParams) > 0 || own.Func.Pkg == nil || own.Func.Pkg.Info == nil {
		return c
	}
	if ptr && !(c.Op.Sig.MutSelf && takesPointer(own.Func)) {
		// Through a pointer to the parameter (s mut S, examples/vt): the
		// call passes the pointer as it is, which only a mut operation's
		// implementing function that takes the pointer receives.
		return c
	}
	g := own.Func
	gd := b.l.funcDecl(g)
	if gd.extern != "" || len(b.l.ownedParams(g)) > 0 || len(witnessParams(g.Sig)) > 0 {
		return c
	}
	return &check.Call{Kind: check.CallFunc, Func: g, Recv: c.Recv, Fails: c.Fails, Needs: c.Needs}
}

// takesPointer reports a function whose first parameter is a pointer.
func takesPointer(f *check.Func) bool {
	if len(f.Sig.Params) == 0 {
		return false
	}
	_, ok := f.Sig.Params[0].Type.(*check.Pointer)
	return ok
}

// witnessCopyIR selects G4: "" (on) or "off" (the reference: a copy for a
// known witness keeps the emitter's body, "copy for an instance"), set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.witnessCopyIR=off.
var witnessCopyIR = ""

// witnessIR selects the callee side: "" (on) or "off" (the reference: a
// body with witness parameters keeps its emitter body), set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.witnessIR=off.
// (witnessLowering is witspec.go's: whether calls go to copies at all.)
var witnessIR = ""

// witnessSlots gives each witness parameter of f its local, in the Go
// head's order (decls.go: after the environment, before the parameters).
func (b *irBuilder) witnessSlots(f *check.Func) {
	for _, tp := range witnessParams(f.Sig) {
		if b.fc.static[tp] != nil || b.fc.claims[tp] {
			continue // a copy's known witness: no parameter (decls.go)
		}
		if b.wits == nil {
			b.wits = map[*check.TypeParam]int{}
		}
		b.wits[tp] = b.next
		b.next++
	}
}

// witnessCopy is the Go name of the copy for a known witness that the
// call x of c goes to (specAt, witspec.go), or "": the copy takes no
// witness and no type arguments (callText). Its body is the IR's (G4)
// unless witnessCopyIR is "off" (irInstanceCopy).
func (b *irBuilder) witnessCopy(x *syntax.CallExpr, c *check.Call) string {
	if sp := b.fc.specAt(x, c); sp != nil && sp.wit != nil {
		return sp.name
	}
	return ""
}

// witnessOpCall is the operation call x of c on a value of the type
// parameter tp, or through a pointer to one (ptr: a mut T parameter):
// the witness's field, called
// with the environment, the value and the operands (witnessCall). The
// result has the uniform shape, (value, error), as a function value's:
// call and try bind it so (ir_call.go, ir_fail.go).
func (b *irBuilder) witnessOpCall(x *syntax.CallExpr, c *check.Call, tp *check.TypeParam, ptr bool, p irPos) ([]*irStmt, *irExpr, bool) {
	if pre, e, effect, ok := b.directOp(x, c, tp, ptr, p); ok {
		return pre, e, effect // a copy for an instance (ir_instance.go)
	}
	if b.fc.static[tp] != nil {
		// knownWitnessOp made every call it admits a direct call before
		// this (callParts); the rest keep the emitter's copy.
		irNo("operation call on a known witness")
	}
	w, ok := b.wits[tp]
	if !ok || b.fc.refSiteOf(x) != nil {
		irNo("operation call on a type parameter with no witness here")
	}
	if b.env < 0 {
		irNo("operation call from a pure function")
	}
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	if ptr && !c.Op.Sig.MutSelf {
		// A *T receiver: a mut operation's witness field takes the
		// pointer and writes through it; any other takes the value it
		// points to, as the emitter's opCall passes *(v).
		vals[0] = &irExpr{K: "deref", Pos: p, X: vals[0]}
	}
	fn := &irExpr{K: "field", Pos: p, Name: memberName(c.Op.Name), X: &irExpr{K: "local", Pos: p, I: w}}
	args := append([]*irExpr{{K: "local", Pos: p, I: b.env}}, vals...)
	return pre, &irExpr{K: "vcall", Pos: p, X: fn, Args: args}, true
}
