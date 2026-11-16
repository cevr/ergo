package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// The exit actions of a machine's states (FK9-g1; runtime.md, Machines and
// actors 12a). A machine declaration's `exit: x` entry names a package
// function x(s S) Option[func() needs R]: Some is the action the runner
// runs when an entry that holds s ends, with that state, and None is no
// action. It is the third per-state entry of one shape, beside after (the
// state's timer) and work (the state's work). The action has no error row:
// a typed failure in it is handled inside it, as scope.OnExit's body's is.
// R is charged to the machine's row W with the work's row (machineValue):
// the runner must provide it; a fold, which runs no exit action, does not.
//
// Prior art: XState's (and SCXML's) exit actions of a state, which read
// the context at the transition; gen_statem's terminate and Akka Typed's
// PostStop, which run at a stop too.

// checkMachineExit checks a machine's `exit: x` entry: x names a package
// function from the state to Option[func() needs R], with no type
// parameters and no rows of its own. It returns x, or nil when the entry is
// refused.
func (c *checker) checkMachineExit(tn *TypeName, x syntax.Expr, state Type) *Func {
	want := "func(" + state.String() + ") Option[func() needs R]"
	id, ok := x.(*syntax.Ident)
	if !ok {
		c.errorf(x, CodeType, "a machine's exit names a function of this package of the type %s (runtime.md, Machines and actors 12a)", want)
		return nil
	}
	f, ok := c.pkg.Scope.names[id.Name].(*Func)
	if !ok {
		if c.pkg.Scope.names[id.Name] == nil {
			c.errorf(id, CodeUnknown, "unknown function %s", id.Name)
		} else {
			c.errorf(id, CodeType, "%s is not a function; a machine's exit names a function (runtime.md, Machines and actors 12a)", id.Name)
		}
		return nil
	}
	f.called = true
	c.pkg.Info.Uses[id] = f
	if isExtern(f) && !externValue(f) {
		// A call-only extern is refused (P3), as for work.
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return nil
	}
	if f.Sig == nil {
		return nil
	}
	if c.machineExitRow(f.Sig, state) == nil {
		c.errorf(id, CodeType, "the exit %s of machine %s must have the type %s, with no error or capability row of its own and no error row in the action: R is the action's capability row, charged to the machine (runtime.md, Machines and actors 12a)", id.Name, tn.Name, want)
		return nil
	}
	// Its own rows: the value must be assignable to the same function type
	// with empty rows; an inferred row is solved later, so assignment
	// checks it (rows.md, Error rows), as for work.
	wantSig := &Signature{Params: f.Sig.Params, Results: f.Sig.Results, Errors: newRow(), Uses: newRow()}
	t := &target{name: "the exit of " + tn.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
	prev := c.fn
	c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
	op := c.valueExpr(x, wantSig)
	ok = c.assign(&op, wantSig, "machine "+tn.Name+"'s exit")
	c.fn = prev
	if !ok {
		return nil
	}
	return f
}

// machineExitRow is the action's capability row R when sig is func(S)
// Option[func() needs R] with no type parameters, and the action takes
// nothing, gives nothing and has no error row; else nil.
func (c *checker) machineExitRow(sig *Signature, state Type) *Row {
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
	act, ok := o.TArgs[0].(*Signature)
	if !ok || act.Entry || len(act.TParams) > 0 || len(act.Params) != 0 || len(act.Results) != 0 || !act.Errors.Empty() {
		return nil
	}
	if act.Uses == nil {
		return newRow()
	}
	return act.Uses
}

// machineExit is the exit function of the machine declaration t and its
// action's capability row, or nil when it has none or it is refused
// (checkMachineExit reports that).
func (c *checker) machineExit(t Type) (*Func, *Row) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil, nil
	}
	m, ok := n.Underlying().(*MachineType)
	spec, ok2 := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok || !ok2 {
		return nil, nil
	}
	id, ok := spec.Exit.(*syntax.Ident)
	if !ok {
		return nil, nil
	}
	f, ok := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	if !ok || f.Sig == nil || isExtern(f) {
		return nil, nil
	}
	r := c.machineExitRow(f.Sig, m.State)
	if r == nil {
		return nil, nil
	}
	return f, r
}
