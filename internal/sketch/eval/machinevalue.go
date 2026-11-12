package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A machine declaration's value (ORACLE O7): `X{}` of `type X machine[S,
// E] { initial: …, step: …, after: …, work: … }` is a plan with no fields,
// and it converts to the prelude's Machine (or InputMachine, when its
// initial is a function of an input) where the checker records ConvMachine
// (README Gap 46; runtime.md, Machines and actors 10): Initial is the
// machine's initial value, computed once with the package's (init.go,
// initMachines), or Start its start function; Step its step function;
// After its after function, or one that gives every state None; and Work
// one that gives the Start of the std machines.Work its work function
// gives, or None. A fold (machines.Run, From, Initial) then runs on the
// record as on any Machine a program built itself.

// machinePlan is the value of a machine declaration's literal X{}.
type machinePlan struct{ tn *check.TypeName }

// nativeFunc is a function value the evaluator makes, not a literal or a
// declared function: a machine's After and Work, a start of several
// inputs.
type nativeFunc struct {
	name string
	call func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt)
}

// machineValue is p's Machine or InputMachine, conv.To.
func (in *interp) machineValue(fr *frame, at syntax.Node, p *machinePlan, conv check.Conversion) (Value, *abrupt) {
	to := fr.rt(conv.To)
	n, ok := to.t.(*check.Named)
	if !ok || structOf(n) == nil {
		ice("a machine converts to %s, no record", to.t)
	}
	step := conv.Table["Step"]
	if step == nil {
		ice("a machine conversion with no step")
	}
	if len(step.Sig.TParams) > 0 {
		return nil, fr.unsupported(at, "a generic machine step (%s) yet", step.Name)
	}
	if ab := in.build(fr, at, 1, int64(len(structOf(n).Fields)), 0); ab != nil {
		return nil, ab
	}
	out := &record{t: n, key: argsKey(n, to.env), fields: make([]Value, len(structOf(n).Fields))}
	set := func(name string, v Value) { out.fields[fieldIndex(n, name)] = v }
	if start := conv.Table["Start"]; start != nil {
		set("Start", startValue(start))
	} else {
		v, ok := in.initials[p.tn]
		if !ok {
			ice("the machine %s has no initial value", p.tn.Name)
		}
		set("Initial", v)
	}
	set("Step", &funcRef{fn: step})
	for _, name := range []string{"After", "Work"} {
		set(name, in.machineOption(to, n, name, conv.Table[name]))
	}
	return out, nil
}

// startValue is the Start of an InputMachine: the start function, or, when
// it takes several inputs, a function of the tuple of them.
func startValue(start *check.Func) Value {
	if len(start.Sig.Params) == 1 {
		return &funcRef{fn: start}
	}
	return &nativeFunc{name: start.Name, call: func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		t, ok := args[0].(tuple)
		if !ok {
			ice("the input of %s is %T, no tuple", start.Name, args[0])
		}
		call, _ := at.(*syntax.CallExpr)
		return in.callResolved(fr, call, start, nil, t.elems)
	}}
}

// machineOption is the Machine's field name (After or Work), a function of
// a state that gives an Option: of the machine's function fn, or None
// when it has none.
func (in *interp) machineOption(to rtype, n *check.Named, name string, fn *check.Func) Value {
	var field check.Type
	for _, f := range structOf(n).Fields {
		if f.Name == name {
			field = f.Type
		}
	}
	sig, ok := field.(*check.Signature)
	if !ok || len(sig.Results) != 1 {
		ice("the prelude Machine has no %s func(S) Option[…]", name)
	}
	result := rtype{t: sig.Results[0], env: to.env}
	return &nativeFunc{name: name, call: func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		if fn == nil {
			return optionOf(result, nil, false), nil
		}
		call, _ := at.(*syntax.CallExpr)
		o, ab := in.callResolved(fr, call, fn, nil, args)
		if ab != nil {
			return nil, ab
		}
		some, ok := o.(*record)
		if !ok || some.t.Obj.Name != "Some" {
			return optionOf(result, nil, false), nil
		}
		v := some.fields[0]
		switch name {
		case "After":
			// A timer that is not positive is the build's defect, whose text
			// prints the state as Go formats it (runtime.md, Machines and
			// actors 11 rule 7): not one the evaluator writes.
			t := v.(*record)
			if t.fields[fieldIndex(t.t, "In")].(int64) <= 0 {
				return nil, fr.unsupported(at, "a machine timer that is not positive, whose defect text the Go build formats")
			}
		case "Work":
			w, ok := v.(*record)
			if !ok {
				return nil, fr.unsupported(at, "a work value of %s yet", describe(v))
			}
			v = w.fields[fieldIndex(w.t, "Start")]
		}
		return optionOf(result, v, true), nil
	}}
}

// optionOf is Some{v} (found) or None of the Option type t.
func optionOf(t rtype, v Value, found bool) Value {
	u, ok := t.under().t.(*check.Union)
	if !ok {
		ice("%s is no Option", t.t)
	}
	want := "None"
	if found {
		want = "Some"
	}
	for _, vt := range u.Variants {
		n, ok := vt.(*check.Named)
		if !ok || n.Obj.Name != want {
			continue
		}
		r := &record{t: n, key: argsKey(n, t.under().env)}
		if found {
			r.fields = []Value{v}
		}
		return r
	}
	ice("the Option %s has no %s", t.t, want)
	return nil
}
