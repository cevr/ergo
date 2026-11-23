package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine entries as expressions (MACHINE-EXPR; DECL-VALUES design B (a);
// runtime.md, Machines and actors 1). A machine declaration's entries,
// initial (a value of the state, or the start function of an input),
// step, after, work and exit, are expressions of the prelude Machine's
// field types. Each is checked once, as a package let's value is: in a
// closed context, so the expression itself neither fails nor needs a
// capability (its function value's own rows are the entry's type), with
// the declaration's own type parameters in scope. Every reader of an entry
// (the shape checks here, the conversion to Machine, the laws, the
// lowering and the evaluator) reads the one MachineEntry the checker
// records, never the entry's spelling.
//
// A name entry, a declared function or an instance of a generic one, is
// called directly; any other expression is computed once and called
// through its value. Prior art: Elm's and Elmish's programs are records of
// function values (init, update, subscriptions), built by any expression;
// XState's machine config takes any action value; Effect builds a Layer
// from any expression. Gleam's actor spec is a record of functions too.

// Machine entry kinds, the keys of a machine's entries.
const (
	entryStart = "start" // the initial entry, when it is a function of an input
	entryStep  = "step"
	entryAfter = "after"
	entryWork  = "work"
	entryExit  = "exit"
)

// machineEntryKey names one entry of one declaration for the memo.
type machineEntryKey struct {
	tn   *TypeName
	kind string
}

// entry states in checker.entryState.
const (
	entryActive = 1 // being checked: a reference back to it sees no entry
	entryDone   = 2
)

// get is the entry kind of es, or nil.
func (es *MachineEntries) get(kind string) *MachineEntry {
	if es == nil {
		return nil
	}
	switch kind {
	case entryStart:
		return es.Start
	case entryStep:
		return es.Step
	case entryAfter:
		return es.After
	case entryWork:
		return es.Work
	case entryExit:
		return es.Exit
	}
	return nil
}

func (es *MachineEntries) set(kind string, e *MachineEntry) {
	switch kind {
	case entryStart:
		es.Start = e
	case entryStep:
		es.Step = e
	case entryAfter:
		es.After = e
	case entryWork:
		es.Work = e
	case entryExit:
		es.Exit = e
	}
}

// entryExpr is the expression of the entry kind in spec, or nil.
func entryExpr(spec *syntax.MachineType, kind string) syntax.Expr {
	switch kind {
	case entryStart:
		return spec.Initial
	case entryStep:
		return spec.Step
	case entryAfter:
		return spec.After
	case entryWork:
		return spec.Work
	case entryExit:
		return spec.Exit
	}
	return nil
}

// machineEntry is the entry kind of the machine declaration tn, checked
// once (checkEntryExpr), or nil when tn writes none, or it is refused. A
// declaration of another package was checked with its package; its
// entries are read from that package's Info.
func (c *checker) machineEntry(tn *TypeName, kind string) *MachineEntry {
	if tn == nil || tn.Pkg == nil || tn.Decl == nil {
		return nil
	}
	if _, ok := tn.Decl.Spec.(*syntax.MachineType); !ok {
		return nil
	}
	if tn.Pkg != c.pkg || c.pkg.Info == nil {
		if tn.Pkg.Info == nil {
			return nil
		}
		return tn.Pkg.Info.MachineEntries[tn].get(kind)
	}
	key := machineEntryKey{tn, kind}
	switch c.entryState[key] {
	case entryDone:
		return c.pkg.Info.MachineEntries[tn].get(kind)
	case entryActive:
		return nil
	}
	if c.entryState == nil {
		c.entryState = map[machineEntryKey]int{}
	}
	c.entryState[key] = entryActive
	e := c.checkEntryExpr(tn, kind)
	es := c.pkg.Info.MachineEntries[tn]
	if es == nil {
		es = &MachineEntries{}
		c.pkg.Info.MachineEntries[tn] = es
	}
	es.set(kind, e)
	c.entryState[key] = entryDone
	return e
}

// checkEntryExpr checks the expression of the entry kind of tn in the
// declaration's closed context. It reports what the expression itself
// does wrong (an unknown name, a failure, a capability, a value that is no
// function); the entry's shape against its Machine field is checked by
// the entry's own check (checkMachineStep and the others), once. An
// initial that is a value of the state is checked and assigned here, and
// it gives no entry.
func (c *checker) checkEntryExpr(tn *TypeName, kind string) *MachineEntry {
	spec := tn.Decl.Spec.(*syntax.MachineType)
	x := entryExpr(spec, kind)
	if x == nil {
		return nil
	}
	n, _ := tn.Type.(*Named)
	if n == nil {
		return nil
	}
	m, _ := n.under.(*MachineType)
	prevCur, prevFn := c.cur, c.fn
	c.cur = tn.file
	name := "the " + kind + " of " + tn.Name
	if kind == entryStart {
		name = "the initial value of " + tn.Name
	}
	c.fn = &funcCtx{
		t:       &target{name: name, declErrors: newRow(), declUses: newRow(), resultKnown: true},
		scope:   newScope(c.pkg.Scope),
		tparams: n.TParams,
	}
	defer func() { c.cur, c.fn = prevCur, prevFn }()
	if id, ok := unparen(x).(*syntax.Ident); ok {
		// A call-only extern is no function value (P3); an extern value
		// (G1) is checked as an entry like any function.
		if f, ok := c.pkg.Scope.names[id.Name].(*Func); ok && isExtern(f) && !externValue(f) {
			c.pkg.Info.Uses[id] = f
			c.errorf(id, CodeType, "%s", externCallOnlyText)
			return nil
		}
	}
	valid := m != nil && !isInvalid(m.State) && unionLike(m.State)
	var hint Type
	if kind == entryStart && valid {
		hint = m.State
	}
	op := c.valueExpr(x, hint)
	if op.invalid() {
		return nil
	}
	sig, ok := under(op.typ).(*Signature)
	if !ok {
		if kind == entryStart {
			// An initial value of the state: no entry (the state is a
			// union or a record, never a function).
			if valid {
				c.assign(&op, m.State, "machine "+tn.Name+"'s initial value")
			}
			return nil
		}
		c.errorf(x, CodeType, "%s; a machine's %s is a function value (runtime.md, Machines and actors 1)", notFunctionText(x, op.typ), kind)
		return nil
	}
	if kind != entryStep {
		// Every entry but the step has no rows of its own (Machines and
		// actors 9, 10, 12 and 12a: the runner calls it after a commit,
		// and a work's or exit action's capability is its R). An inferred
		// row is solved later, so assignment checks it (rows.md, Error
		// rows).
		want := &Signature{Params: sig.Params, Results: sig.Results, Errors: newRow(), Uses: newRow()}
		if !c.assign(&op, want, "machine "+tn.Name+"'s "+kind) {
			return nil
		}
	}
	e := &MachineEntry{Expr: x, Type: sig}
	e.Func, e.TypeArgs = c.entryName(x)
	if e.Func != nil {
		e.Func.called = true
	}
	return e
}

// notFunctionText says that the entry x, of type t, is no function.
func notFunctionText(x syntax.Expr, t Type) string {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		return id.Name + " is not a function"
	}
	return "the entry has the type " + t.String() + ", not a function type"
}

// entryName is the declared function an entry expression x names, and the
// type arguments of the instance it writes (stepT[T]), or nil when x is
// computed: a method value x.f binds x, and a call builds a value.
func (c *checker) entryName(x syntax.Expr) (*Func, []Type) {
	y := unparen(x)
	base := y
	if ix, ok := y.(*syntax.IndexExpr); ok {
		base = unparen(ix.X)
	}
	var obj Object
	switch b := base.(type) {
	case *syntax.Ident:
		obj = c.pkg.Info.Uses[b]
	case *syntax.SelectorExpr:
		pk, ok := b.X.(*syntax.Ident)
		if !ok {
			return nil, nil
		}
		if _, ok := c.pkg.Info.Uses[pk].(*PkgName); !ok {
			return nil, nil
		}
		obj = c.pkg.Info.Uses[b.Sel]
	}
	f, ok := obj.(*Func)
	if !ok || f.Sig == nil {
		return nil, nil
	}
	if len(f.Sig.TParams) == 0 {
		if base != y {
			return nil, nil
		}
		return f, nil
	}
	inst := c.pkg.Info.Instances[y]
	if inst == nil || inst.Func != f {
		return nil, nil
	}
	return f, inst.TypeArgs
}

// entrySig is the type of the entry e of the machine type t: the
// declaration's type parameters bound to t's type arguments.
func (c *checker) entrySig(t *Named, e *MachineEntry) *Signature {
	if e == nil || e.Type == nil {
		return nil
	}
	m := typeArgMap(t)
	if m == nil {
		return e.Type
	}
	s, _ := c.subst(e.Type, m).(*Signature)
	return s
}

// machineDeclOf is the declaration of the machine type t and its checked
// state and signal (at t's type arguments), or nil.
func machineDeclOf(t Type) (*Named, *TypeName, *MachineType) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil, nil, nil
	}
	m, ok := n.Underlying().(*MachineType)
	if !ok {
		return nil, nil, nil
	}
	if _, ok := n.Obj.Decl.Spec.(*syntax.MachineType); !ok {
		return nil, nil, nil
	}
	return n, n.Obj, m
}

// tupleRecordHint names the record form of a tuple state or signal (owner
// Q2 assumption (a): a tuple is no machine state, DECL-VALUES §7), so the
// message writes the repair.
func tupleRecordHint(machine, part string, t Type) string {
	tup, ok := under(t).(*Tuple)
	if !ok {
		return ""
	}
	s := "; write a record, such as type " + machine + capital(part) + " {"
	for i, e := range tup.Types {
		if i > 0 {
			s += ","
		}
		s += " " + string(rune('A'+i%26)) + " " + e.String()
	}
	return s + " } (runtime.md, Machines and actors 1)"
}

// capital is s with its first letter upper case (ASCII).
func capital(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// machineConversion is the conversion of a value of the machine type v to
// the prelude Machine (or InputMachine) t: its entries at v's type
// arguments, the name entries in its Table and their instances in
// Instances.
func (c *checker) machineConversion(v *Named, t Type, step *MachineEntry, stepSig *Signature, input bool) (Conversion, string) {
	conv := Conversion{Kind: ConvMachine, From: v, To: t, Table: map[string]*Func{}, Entries: map[string]*MachineEntry{}}
	_, tn, _ := machineDeclOf(v)
	add := func(field string, e *MachineEntry, sig *Signature) string {
		if e == nil {
			return ""
		}
		if sig == nil {
			sig = c.entrySig(v, e)
		}
		at := &MachineEntry{Expr: e.Expr, Func: e.Func, TypeArgs: c.entryTypeArgs(v, e), Type: sig}
		conv.Entries[field] = at
		if at.Func == nil {
			return ""
		}
		conv.Table[field] = at.Func
		if len(at.Func.Sig.TParams) > 0 {
			inst, why := c.witnessInstance(at.Func, at.TypeArgs)
			if why != "" {
				return why
			}
			if conv.Instances == nil {
				conv.Instances = map[string]*Instance{}
			}
			conv.Instances[field] = inst
		}
		return ""
	}
	why := add("Step", step, stepSig)
	if input && why == "" {
		why = add("Start", c.machineEntry(tn, entryStart), nil)
	}
	if after, asig := c.machineAfter(v); after != nil && why == "" {
		why = add("After", after, asig) // else every state has no timer
	}
	if work, _ := c.machineWork(v); work != nil && why == "" {
		why = add("Work", work, nil) // else every state has no work
	}
	if exit, _ := c.machineExit(v); exit != nil && why == "" {
		why = add("Exit", exit, nil) // else every state has no exit action
	}
	return conv, why
}

// entryTypeArgs are the type arguments of the name entry e at the machine
// type t's type arguments.
func (c *checker) entryTypeArgs(t *Named, e *MachineEntry) []Type {
	if len(e.TypeArgs) == 0 {
		return nil
	}
	m := typeArgMap(t)
	if m == nil {
		return e.TypeArgs
	}
	out := make([]Type, len(e.TypeArgs))
	for i, a := range e.TypeArgs {
		out[i] = c.subst(a, m)
	}
	return out
}

// typeArgMap binds the type parameters of t's origin to t's type
// arguments, or is nil when t has none.
func typeArgMap(t *Named) map[*TypeParam]Type {
	o := t.origin()
	if len(t.TArgs) == 0 || o == nil || len(o.TParams) != len(t.TArgs) {
		return nil
	}
	m := map[*TypeParam]Type{}
	for i, tp := range o.TParams {
		m[tp] = t.TArgs[i]
	}
	return m
}
