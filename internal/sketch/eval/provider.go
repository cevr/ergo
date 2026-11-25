package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Derived providers with an Ergo field type (patterns.md, Provider plans 3;
// check/provider.go). An instance `T[S]` of `type T[S] derive Provider(…)`
// is a record with one field per operation of S. When its field type is
// declared in Ergo, the field's whole behavior is Ergo code: NewF makes
// it, Call records a call and returns the answer, if the test set one.
// So check time runs it as the test build does (lower/script.go):
//   - the literal T[S]{} calls NewF[A, G]() once per operation, in S's
//     order (scriptLit);
//   - its fields are read as a record's (s.Show.Calls());
//   - converted to S, it is a provider (scriptOperations) whose operation
//     packs its arguments as A (the one argument, a tuple of several,
//     the type of no arguments for none), calls Call, and calls the
//     answer with the arguments; with no answer, an operation with no
//     result and no failure is only recorded, and any other is the
//     defect "T[S]: Op has no answer" (v1.NoAnswerDefect).
//
// A field type that names a runtime Go type (std's testing.Op) has a Go
// body, which check time does not run: that literal stays a skip.

// ergoProvider reports the instance n of a derived provider whose fields
// are each of an Ergo field type.
func ergoProvider(n *check.Named) bool {
	if check.ScriptOf(n) == nil {
		return false
	}
	s := structOf(n)
	if s == nil {
		return false
	}
	for _, f := range s.Fields {
		if f.ProviderCalls() == nil {
			return false
		}
	}
	return true
}

// providerLit is the literal t{} of a derived provider instance with an
// Ergo field type: each field made by its NewF.
func (in *interp) providerLit(fr *frame, x *syntax.CompositeLit, n *check.Named, t rtype) (Value, *abrupt) {
	s := structOf(n)
	if ab := in.build(fr, x, int64(len(s.Fields)), int64(len(s.Fields)), 0); ab != nil {
		return nil, ab
	}
	out := newRecord(n, argsKey(n, t.env), len(s.Fields))
	for i, f := range s.Fields {
		pc := f.ProviderCalls()
		v, ab := in.callResolved(fr, nil, pc.New, providerTArgs(t.env, pc), nil)
		if ab != nil {
			return nil, ab
		}
		out.fields[i] = v
	}
	return out, nil
}

func providerTArgs(env *tenv, pc *check.ProviderCalls) []rtype {
	out := make([]rtype, len(pc.TypeArgs))
	for i, a := range pc.TypeArgs {
		out[i] = env.resolve(a)
	}
	return out
}

// wrapProviderConv builds a wrapping derived provider instance T[S](inner):
// fields[0..n-1] hold each operation's field initialized by NewF(op, faults),
// and fields[n] holds the wrapped inner provider value.
func (in *interp) wrapProviderConv(fr *frame, x *syntax.CallExpr, inner Value, n *check.Named, t rtype) (Value, *abrupt) {
	s := structOf(n)
	if ab := in.build(fr, x, int64(len(s.Fields)+1), int64(len(s.Fields)+1), 0); ab != nil {
		return nil, ab
	}
	out := newRecord(n, argsKey(n, t.env), len(s.Fields)+1)
	for i, f := range s.Fields {
		pc := f.ProviderCalls()
		if pc == nil || !pc.Wrapping {
			return nil, fr.unsupported(x, "a wrapping provider field %s of %s", f.Name, n)
		}
		op := f.Type.(*check.Named)
		sig, ok := op.TArgs[1].(*check.Signature)
		if !ok {
			ice("the provider field %s answers with %s, not a function", f.Name, op.TArgs[1])
		}
		errTypes := check.ErrorRowTypes(sig.Errors)
		faultNames := make([]Value, len(errTypes))
		for j, et := range errTypes {
			faultNames[j] = faultTypeName(et)
		}
		sliceVal := newSlice(faultNames)
		v, ab := in.callResolved(fr, x, pc.New, providerTArgs(t.env, pc), []Value{f.Name, sliceVal})
		if ab != nil {
			return nil, ab
		}
		out.fields[i] = v
	}
	out.fields[len(s.Fields)] = inner
	return out, nil
}

func faultTypeName(t check.Type) string {
	if n, ok := t.(*check.Named); ok && n.Obj != nil {
		return n.Obj.Name
	}
	return t.String()
}

// providerOps is a derived provider instance converted to its capability:
// the record of its fields and the substitution its type is read in.
type providerOps struct {
	rec *record
	env *tenv
}

func (p providerOps) describe() string { return p.rec.t.String() }

// opCall runs the operation name: for a recording provider, Call records args
// and gives the answer; for a wrapping provider, Wrap chooses between calling
// inner or injecting one declared failure built from Draw.
func (p providerOps) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	n := p.rec.t
	i := fieldIndex(n, name)
	s := structOf(n)
	f := s.Fields[i]
	pc := f.ProviderCalls()
	op := f.Type.(*check.Named)
	sig, ok := op.TArgs[1].(*check.Signature)
	if !ok {
		ice("the provider field %s answers with %s, not a function", name, op.TArgs[1])
	}
	call, _ := at.(*syntax.CallExpr)
	if pc.Wrapping {
		inner := p.rec.fields[len(s.Fields)]
		errTypes := check.ErrorRowTypes(sig.Errors)
		if len(errTypes) == 0 {
			return in.dispatch(fr, call, inner, name, args)
		}
		voidOp := len(sig.Results) == 0
		var resultT check.Type
		if voidOp {
			resultT = check.Typ[check.Bool]
		} else {
			resultT = sig.Results[0]
		}
		fieldVal := p.rec.fields[i]
		innerFn := &nativeFunc{
			name: "wrap.inner",
			call: func(in *interp, cfr *frame, cat syntax.Node, _ []Value) (Value, *abrupt) {
				ccall, _ := cat.(*syntax.CallExpr)
				v, ab := in.dispatch(cfr, ccall, inner, name, args)
				if ab != nil {
					return nil, ab
				}
				if voidOp {
					return true, nil
				}
				return v, nil
			},
		}
		failFn := &nativeFunc{
			name: "wrap.failWith",
			call: func(in *interp, cfr *frame, cat syntax.Node, fargs []Value) (Value, *abrupt) {
				idx := int(fargs[0].(int64))
				if idx < 0 || idx >= len(errTypes) {
					return nil, cfr.defect(cat, "fault index %d out of range [0, %d)", idx, len(errTypes))
				}
				ev, ab := in.drawFaultValue(cfr, cat, pc, p.env, fieldVal, p.env.resolve(errTypes[idx]), 0)
				if ab != nil {
					return nil, ab
				}
				return nil, cfr.abrupt(ctlFail, cat, ev, "")
			},
		}
		baseTArgs := providerTArgs(p.env, pc)
		wrapTArgs := []rtype{baseTArgs[0], baseTArgs[1], p.env.resolve(resultT), p.env.resolve(&check.RowArg{Row: sig.Errors})}
		v, ab := in.callResolved(fr, call, pc.Wrap, wrapTArgs, []Value{fieldVal, innerFn, failFn})
		if ab != nil {
			return nil, ab
		}
		if voidOp {
			return unit{}, nil
		}
		return v, nil
	}
	var a Value
	switch len(args) {
	case 0:
		none := p.env.resolve(op.TArgs[0])
		nn, ok := none.t.(*check.Named)
		if !ok {
			ice("the arguments of %s's %s are %s, not a record", n, name, none.t)
		}
		a = newRecord(nn, argsKey(nn, none.env), 0)
	case 1:
		a = args[0]
	default:
		a = tuple{elems: append([]Value(nil), args...)}
	}
	o, ab := in.callResolved(fr, call, pc.Call, providerTArgs(p.env, pc), []Value{p.rec.fields[i], a})
	if ab != nil {
		return nil, ab
	}
	// Call's result is Option[G] (check/provider.go): of its two variants,
	// the one with a field holds the answer.
	if some, ok := o.(*record); ok && len(some.fields) == 1 {
		return in.callValue(fr, at, some.fields[0], args)
	}
	if len(sig.Results) == 0 && sig.Errors.Empty() {
		return unit{}, nil // only recorded: the test set no answer
	}
	return nil, fr.defect(at, "%s: %s has no answer", n, name)
}

func (in *interp) drawFaultUint(fr *frame, at syntax.Node, pc *check.ProviderCalls, env *tenv, fieldVal Value, n uint64) (uint64, *abrupt) {
	call, _ := at.(*syntax.CallExpr)
	v, ab := in.callResolved(fr, call, pc.Draw, providerTArgs(env, pc), []Value{fieldVal, n})
	if ab != nil {
		return 0, ab
	}
	return v.(uint64), nil
}

func (in *interp) drawFaultValue(fr *frame, at syntax.Node, pc *check.ProviderCalls, env *tenv, fieldVal Value, rt rtype, depth int) (Value, *abrupt) {
	draw := func(n uint64) (uint64, *abrupt) {
		return in.drawFaultUint(fr, at, pc, env, fieldVal, n)
	}
	switch u := rt.under().t.(type) {
	case *check.Basic:
		switch u.Kind {
		case check.Bool:
			d, ab := draw(2)
			return d == 1, ab
		case check.String:
			d, ab := draw(3)
			if ab != nil {
				return nil, ab
			}
			vals := []string{"", "a", "err"}
			return vals[d], nil
		case check.Float32:
			d, ab := draw(3)
			return float32(d), ab
		case check.Float64:
			d, ab := draw(3)
			return float64(d), ab
		case check.Int, check.Int64, check.Duration:
			d, ab := draw(5)
			return boxInt(int64(d)), ab
		case check.Int8:
			d, ab := draw(5)
			return int8(d), ab
		case check.Int16:
			d, ab := draw(5)
			return int16(d), ab
		case check.Int32:
			d, ab := draw(5)
			return int32(d), ab
		case check.Uint, check.Uint64, check.Uintptr:
			d, ab := draw(5)
			return d, ab
		case check.Uint8:
			d, ab := draw(5)
			return uint8(d), ab
		case check.Uint16:
			d, ab := draw(5)
			return uint16(d), ab
		case check.Uint32:
			d, ab := draw(5)
			return uint32(d), ab
		}
	case *check.Struct:
		nn := rt.t.(*check.Named)
		rec := newRecord(nn, argsKey(nn, rt.env), len(u.Fields))
		for i, fld := range u.Fields {
			ft := rt.env.resolve(fld.Type)
			if depth >= 3 {
				v, ab := in.zero(fr, at, ft)
				if ab != nil {
					return nil, ab
				}
				rec.fields[i] = v
				continue
			}
			v, ab := in.drawFaultValue(fr, at, pc, env, fieldVal, ft, depth+1)
			if ab != nil {
				return nil, ab
			}
			rec.fields[i] = v
		}
		return rec, nil
	case *check.Union:
		if len(u.Variants) == 0 {
			return unit{}, nil
		}
		idx := uint64(0)
		if depth < 3 && len(u.Variants) > 1 {
			var ab *abrupt
			idx, ab = draw(uint64(len(u.Variants)))
			if ab != nil {
				return nil, ab
			}
		}
		vt := rt.env.resolve(u.Variants[idx])
		return in.drawFaultValue(fr, at, pc, env, fieldVal, vt, depth+1)
	case *check.Slice:
		return newSlice(nil), nil
	case *check.Map:
		return newMap(), nil
	case *check.Tuple:
		elems := make([]Value, len(u.Types))
		for i, et := range u.Types {
			v, ab := in.drawFaultValue(fr, at, pc, env, fieldVal, rt.env.resolve(et), depth+1)
			if ab != nil {
				return nil, ab
			}
			elems[i] = v
		}
		return tuple{elems: elems}, nil
	}
	return in.zero(fr, at, rt)
}
