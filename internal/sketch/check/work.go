package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// The work of a machine's states (MU4; runtime.md, Machines and actors
// 12). A machine declaration's `work: w` entry names a package function
// w(s S) Option[W], where W is a work record (WorkStart), such as std
// machines.Work[E, R]: the runner calls the Start of each entry's work
// after the commit and interrupts it when the entry ends. Its capability
// row R is charged to the machine: the Machine value's R holds it beside
// the step's (machineValue). A Work is an ordinary record (STD1b, NP3b):
// std machines.Do and machines.Attempt build one from a function that runs
// once, as a program may, and a work that sends as it goes writes Start.
//
// The child rule (MU5, mu5b; runtime.md, Machines and actors 14) is a
// type: Start's send is an `entry func(e E)` (Signature.Entry), a value no
// Ergo code makes, since no other function type is assignable to it. A
// function that starts a child of an entry, actor.Child and actor.ChildIn,
// takes such a send; so a call passes the send its work was given, or one
// its own caller passed to a parameter written entry. It is a parameter's
// type only (non-escaping), so a send never outlives the call that got it.
//
// Prior art: effect-machine's .task(state, run, {onSuccess, onFailure})
// and its .spawn handler's context argument; XState's invoke and
// spawnChild, an action creator usable only in an assign or entry action,
// enforced at run time. Here the runner's send is the capability, and its
// type ties a child's map to the entry's signal.

// TakesWorkSend reports whether a call of f needs an entry's send: a
// parameter of its declared type is an entry func(…) (Signature.Entry). A
// tool that builds a call of a std function, such as the lowering's
// std-positions probe, asks this predicate to place the call where an
// entry's send is in scope.
func TakesWorkSend(f *Func) bool {
	if f == nil || f.Sig == nil {
		return false
	}
	for _, p := range f.Sig.Params {
		if s, ok := p.Type.(*Signature); ok && s.Entry {
			return true
		}
	}
	return false
}

// checkMachineWork checks a machine's `work: w` entry: w names a package
// function from the state to Option[W], W a work record of the signal
// (WorkStart), with no rows of its own; R is the work's capability row,
// charged to the machine (runtime.md, Machines and actors 12). It returns
// w, or nil when the entry is refused.
func (c *checker) checkMachineWork(tn *TypeName, x syntax.Expr, state, signal Type) *Func {
	want := "func(" + state.String() + ") Option[machines.Work[" + signal.String() + ", R]]"
	id, ok := x.(*syntax.Ident)
	if !ok {
		c.errorf(x, CodeType, "a machine's work names a function of this package of the type %s (runtime.md, Machines and actors 12)", want)
		return nil
	}
	f, ok := c.pkg.Scope.names[id.Name].(*Func)
	if !ok {
		if c.pkg.Scope.names[id.Name] == nil {
			c.errorf(id, CodeUnknown, "unknown function %s", id.Name)
		} else {
			c.errorf(id, CodeType, "%s is not a function; a machine's work names a function (runtime.md, Machines and actors 12)", id.Name)
		}
		return nil
	}
	f.called = true
	c.pkg.Info.Uses[id] = f
	if isExtern(f) && !externValue(f) {
		// A call-only extern is refused (P3); an extern value (G1) is
		// checked as a work like any function.
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return nil
	}
	if f.Sig == nil {
		return nil
	}
	r := c.machineWorkRow(f.Sig, state, signal)
	if r == nil {
		c.errorf(id, CodeType, "the work %s of machine %s must have the type %s, with no error or capability row of its own: R is the work's capability row, charged to the machine (runtime.md, Machines and actors 12)", id.Name, tn.Name, want)
		return nil
	}
	// Its own rows: the value must be assignable to the same function type
	// with empty rows; an inferred row is solved later, so assignment
	// checks it (rows.md, Error rows).
	wantSig := &Signature{Params: f.Sig.Params, Results: f.Sig.Results, Errors: newRow(), Uses: newRow()}
	t := &target{name: "the work of " + tn.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
	prev := c.fn
	c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
	op := c.valueExpr(x, wantSig)
	ok = c.assign(&op, wantSig, "machine "+tn.Name+"'s work")
	c.fn = prev
	if !ok {
		return nil
	}
	return f
}

// machineWorkRow is the work's capability row R when sig is func(S)
// Option[W] with no type parameters and W is a work record of the signal
// (WorkStart), else nil.
func (c *checker) machineWorkRow(sig *Signature, state, signal Type) *Row {
	option := preludeType("Option")
	if option == nil || len(sig.TParams) > 0 || len(sig.Params) != 1 || len(sig.Results) != 1 {
		return nil
	}
	if !c.identical(sig.Params[0].Type, state) {
		return nil
	}
	o, ok := sig.Results[0].(*Named)
	if !ok || o.origin() != option || len(o.TArgs) != 1 {
		return nil
	}
	_, start := WorkStart(o.TArgs[0])
	if start == nil {
		return nil
	}
	send := start.Params[0].Type.(*Signature)
	if !c.identical(send.Params[0].Type, signal) {
		return nil
	}
	if start.Uses == nil {
		return newRow()
	}
	return start.Uses
}

// WorkStart is the one field of a work record w and its type, or "" and
// nil when w is none. A work record is a declared record whose one field
// is a function func(send entry func(e E)) needs R that has no type
// parameters, results or error row, and whose send takes one signal and gives
// nothing: std machines.Work, or a program's record of that shape (STD1b:
// the checker reads the shape, not the name). The runner calls the field
// with the entry's send; the lowering reads its name here.
func WorkStart(w Type) (string, *Signature) {
	n, ok := w.(*Named)
	if !ok {
		return "", nil
	}
	st, ok := n.Underlying().(*Struct)
	if !ok || len(st.Fields) != 1 {
		return "", nil
	}
	f := st.Fields[0]
	start, ok := f.Type.(*Signature)
	if !ok || len(start.TParams) > 0 || len(start.Params) != 1 || len(start.Results) != 0 || !start.Errors.Empty() {
		return "", nil
	}
	send, ok := start.Params[0].Type.(*Signature)
	if !ok || !send.Entry || len(send.TParams) > 0 || len(send.Params) != 1 || len(send.Results) != 0 || !send.Errors.Empty() || !send.Uses.Empty() {
		return "", nil
	}
	return f.Name, start
}

// machineWork is the work function of the machine declaration t and its
// capability row, or nil when it has none or it is refused
// (checkMachineWork reports that).
func (c *checker) machineWork(t Type) (*Func, *Row) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil, nil
	}
	m, ok := n.Underlying().(*MachineType)
	spec, ok2 := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok || !ok2 {
		return nil, nil
	}
	id, ok := spec.Work.(*syntax.Ident)
	if !ok {
		return nil, nil
	}
	f, ok := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	if !ok || f.Sig == nil || isExtern(f) {
		return nil, nil
	}
	r := c.machineWorkRow(f.Sig, m.State, m.Signal)
	if r == nil {
		return nil, nil
	}
	return f, r
}
