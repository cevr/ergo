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

// providerOps is a derived provider instance converted to its capability:
// the record of its fields and the substitution its type is read in.
type providerOps struct {
	rec *record
	env *tenv
}

func (p providerOps) describe() string { return p.rec.t.String() }

// opCall runs the operation name: Call records args and gives the answer.
func (p providerOps) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	n := p.rec.t
	i := fieldIndex(n, name)
	f := structOf(n).Fields[i]
	pc := f.ProviderCalls()
	op := f.Type.(*check.Named)
	sig, ok := op.TArgs[1].(*check.Signature)
	if !ok {
		ice("the provider field %s answers with %s, not a function", name, op.TArgs[1])
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
	call, _ := at.(*syntax.CallExpr)
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
