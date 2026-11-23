package check

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

// checkMachineExit checks a machine's exit entry: a function from the
// state to Option[func() needs R], with no rows of its own and no error row
// in the action, R being the action's capability row, charged to the
// machine (runtime.md, Machines and actors 12a).
func (c *checker) checkMachineExit(tn *TypeName, state Type) {
	e := c.machineEntry(tn, entryExit)
	if e == nil {
		return
	}
	if c.machineExitRow(e.Type, state) == nil {
		want := "func(" + state.String() + ") Option[func() needs R]"
		c.errorf(e.Expr, CodeType, "the exit %sof machine %s must have the type %s, with no error or capability row of its own and no error row in the action: R is the action's capability row, charged to the machine (runtime.md, Machines and actors 12a)", entryLabel(e.Expr), tn.Name, want)
	}
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

// machineExit is the exit entry of the machine type t and its action's
// capability row, at t's type arguments, or nils when it has none or it is
// refused (checkMachineExit reports that).
func (c *checker) machineExit(t Type) (*MachineEntry, *Row) {
	n, tn, m := machineDeclOf(t)
	if n == nil {
		return nil, nil
	}
	e := c.machineEntry(tn, entryExit)
	sig := c.entrySig(n, e)
	if sig == nil {
		return nil, nil
	}
	r := c.machineExitRow(sig, m.State)
	if r == nil {
		return nil, nil
	}
	return e, r
}
