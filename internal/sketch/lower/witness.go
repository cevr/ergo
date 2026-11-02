package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Witnesses (D-E1; stage E1 orchestrator answer 2). A type parameter that a
// method set constrains, [T Valid], lowers to a Go type parameter
// constrained by any, and the function takes one extra value per such
// parameter after the environment (and the destination of a returned
// Task): a struct with one function field per operation, of the uniform
// function shape (typeCtx.funcType) with the type parameter first. The
// caller builds it once per call from the operations Info.Instances
// resolved (196m package functions, a caller's own witness, or an operation
// type's value), so the lowering never resolves an operation again. A Go
// method cannot carry them: Go declares methods only on the package's own
// defined types, never on int or on another package's type. Prior art:
// Swift witness tables, Haskell and Scala dictionaries; Go's own GC-shape
// stenciling calls a type parameter's methods through a dictionary too.

// witnessParams are the type parameters of sig that take a witness: the
// type-kinded ones a method set constrains, in order.
func witnessParams(sig *check.Signature) []*check.TypeParam {
	var out []*check.TypeParam
	for _, tp := range sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if it := constraintOf(tp); it != nil && len(it.Methods) > 0 {
			out = append(out, tp)
		}
	}
	return out
}

// witnessType is the Go type of the witness of tp's constraint for the
// type self: in the generic function, self is tp itself.
func (tc *typeCtx) witnessType(tp *check.TypeParam, self check.Type) string {
	it := constraintOf(tp)
	fields := make([]string, len(it.Methods))
	for i, m := range it.Methods {
		fields[i] = memberName(m.Name) + " " + tc.funcType(selfSig(self, m.Sig))
	}
	return "struct{ " + strings.Join(fields, "; ") + " }"
}

// selfSig is the operation sig as a function of a value of self, which it
// takes first.
func selfSig(self check.Type, sig *check.Signature) *check.Signature {
	out := *sig
	out.Params = append([]*check.Var{{Name: "self", Type: self}}, sig.Params...)
	return &out
}

// witnessArgs are the witnesses a call of f at the instance inst passes,
// one per witness parameter of f, in order.
func (fc *funcCtx) witnessArgs(f *check.Func, inst *check.Instance, site syntax.Node) []string {
	params := witnessParams(f.Sig)
	if len(params) == 0 {
		return nil
	}
	if inst == nil || inst.Func != f {
		ice(site, "a use of %s, whose type parameter %s a method set constrains, has no Instances entry", f.Name, params[0].Name)
	}
	out := make([]string, len(params))
	for i, tp := range params {
		var w *check.Witness
		for j := range inst.Witnesses {
			if inst.Witnesses[j].Param == tp {
				w = &inst.Witnesses[j]
			}
		}
		if w == nil {
			ice(site, "the instance of %s has no witness for %s", f.Name, tp.Name)
		}
		out[i] = fc.witnessValue(w, site)
	}
	return out
}

// witnessValue builds the witness w at a call: a struct literal of the
// caller's instance of the witness type.
func (fc *funcCtx) witnessValue(w *check.Witness, site syntax.Node) string {
	types := make([]string, len(w.Ops))
	vals := make([]string, len(w.Ops))
	for i, op := range w.Ops {
		if op.Sig == nil {
			ice(site, "the witness operation %s for %s has no signature", op.Name, w.Arg)
		}
		types[i] = memberName(op.Name) + " " + fc.goType(op.Sig)
		vals[i] = memberName(op.Name) + ": " + fc.witnessOp(w, op, site)
	}
	return "struct{ " + strings.Join(types, "; ") + " }{" + strings.Join(vals, ", ") + "}"
}

// witnessOp is one operation of a witness: the 196m function that
// implements it, the caller's own witness when the argument is the
// caller's type parameter, or a call of the operation on a value of an
// operation type.
func (fc *funcCtx) witnessOp(w *check.Witness, op check.WitnessOp, site syntax.Node) string {
	if op.Func != nil {
		if len(op.Func.Sig.TParams) > 0 && op.Inst == nil {
			ice(site, "the generic function %s implements %s for %s with no instance", op.Func.Name, op.Name, w.Arg)
		}
		return fc.funcValue(op.Func, op.Sig, site, op.Inst)
	}
	if tp, ok := w.Arg.(*check.TypeParam); ok {
		src, ok := fc.witness[tp]
		if !ok {
			ice(site, "the type parameter %s supplies %s with no witness of its own", tp.Name, op.Name)
		}
		return src + "." + memberName(op.Name)
	}
	n, ok := w.Arg.(*check.Named)
	var m *check.Method
	if ok {
		if it, isOps := n.Underlying().(*check.Interface); isOps {
			for _, cand := range it.Methods {
				if cand.Name == op.Name {
					m = cand
				}
			}
		}
	}
	if m == nil {
		ice(site, "the operation %s of %s has no implementing function and no operation type", op.Name, w.Arg)
	}
	if len(op.Sig.Results) > 1 {
		refuse(site, "an operation with several results is a tuple, lowered after check-v1 stage B (196t)")
	}
	return fc.envClosure(callerEnv, "e", site, func(e string) (params []string, results, body string) {
		var vals []string
		for _, p := range op.Sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			vals = append(vals, name)
		}
		var result check.Type
		if len(op.Sig.Results) == 1 {
			result = op.Sig.Results[0]
		}
		fc.depth++
		call, hasResult, fallible := fc.opCall(w.Arg, m, result, vals, e, nil, nil, site)
		ind := strings.Repeat("\t", fc.depth)
		fc.depth--
		return params, fc.resultList(result, true), uniformBody(ind, call, hasResult, result != nil, fallible)
	})
}

// witnessMethod is the Go method op of the adapter recv (its pointer
// receiver type, with type arguments) that converts a value of a type
// parameter to an operation type: it calls the witness the adapter holds
// in an environment of the caller's task alone, as a table function's
// adapter does, on the value (ptr: through the pointer the adapter holds).
// The field has the uniform shape; the method has the operation's own.
func (fc *funcCtx) witnessMethod(tc *typeCtx, locals *namer, recv string, op *check.Method, ptr bool) string {
	l := fc.l
	self := locals.alloc("a")
	task := locals.alloc("t")
	params := []string{task + " *" + l.taskPkg() + ".Task"}
	val := self + ".v"
	if ptr {
		val = "*" + val
	}
	args := []string{"&" + l.env() + "{" + envTask + ": " + task + "}", val}
	for _, p := range op.Sig.Params {
		n := locals.alloc("p")
		params = append(params, n+" "+tc.goType(p.Type))
		args = append(args, n)
	}
	if len(op.Sig.Results) > 1 {
		refuse(tc.site, "an operation with several results is a tuple, lowered after check-v1 stage B (196t)")
	}
	var result check.Type
	if len(op.Sig.Results) == 1 {
		result = op.Sig.Results[0]
	}
	opFallible := len(rowElems(op.Sig.Errors)) > 0
	call := self + ".w." + memberName(op.Name) + "(" + strings.Join(args, ", ") + ")"
	var body string
	switch {
	case opFallible:
		body = "\treturn " + call + "\n"
	case result != nil:
		// The operation cannot fail, so neither can the function the
		// witness holds for it: its error is nil.
		v := locals.alloc("v")
		body = "\t" + v + ", _ := " + call + "\n\treturn " + v + "\n"
	default:
		body = "\t_ = " + call + "\n"
	}
	return "func (" + self + " *" + recv + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, opFallible) + " {\n" + body + "}\n\n"
}

// witnessCall is the call of the operation op on a value of the type
// parameter tp, through tp's witness: vals are the receiver's and the
// arguments' Go text, env the environment of the call. The field has the
// uniform shape, so the call has the operation's value and an error.
func (fc *funcCtx) witnessCall(tp *check.TypeParam, op *check.Method, vals []string, env string, site syntax.Node) (string, bool, bool) {
	w, ok := fc.witness[tp]
	if !ok {
		ice(site, "the operation %s of the type parameter %s, which has no witness here", op.Name, tp.Name)
	}
	return w + "." + memberName(op.Name) + "(" + strings.Join(append([]string{env}, vals...), ", ") + ")", len(op.Sig.Results) > 0, true
}
