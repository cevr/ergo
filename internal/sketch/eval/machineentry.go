package eval

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine entries as expressions (MACHINE-EXPR; runtime.md, Machines and
// actors 1). A machine declaration's entry is an expression of its Machine
// field's type (check.MachineEntry). The evaluator runs it as the test
// build does (lower/machine.go): a name entry is a call of its function;
// a computed entry's value is computed once, as a package let's is, with
// the machine's own initial value (initMachines), and called through the
// value. A generic machine's entries and initial value are computed per
// instance, at the first conversion of it, as the build's per-instance
// plan computes them.

// entryKey is one entry of one machine instance: the entry's expression
// (or the initial value's, for "initial") and the instance's type
// arguments, as typeKey writes them.
type entryKey struct {
	x    syntax.Expr
	args string
}

// machineEnv binds the type parameters of the machine declaration tn to the
// type arguments of from, its instance, read in fr's substitution; nil for
// a machine with none.
func machineEnv(fr *frame, tn *check.TypeName, from check.Type) (*tenv, string) {
	decl, _ := tn.Type.(*check.Named)
	n, _ := fr.rt(from).t.(*check.Named)
	if decl == nil || n == nil || len(decl.TParams) == 0 || len(n.TArgs) != len(decl.TParams) {
		return nil, ""
	}
	env := &tenv{}
	for i, tp := range decl.TParams {
		env.bind(tp, fr.env.resolve(n.TArgs[i]))
	}
	return env, typeKey(fr.rt(from))
}

// entryValue is the function value of the machine entry e of tn at the
// instance from: its function (an instance's, with its type arguments) for
// a name entry, else the value its expression computes, once.
func (in *interp) entryValue(fr *frame, at syntax.Node, tn *check.TypeName, e *check.MachineEntry, from check.Type) (Value, *abrupt) {
	env, args := machineEnv(fr, tn, from)
	if !e.Computed() {
		ref := &funcRef{fn: e.Func}
		for _, t := range e.TypeArgs {
			ref.targs = append(ref.targs, fr.env.resolve(t))
		}
		return ref, nil
	}
	return in.declValue(fr, at, tn, e.Expr, env, args, "the "+entryName(tn, e)+" of machine "+tn.Name)
}

// declValue is the value of x, an expression of the machine declaration
// tn, in the substitution env (args its key), computed once.
func (in *interp) declValue(fr *frame, at syntax.Node, tn *check.TypeName, x syntax.Expr, env *tenv, args, what string) (Value, *abrupt) {
	key := entryKey{x, args}
	if v, ok := in.entries[key]; ok {
		return v, nil
	}
	efr := in.newFrame(tn.Pkg, tn.Decl)
	efr.env = env
	efr.escaped = true // a computed function value may hold the frame
	v, ab := in.expr(efr, x)
	if ab != nil {
		switch ab.kind {
		case ctlDefect, ctlExhausted, ctlUnsupported:
			return nil, ab
		}
		ice("the control %d left %s", ab.kind, what)
	}
	if in.entries == nil {
		in.entries = map[entryKey]Value{}
	}
	in.entries[key] = v
	return v, nil
}

// entryName is the entry e's field in tn's declaration, for a message.
func entryName(tn *check.TypeName, e *check.MachineEntry) string {
	spec, _ := tn.Decl.Spec.(*syntax.MachineType)
	if spec != nil {
		switch e.Expr {
		case spec.Initial:
			return "start"
		case spec.After:
			return "after"
		case spec.Work:
			return "work"
		case spec.Exit:
			return "exit"
		}
	}
	return "step"
}

// computedEntries computes, once, each computed entry of the machine tn
// that has no type parameters, in the order its declaration writes them,
// after its initial value (initMachines): the build's initializer computes
// them there, so a defect in one is the run's.
func (in *interp) computedEntries(fr *frame, tn *check.TypeName) *abrupt {
	es := tn.Pkg.Info.MachineEntries[tn]
	if es == nil {
		return nil
	}
	for _, e := range []*check.MachineEntry{es.Start, es.Step, es.After, es.Work, es.Exit} {
		if !e.Computed() {
			continue
		}
		if _, ab := in.declValue(fr, e.Expr, tn, e.Expr, nil, "", fmt.Sprintf("the %s of machine %s", entryName(tn, e), tn.Name)); ab != nil {
			return ab
		}
	}
	return nil
}
