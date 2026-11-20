package lower

// IR builder: a declared function used as a value (FLIP F7, "name that is
// not a local"; pkg.F through ir_select.go). The emitter's funcValue
// (calls.go): a Go closure of the uniform shape of every function value
// (ir_closure.go sigType) whose body calls the function, the environment
// first unless the function is pure, and returns its value and error.
// The forms funcValue writes another way stay the emitter's, each under
// its own reason: a Go function, a user extern, a std extern, a generic
// function and one that takes witnesses. A function the lowering writes
// as a Go method (methodReceiver) takes its receiver first: a record, or
// a pointer to one, so the call needs no address.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// funcName is the function f, written x at p, as a value of type t; inst
// is the instance a generic function is named at (f[A, B], explicitly
// instantiated: the emitter's index), nil for a plain name.
func (b *irBuilder) funcName(f *check.Func, t check.Type, x syntax.Node, p irPos, inst *check.Instance) *irExpr {
	sig, ok := t.(*check.Signature)
	if !ok {
		irNo("function value without a function type")
	}
	return b.declaredLit(f, sig, x, p, nil)
}

// declaredLit is the closure of the uniform shape that calls f (funcName),
// with recv, when not nil, as f's first argument before the value's
// parameters: a method value's receiver, evaluated once where the value
// is (ir_methodvalue.go; the emitter's declaredValue).
func (b *irBuilder) declaredLit(f *check.Func, sig *check.Signature, x syntax.Node, p irPos, recv *irExpr) *irExpr {
	bound := 0
	if recv != nil {
		bound = 1
	}
	if f.Pkg != nil && f.Pkg.Host() {
		irNo("Go function as a value")
	}
	fd := b.l.useFunc(f, x)
	switch {
	case userExtern(f):
		irNo("user extern as a value")
	case fd.method != nil && len(sig.Params) == 0 && recv == nil:
		irNo("method as a value")
	case len(witnessParams(f.Sig)) > 0:
		irNo("function that takes witnesses as a value")
	case len(sig.Results) > 1:
		irNo("function type with several results")
	case len(sig.Params)+bound != len(f.Sig.Params):
		irNo("function value of another arity")
	}
	if _, _, std := b.l.stdBody(fd); std {
		irNo("std extern as a value")
	}
	if fd.extern != "" && b.l.intrinsic(f) == nil {
		irNo("extern as a value")
	}
	for _, tp := range f.Sig.TParams {
		if b.l.goParam(tp) && inst == nil {
			irNo("generic function as a value")
		}
	}
	goVal := len(f.Sig.Results) > 0
	value := len(sig.Results) == 1
	if goVal != value {
		irNo("function value whose result differs from its function's")
	}
	env := b.temp("env", "*"+b.l.env(), p)
	lit := &irExpr{K: "func", Pos: p, T: b.sigType(sig, x), Params: []int{env}}
	var args []*irExpr
	if !fd.envFree {
		args = append(args, &irExpr{K: "local", Pos: p, I: env})
	}
	if recv != nil {
		args = append(args, recv)
	}
	for _, prm := range sig.Params {
		i := b.temp("p", b.goType(prm.Type, x), p)
		lit.Params = append(lit.Params, i)
		args = append(args, &irExpr{K: "local", Pos: p, I: i})
	}
	call := &irExpr{K: "call", Pos: p, Name: fd.name, Args: args}
	if inst != nil && fd.method == nil {
		// An instance's call goes to its plain copy (mono.go), else names
		// its Go type arguments, as funcValue's does.
		if mono := b.fc.monoAt(nil, nil, f, inst.TypeArgs); mono != nil {
			call.Name = mono.name
		} else {
			call.T = b.sigTypeArgs(f.Sig, inst.TypeArgs, x) // ir_pkg.go
		}
	}
	if fd.method != nil {
		// A method's receiver is the value's first parameter, the
		// environment its first argument (funcValue, callText).
		r := len(args) - len(f.Sig.Params)
		call = &irExpr{K: "mcall", Pos: p, Name: fd.name, X: args[r], Args: append(args[:r:r], args[r+1:]...)}
	}
	unit := &irStmt{K: "ret", Pos: p, X: &irExpr{K: "zero", Pos: p, T: "struct{}"}}
	switch {
	case goVal:
		// The function's (value, error) pair passes on as it is; a total
		// function's value returns with a nil error (uniformBody).
		lit.Body = []*irStmt{{K: "ret", Pos: p, X: call}}
	case fd.fallible:
		// A function with no value returns only its error.
		e := b.temp("err", "error", p)
		lit.Body = []*irStmt{
			{K: "def", Pos: p, Local: e, X: call},
			{K: "if", Pos: p, End: p, X: b.notNil(e, p), Then: []*irStmt{{K: "fail", Pos: p, X: &irExpr{K: "local", Pos: p, I: e}}}},
			unit,
		}
	default:
		lit.Body = []*irStmt{{K: "expr", Pos: p, X: call}, unit}
	}
	end := p
	lit.End = &end
	return lit
}
