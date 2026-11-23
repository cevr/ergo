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
	if conv.Entries["Step"] == nil {
		ice("a machine conversion with no step")
	}
	if ab := in.build(fr, at, 1, int64(len(structOf(n).Fields)), 0); ab != nil {
		return nil, ab
	}
	out := &record{t: n, key: argsKey(n, to.env), fields: make([]Value, len(structOf(n).Fields))}
	set := func(name string, v Value) { out.fields[fieldIndex(n, name)] = v }
	entry := func(name string) (Value, *abrupt) {
		e := conv.Entries[name]
		if e == nil {
			return nil, nil
		}
		return in.entryValue(fr, at, p.tn, e, conv.From)
	}
	if start := conv.Entries["Start"]; start != nil {
		v, ab := entry("Start")
		if ab != nil {
			return nil, ab
		}
		set("Start", startValue(v, start.Type))
	} else {
		v, ab := in.initialValue(fr, at, p.tn, conv.From)
		if ab != nil {
			return nil, ab
		}
		set("Initial", v)
	}
	step, ab := entry("Step")
	if ab != nil {
		return nil, ab
	}
	set("Step", step)
	// Exit (FK9-g1): the exit function, or None for every state; a fold
	// runs no exit action, so only a program that calls Exit reads it.
	for _, name := range []string{"After", "Work", "Exit"} {
		v, ab := entry(name)
		if ab != nil {
			return nil, ab
		}
		set(name, in.machineOption(to, n, name, v, conv.Entries[name]))
	}
	return out, nil
}

// initialValue is the initial value of the machine tn at the instance
// from: computed once with the package's (initMachines), or, for a generic
// machine, once per instance.
func (in *interp) initialValue(fr *frame, at syntax.Node, tn *check.TypeName, from check.Type) (Value, *abrupt) {
	if v, ok := in.initials[tn]; ok {
		return v, nil
	}
	env, args := machineEnv(fr, tn, from)
	if env == nil {
		ice("the machine %s has no initial value", tn.Name)
	}
	spec := tn.Decl.Spec.(*syntax.MachineType)
	return in.declValue(fr, at, tn, spec.Initial, env, args, "the initial value of machine "+tn.Name)
}

// startValue is the Start of an InputMachine: the start function's value,
// or, when it takes several inputs, a function of the tuple of them.
func startValue(start Value, sig *check.Signature) Value {
	if len(sig.Params) == 1 {
		return start
	}
	return &nativeFunc{name: "start", call: func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		t, ok := args[0].(tuple)
		if !ok {
			ice("the input of a machine's start is %T, no tuple", args[0])
		}
		return in.callValue(fr, at, start, t.elems)
	}}
}

// machineOption is the Machine's field name (After or Work), a function of
// a state that gives an Option: of the machine's function fn, or None
// when it has none.
func (in *interp) machineOption(to rtype, n *check.Named, name string, fn Value, e *check.MachineEntry) Value {
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
		o, ab := in.callValue(fr, at, fn, args)
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
			if check.IsEvery(t.t) {
				tv, ab := in.everyTimeout(fr, at, e.Type, result, t)
				if ab != nil {
					return nil, ab
				}
				v = tv
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

// everyTimeout is the Timeout the prelude Machine's After gives for an
// after function's machines.Every (FKG1; the build's everyTimers, lower/
// machine.go): In as written, and Signal made from time.Clock's reading
// when the task runs under std machines' firing mark (a timer that
// fires), else from the zero time, where the runner reads only In.
func (in *interp) everyTimeout(fr *frame, at syntax.Node, after *check.Signature, result rtype, every *record) (Value, *abrupt) {
	timeout, ok := optionElem(result)
	if !ok {
		ice("the prelude Machine's After gives no Option[Timeout[E]]")
	}
	sigField := every.fields[fieldIndex(every.t, "Signal")]
	var when Value
	clockT := check.EveryClock(after)
	if clockT == nil {
		ice("std machines.Every has no time.Clock")
	}
	timeT := sigParam(every)
	if in.firing {
		op, ok := fr.caps.lookup(typeKey(rtype{t: clockT, env: fr.env}))
		if !ok {
			return nil, fr.unsupported(at, "the capability %s, which no with of the run binds", clockT)
		}
		call, _ := at.(*syntax.CallExpr)
		now, ab := in.dispatch(fr, call, op, "Now", nil)
		if ab != nil {
			return nil, ab
		}
		when = now
	} else {
		z, ab := in.zero(fr, at, rtype{t: timeT, env: fr.env})
		if ab != nil {
			return nil, ab
		}
		when = z
	}
	e, ab := in.callValue(fr, at, sigField, []Value{when})
	if ab != nil {
		return nil, ab
	}
	n, ok := timeout.t.(*check.Named)
	if !ok {
		ice("Timeout is %s, no record", timeout.t)
	}
	out := &record{t: n, key: argsKey(n, timeout.env), fields: make([]Value, len(structOf(n).Fields))}
	out.fields[fieldIndex(n, "In")] = every.fields[fieldIndex(every.t, "In")]
	out.fields[fieldIndex(n, "Signal")] = e
	return out, nil
}

// sigParam is the type of the parameter of an Every's Signal field,
// time.Time.
func sigParam(every *record) check.Type {
	f := structOf(every.t).Fields[fieldIndex(every.t, "Signal")]
	if s, ok := f.Type.(*check.Signature); ok && len(s.Params) == 1 {
		return s.Params[0].Type
	}
	ice("std machines.Every has no Signal func(at time.Time) E")
	return nil
}

// optionElem is T of the Option type t: the type of the field of its one
// variant with a field.
func optionElem(t rtype) (rtype, bool) {
	u, ok := t.under().t.(*check.Union)
	if !ok {
		return rtype{}, false
	}
	for _, vt := range u.Variants {
		n, ok := vt.(*check.Named)
		if ok && structOf(n) != nil && len(structOf(n).Fields) == 1 {
			return rtype{t: structOf(n).Fields[0].Type, env: t.under().env}, true
		}
	}
	return rtype{}, false
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
