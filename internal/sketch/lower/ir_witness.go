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
// A codec parameter (PORT-WITNESS) is a witness local too: the codec
// calls read its Enc and Dec fields and pass it to a generic codec
// function (ir_codec.go), and a call passes it on as any other.
//
// Still gated: in a copy for a known witness (fc.static, whose body "copy
// for an instance" keeps the emitter's until G4) an operation call on
// that witness, which the copy makes a direct call (witnessCall); and a
// witness built from the caller's own for another type (a codec of []T
// passed to a callee), a literal the IR does not build yet.
//
// Prior art: Swift emits a protocol witness table for each concrete
// conformance once, as a global constant, and passes its address; GHC
// floats a dictionary for a known instance to the top level (a CAF), so a
// call passes a pointer to it; Go's own GC-shape stenciling reads a
// per-instantiation dictionary that the linker lays out statically.

import (
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
}

// witnessArgs are the witnesses the call x of f passes, as package
// variables, in f's witness parameter order; nil when f takes none.
func (b *irBuilder) witnessArgs(f *check.Func, x *syntax.CallExpr, p irPos) []*irExpr {
	params := witnessParams(f.Sig)
	if len(params) == 0 {
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
		return b.passedWitnesses(f, inst, params, p)
	}
	texts := b.fc.witnessArgs(f, inst, x)
	out := make([]*irExpr, len(texts))
	for i, text := range texts {
		key := pkgKey{"witness value", witnessSite{x, i}}
		name, ok := b.l.pkgNames[key]
		if !ok {
			irNo("witness without its reserved name")
		}
		if b.st.census == nil && !b.l.texts[key] {
			b.l.texts[key] = true
			b.l.gen.WriteString("var " + name + " = " + text + "\n\n")
		}
		out[i] = &irExpr{K: "global", Pos: p, Name: name}
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
		if !isParam || !has || b.fc.witnessType(tp, own) != b.fc.witnessType(own, own) {
			irNo("call that passes the caller's witness")
		}
		out[i] = &irExpr{K: "local", Pos: p, I: l}
	}
	return out
}

// witnessGate stops a body with witness parameters when the callee side
// is off (witnessIR). A codec parameter's witness is a local as any
// other's (PORT-WITNESS), read by the codec calls (codecCall). A copy for
// a known witness keeps its own gate, "copy for an instance" (G4), and
// its operation calls on the known witness theirs (witnessOpCall).
func (b *irBuilder) witnessGate() {
	if witnessIR == "off" {
		b.gate("witness parameter")
	}
}

// witnessIR selects the callee side: "" (on) or "off" (the reference: a
// body with witness parameters keeps its emitter body), set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.witnessIR=off.
// (witnessLowering is witspec.go's: whether calls go to copies at all.)
var witnessIR = ""

// witnessSlots gives each witness parameter of f its local, in the Go
// head's order (decls.go: after the environment, before the parameters).
func (b *irBuilder) witnessSlots(f *check.Func) {
	for _, tp := range witnessParams(f.Sig) {
		if b.fc.static[tp] != nil {
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
// witness and no type arguments (callText). Its body is the emitter's
// (irInstanceCopy).
func (b *irBuilder) witnessCopy(x *syntax.CallExpr, c *check.Call) string {
	if sp := b.fc.specAt(x, c); sp != nil && sp.wit != nil {
		return sp.name
	}
	return ""
}

// witnessOpCall is the operation call x of c on a value of the type
// parameter tp (a call through a pointer to one, ptr, keeps its gate):
// the witness's field, called
// with the environment, the value and the operands (witnessCall). The
// result has the uniform shape, (value, error), as a function value's:
// call and try bind it so (ir_call.go, ir_fail.go).
func (b *irBuilder) witnessOpCall(x *syntax.CallExpr, c *check.Call, tp *check.TypeParam, ptr bool, p irPos) ([]*irStmt, *irExpr, bool) {
	if b.fc.static[tp] != nil {
		// A copy for a known witness calls the implementing function
		// (witnessCall); its body is the emitter's until G4 builds copies.
		irNo("operation call on a known witness")
	}
	w, ok := b.wits[tp]
	if !ok || b.fc.refSiteOf(x) != nil {
		irNo("operation call on a type parameter with no witness here")
	}
	if b.env < 0 {
		irNo("operation call from a pure function")
	}
	if ptr {
		irNo("operation call through a pointer to a type parameter")
	}
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	fn := &irExpr{K: "field", Pos: p, Name: memberName(c.Op.Name), X: &irExpr{K: "local", Pos: p, I: w}}
	args := append([]*irExpr{{K: "local", Pos: p, I: b.env}}, vals...)
	return pre, &irExpr{K: "vcall", Pos: p, X: fn, Args: args}, true
}
