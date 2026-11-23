package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine entries as expressions (MACHINE-EXPR; runtime.md, Machines and
// actors 1). A machine declaration's entries (its step, after, work, exit
// and an input's start) are expressions of the prelude Machine's field
// types, which the checker records (check.MachineEntry):
//
//   - a name entry, a declared function or an instance of a generic one
//     (step: stepT[T]), is called directly, as before: the plan's Step,
//     the conversion's Machine fields and a law's run call the function,
//     an instance through its plain copy (mono.go) when its type arguments
//     are known Go types;
//   - a computed entry of a machine with no type parameters is a package
//     variable of the uniform function shape, ergoStepX, set once by the
//     initializer right after the machine's initial value (machineInit),
//     as a let's value is: so a defect in it is reported at start, as the
//     evaluator's initMachines reports it. The Machine's field and the
//     plan's Step call through it; when the entry is a known builder call
//     (step: Both(left, right)), the variable holds the call's copy, which
//     the Step calls directly (entrycopy.go);
//   - a machine with type parameters (type Toggle[T] machine[…]) lowers to
//     a Go generic plan type with no methods, and its value converts to its
//     Machine through one generic Go function, ergoMachineOfX[T](env, v),
//     written once for the declaration from its own conversion
//     (check.MachineEntries.Conv): it computes the initial value and each
//     computed entry once per conversion, in the declaration's type
//     parameters, and builds the record. A conversion's text is a call of
//     it, whose type arguments Go infers from v. A function that converts
//     a generic machine's value takes the environment (pure.go,
//     pureCalls), so the call always has one to pass.
//
// The evaluator computes a generic machine's initial value and computed
// entries once per instance (eval/machineentry.go); the values agree,
// since an entry is checked in a closed context: it neither fails nor
// needs a capability, so computing it again gives an equal value.
//
// Prior art: Elm's and Elmish's programs, Gleam's actor specs and XState's
// machine configs are records of function values built by any expression;
// Effect's Layer is a value any expression builds. OCaml's functors and
// Rust's generic impls are instantiated per type, as the generic plan is
// by Go here.

// machineFields are a machine's entries by the Machine field each fills,
// in declaration order.
var machineFields = []string{"Start", "Step", "After", "Work", "Exit"}

// entryVarKey is the package-scope key of the variable of the computed
// entry field of the machine tn.
func entryVarKey(tn *check.TypeName, field string) pkgKey {
	return pkgKey{"machine " + field, tn}
}

// machineOfKey is the package-scope key of the generic machine tn's
// conversion function.
func machineOfKey(tn *check.TypeName) pkgKey { return pkgKey{"machine of", tn} }

// reserveMachineEntries names, in the naming pass, the variables of the
// machine tn's computed entries and, for a generic machine, its
// conversion function.
func (l *lowerer) reserveMachineEntries(tn *check.TypeName) {
	es := tn.Pkg.Info.MachineEntries[tn]
	if es == nil {
		return
	}
	if n, ok := tn.Type.(*check.Named); ok && len(n.TParams) > 0 {
		l.pkgName(machineOfKey(tn), "ergoMachineOf"+tn.Name)
		return
	}
	for _, field := range machineFields {
		if machineEntryOf(es, field).Computed() {
			l.pkgName(entryVarKey(tn, field), "ergo"+field+tn.Name)
			l.pkgName(entryCopyKey(tn, field), "ergoEntry"+field+tn.Name)
		}
	}
}

// machineEntryOf is the entry of es that fills the Machine field.
func machineEntryOf(es *check.MachineEntries, field string) *check.MachineEntry {
	if es == nil {
		return nil
	}
	switch field {
	case "Start":
		return es.Start
	case "Step":
		return es.Step
	case "After":
		return es.After
	case "Work":
		return es.Work
	case "Exit":
		return es.Exit
	}
	return nil
}

// genericMachine reports whether n is an instance of a machine declaration
// with type parameters.
func genericMachine(n *check.Named) bool {
	d, ok := n.Obj.Type.(*check.Named)
	return ok && len(d.TParams) > 0
}

// entryValue is the Go value of one machine entry, of the uniform function
// shape (typeCtx.funcType): g its text, sig its type at the conversion's
// type arguments, and name what a defect or a refusal calls it.
type entryValue struct {
	g    string
	sig  *check.Signature
	name string
}

// entryOf is the value of the conversion conv's entry for the Machine
// field, or nil when the machine has none: a name entry's function value,
// or a computed entry's variable (or, in a generic machine's conversion
// function, the local that holds it, fc.entryLocals).
func (fc *funcCtx) entryOf(conv check.Conversion, field string, site syntax.Node) *entryValue {
	e := conv.Entries[field]
	if e == nil {
		return nil
	}
	if e.Func != nil {
		return &entryValue{g: fc.funcValue(e.Func, e.Type, site, conv.Instances[field]), sig: e.Type, name: e.Func.Name}
	}
	name := "the " + strings.ToLower(field) + " of " + conv.From.(*check.Named).Obj.Name
	if v, ok := fc.entryLocals[field]; ok {
		return &entryValue{g: v, sig: e.Type, name: name}
	}
	n := conv.From.(*check.Named)
	return &entryValue{g: fc.l.pkgName(entryVarKey(n.Obj, field), "ergo"+field+n.Obj.Name), sig: e.Type, name: name}
}

// entryCall is the Go call of the entry e of the machine tn with no type
// parameters, on the environment env, with args: a name entry's function
// (an instance's plain copy, mono.go, or its generic function with its
// type arguments), or the computed entry's variable, whose uniform shape
// takes the environment and returns its error. fallible reports a call
// that returns an error after its value.
func (l *lowerer) entryCall(tn *check.TypeName, field string, e *check.MachineEntry, env string, args []string, site syntax.Node) (text string, fallible bool) {
	if e.Computed() {
		g := l.pkgName(entryVarKey(tn, field), "ergo"+field+tn.Name)
		if cp := l.entryCopyOf(tn, field, e); cp != nil {
			g = l.entryCopyName(cp) // a known builder call's copy (entrycopy.go)
		}
		return g + "(" + strings.Join(append([]string{env}, args...), ", ") + ")", true
	}
	f := e.Func
	fd := l.useFunc(f, site)
	if len(e.TypeArgs) == 0 {
		return fd.callText(fd.envLead(env), args), fd.fallible
	}
	fc := newFuncCtx(l, newNamer(l.names), false)
	inst := &check.Instance{Func: f, TypeArgs: e.TypeArgs}
	if i := tn.Pkg.Info.MachineEntries[tn].Conv; i != nil && i.Instances[field] != nil {
		inst = i.Instances[field]
	}
	head := fd.name + fc.typeArgs(f.Sig, e.TypeArgs, site)
	wits := fc.witnessArgs(f, inst, site)
	if mono := fc.monoAt(nil, nil, f, e.TypeArgs); mono != nil {
		head = mono.name // the instance's plain copy (mono.go)
		if fc.monoWitnesses(mono, inst); mono.wits != nil {
			wits = nil
		}
	}
	lead := append(fd.envLead(env), wits...)
	return head + "(" + strings.Join(append(lead, args...), ", ") + ")", fd.fallible
}

// entryValueCall is entryCall's call as one value of the Go type t: an
// entry with no rows returns no error, so a call that returns one (a
// computed entry's) drops it.
func (l *lowerer) entryValueCall(tn *check.TypeName, field string, e *check.MachineEntry, env string, args []string, t string, site syntax.Node) string {
	call, fallible := l.entryCall(tn, field, e, env, args, site)
	if !fallible {
		return call
	}
	return "func() " + t + " { v, _ := " + call + "; return v }()"
}

// entryEnvFree reports whether the step entry e of a machine with no type
// parameters takes no environment: a name step whose function is pure
// (machinePure); a computed step's uniform value always takes one.
func (l *lowerer) entryEnvFree(e *check.MachineEntry, site syntax.Node) bool {
	if e.Computed() || len(e.TypeArgs) > 0 {
		return false
	}
	fd := l.useFunc(e.Func, site)
	return machinePure(e.Func, fd, site)
}

// machineEntryInits lowers each computed entry of the machine td, which has
// no type parameters, into the initializer after its initial value, as
// machineInit lowers that, assigning the entry's variable.
func (l *lowerer) machineEntryInits(td *typeDecl) {
	for _, field := range machineFields {
		e := machineEntryOf(td.machine.entries, field)
		if !e.Computed() {
			continue
		}
		name := l.pkgName(entryVarKey(td.tn, field), "ergo"+field+td.tn.Name)
		if cp := l.entryCopyOf(td.tn, field, e); cp != nil {
			// The builder only makes its literal: the copy is the value.
			l.entryCopyInit(td, e.Expr, name+" = "+l.entryCopyName(cp))
			continue
		}
		l.machineInitOf(td, e.Expr, name, e.Type, "machine "+td.tn.Name+"'s "+strings.ToLower(field))
	}
}

// entryCopyInit adds the initializer's assignment of a computed entry's
// variable to its copy, at the entry x.
func (l *lowerer) entryCopyInit(td *typeDecl, x syntax.Expr, assign string) {
	l.addInit(x, x, "", []string{assign})
	l.machineInits = append(l.machineInits, machineInitial{pkg: td.tn.Pkg, x: x})
}

// machineEntryVars writes the variables of the computed entries of the
// machine td, which has no type parameters.
func (l *lowerer) machineEntryVars(b *strings.Builder, td *typeDecl, tc *typeCtx) {
	for _, field := range machineFields {
		e := machineEntryOf(td.machine.entries, field)
		if !e.Computed() {
			continue
		}
		name := l.pkgName(entryVarKey(td.tn, field), "ergo"+field+td.tn.Name)
		b.WriteString("var " + name + " " + tc.goType(e.Type) + "\n\n")
	}
}

// machineOfFunc writes the conversion function of the generic machine td,
// once, and gives its name:
//
//	func ergoMachineOfX[T any](env *Env, v X[T]) Machine[…] {
//		initial := …                   // or the Start, of an input
//		step := …                      // each computed entry
//		return Machine[…]{Initial: initial, Step: step, After: …, Work: …, Exit: …}
//	}
//
// The fields are the emitter's conversion's (convert, ConvMachine), read in
// the declaration's own type parameters.
func (l *lowerer) machineOfFunc(td *typeDecl, site syntax.Node) string {
	name := l.pkgName(machineOfKey(td.tn), "ergoMachineOf"+td.tn.Name)
	if l.machineOfDone[td.tn] {
		return name
	}
	if l.machineOfDone == nil {
		l.machineOfDone = map[*check.TypeName]bool{}
	}
	l.machineOfDone[td.tn] = true
	es := td.machine.entries
	if es.Conv == nil {
		ice(site, "the generic machine %s has no checked conversion to its Machine", td.tn.Name)
	}
	conv := *es.Conv
	if info := td.tn.Pkg.Info; l.info != info {
		saved := l.info
		l.info = info
		defer func() { l.info = saved }()
	}
	scope := newNamer(l.names)
	tparams, names := l.typeParams(td.tparams, scope, site)
	fc := newFuncCtx(l, scope, false)
	fc.params = names
	fc.site = site
	fc.depth = 1
	fc.env = scope.alloc("env")
	fc.owner = "machine " + td.tn.Name
	v := scope.alloc("v")
	fc.entryLocals = map[string]string{}
	var first string
	if start := fc.entryOfLocal(conv, "Start", site); start != nil {
		first = "Start: " + fc.machineStart(start, site)
	} else {
		fc.frame = newFrame(td.machine.state, false, false)
		l.analyzeValues(td.machine.initial, l.info)
		init := scope.alloc("initial")
		fc.line(init + " := " + fc.assigned(td.machine.initial))
		first = "Initial: " + init
	}
	for _, field := range []string{"Step", "After", "Work", "Exit"} {
		fc.entryOfLocal(conv, field, site)
	}
	step := fc.entryOf(conv, "Step", site)
	if step == nil {
		ice(site, "the generic machine %s has no step", td.tn.Name)
	}
	to := fc.goType(conv.To)
	fc.line("return " + to + "{" + first + ", Step: " + step.g + ", After: " + fc.machineAfter(conv, site) + ", Work: " + fc.machineWork(conv, site) + ", Exit: " + fc.machineExit(conv, site) + "}")
	_ = v
	head := "func " + name + tparams + "(" + fc.env + " *" + l.env() + ", " + v + " " + td.name + l.typeArgList(td.tparams, names) + ") " + to
	l.gen.WriteString(head + " {\n" + fc.out.String() + "}\n\n")
	return name
}

// entryOfLocal binds, in a generic machine's conversion function fc, the
// computed entry for field to a local holding its value, computed here, and
// gives the entry's value; nil when the machine has none.
func (fc *funcCtx) entryOfLocal(conv check.Conversion, field string, site syntax.Node) *entryValue {
	e := conv.Entries[field]
	if e == nil {
		return nil
	}
	if e.Computed() {
		if _, done := fc.entryLocals[field]; !done {
			fc.frame = newFrame(e.Type, false, false)
			fc.l.analyzeValues(e.Expr, fc.l.info)
			local := fc.scope.alloc(strings.ToLower(field))
			fc.line(local + " := " + fc.assigned(e.Expr))
			fc.entryLocals[field] = local
		}
	}
	return fc.entryOf(conv, field, site)
}

// typeArgList is the Go type argument list that names params by their Go
// names, or "" for none.
func (l *lowerer) typeArgList(params []*check.TypeParam, names map[*check.TypeParam]string) string {
	if len(params) == 0 {
		return ""
	}
	out := make([]string, len(params))
	for i, tp := range params {
		out[i] = names[tp]
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// genericMachineAt reports whether x converts a value of a generic machine
// to its Machine: the conversion calls ergoMachineOfX, which takes the
// environment (pure.go, pureCalls).
func genericMachineAt(info *check.Info, x syntax.Expr) bool {
	conv, ok := info.Implicit[x]
	if !ok || conv.Kind != check.ConvMachine {
		return false
	}
	n, ok := conv.From.(*check.Named)
	return ok && genericMachine(n)
}
