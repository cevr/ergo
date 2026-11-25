package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Operation witnesses (check Info.Instances, Witness). A generic call
// whose type parameter P an operation type constrains (`func Play[S lent
// Screen](s S, …)`) records, for P's type argument, the function that
// implements each of the constraint's operations: the package function
// of the argument's type (196m), or, when the argument is the caller's
// own type parameter, the caller's witness, or, when it is an operation
// type, the value's own provider. The test build passes the witness beside
// the value (lower/witness.go). Check time binds it in the callee's
// substitution, so an operation call on a value of P (or *P) runs the
// witnessed function with the receiver first, in the caller's
// substitution, as the test build's witness function does.

// witnessV is one type parameter's witness in a running call: the
// checked witness and the substitution of the caller that wrote it, in
// which its function's type arguments and its own argument are read.
type witnessV struct {
	w   check.Witness
	env *tenv
}

// witnessesOf are inst's witnesses, read in the caller's substitution env.
func witnessesOf(inst *check.Instance, env *tenv) map[*check.TypeParam]*witnessV {
	if inst == nil || len(inst.Witnesses) == 0 {
		return nil
	}
	out := make(map[*check.TypeParam]*witnessV, len(inst.Witnesses))
	for _, w := range inst.Witnesses {
		out[w.Param] = &witnessV{w: w, env: env}
	}
	return out
}

// witnessFor is the witness bound to tp in env or an enclosing
// substitution, or nil.
func (e *tenv) witnessFor(tp *check.TypeParam) *witnessV {
	if e == nil {
		return nil
	}
	return e.wits[tp]
}

// recvParam is the type parameter a CallOp's receiver has (P, *P, or P[…]),
// or nil when the receiver is an operation-type value.
func recvParam(t check.Type) *check.TypeParam {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	if r, ok := t.(*check.Reapplied); ok {
		return r.Param
	}
	tp, _ := t.(*check.TypeParam)
	return tp
}

// witnessCall runs the operation name of recv, a value of the type
// parameter tp, through the witness env binds for it. It reports false
// when no witness is bound, so the caller dispatches on the value.
func (in *interp) witnessCall(fr *frame, at *syntax.CallExpr, env *tenv, tp *check.TypeParam, call *check.Call, recv Value, args []Value) (Value, *abrupt, bool) {
	wv := env.witnessFor(tp)
	if wv == nil {
		return nil, nil, false
	}
	var op *check.WitnessOp
	for i := range wv.w.Ops {
		if wv.w.Ops[i].Name == call.Op.Name {
			op = &wv.w.Ops[i]
			break
		}
	}
	if op == nil {
		ice("the witness of %s has no operation %s", tp.Name, call.Op.Name)
	}
	if op.Func == nil {
		// The argument is the caller's own type parameter, whose witness
		// supplies the operation, or an operation type, whose value does.
		if outer := recvParam(wv.w.Arg); outer != nil {
			if v, ab, ok := in.witnessCall(fr, at, wv.env, outer, call, recv, args); ok {
				return v, ab, true
			}
		}
		if p, ok := recv.(*ptrV); ok {
			// A borrow of an operation-type value: its provider runs on
			// the value the place holds.
			v, ab := in.read(fr, p.root.v, p.path)
			if ab != nil {
				return nil, ab, true
			}
			recv = v
		}
		v, ab := in.dispatch(fr, at, recv, call.Op.Name, args)
		return v, ab, true
	}
	var targs []rtype
	var wits map[*check.TypeParam]*witnessV
	if op.Inst != nil {
		for _, t := range op.Inst.TypeArgs {
			targs = append(targs, wv.env.resolve(t))
		}
		wits = witnessesOf(op.Inst, wv.env)
	} else if len(op.Func.Sig.TParams) != 0 {
		// A generic operation's witness has no provider instance: its
		// type arguments are the operation call's, not the outer call's.
		bound := make(map[*check.TypeParam]check.Type)
		for _, p := range env.tps {
			bound[p] = env.checkedType(fr.pkg, p)
		}
		var own []check.Type
		for _, t := range call.TypeArgs {
			own = append(own, fr.env.checkedType(fr.pkg, t))
		}
		selfType := bound[tp]
		if call.Recv != nil {
			rt := fr.typeOf(call.Recv)
			if p, ok := rt.(*check.Pointer); ok {
				rt = p.Elem
			}
			if _, isReapplied := rt.(*check.Reapplied); isReapplied {
				selfType = fr.env.checkedType(fr.pkg, rt)
			}
		}
		var resolved []check.Type
		if op.Func.Decl != nil && op.Func.Decl.Implements != nil {
			f, args := check.ClosedFunctionClaim(fr.pkg, selfType, tp.Constraint, call.Op.Name, own, bound)
			if f != op.Func {
				return nil, fr.unsupported(at, "the generic operation %s has no closed function claim", call.Op.Name), true
			}
			resolved = args
		} else {
			resolved = check.ProviderTypeArgs(selfType, op.Func, own)
		}
		for _, t := range resolved {
			targs = append(targs, rtype{t: t})
		}
	}
	if p, ok := recv.(*ptrV); ok && len(op.Func.Sig.Params) > 0 && !isPointer(op.Func.Sig.Params[0].Type) {
		// A `mut S` receiver whose witness takes its type by value (a read
		// on a borrow, MODES-1): the function reads the borrowed place as
		// it is now, as the test build's witness does.
		v, ab := in.read(fr, p.root.v, p.path)
		if ab != nil {
			return nil, ab, true
		}
		recv = v
	}
	all := make([]Value, 0, len(args)+1)
	all = append(append(all, recv), args...)
	v, ab := in.callWitnessed(fr, at, op.Func, targs, wits, all)
	return v, ab, true
}

func (e *tenv) checkedType(pkg *check.Package, t check.Type) check.Type {
	if e == nil {
		return t
	}
	bound := make(map[*check.TypeParam]check.Type, len(e.tps))
	for i, p := range e.tps {
		r := e.args[i]
		bound[p] = r.env.checkedType(pkg, r.t)
	}
	return check.CopyReader(pkg, bound)(t)
}
