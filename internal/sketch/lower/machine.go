package lower

import (
	"go/scanner"
	"go/token"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine plan values (spec D5; runtime.md, Machines and actors 1 and 5).
// A machine declaration `type X machine[S, E] { initial: v, step: f }`
// lowers to a typed plan that no provider is built into:
//
//	type X struct{}                     // zero size: every value is equal
//	var ergoInitialX S                  // v, set once by the initializer
//	func (X) Initial() S                // the initial value
//	func (X) Step(s S, e E) Decision[S, O]
//
// X's values are ordinary values: `X{}` is bound, passed, returned and
// stored like any record value. Initial returns the value the initializer
// computed once. The initial value is checked as a package let is (in a
// closed context: no failure, no capability), and it runs in the program's
// initializer inside the root task, after every package let, package by
// package in initialization order and each package's in declaration order
// (a library's machines are lowered with its lets, program in module.go):
// no let can read a machine's initial value, so this is
// an initialization order, and a defect in it is reported at start, as a
// let's is, even when nothing uses the machine.
//
// Step calls the step function directly: no closure, no allocation. A step
// with no error or capability row whose lowered Go body never names its
// environment parameter takes no environment (spec D5, the zero-overhead
// pure-call contract): its plan passes the step function a nil one, which
// nothing reads. Purity is decided from what the lowered body reads, not
// from the rows and the footprint alone: a body with no row and an empty
// footprint still reads the environment when it opens a `with` (calls.go,
// with blocks copy it) or passes it to a function it calls. Any other step
// takes the environment first and returns the failure carrier last, as the
// step function does (D3). The Step is written after every user body is
// lowered, so the fact exists when it is read.
//
// Initial and Step are Go methods of X, so they never collide with a
// package function: a function on a machine never lowers to a Go method
// (methodReceiver takes records only). The plan has no runner of its own:
// a value X{} converts to the prelude Machine (ConvMachine, expr.go
// convert: Initial from the plan, Step the step function's value), which
// std machines folds, and the actor library (#57, #101) will run.
//
// Prior art: XState separates the machine definition from its interpreter;
// Erlang's gen_statem keeps the callbacks apart from the process that runs
// them.

// machineDecl is the checked structure of a machine declaration.
type machineDecl struct {
	state, signal check.Type
	initial       syntax.Expr // nil for a machine with an input
	step          *check.Func
	start         *check.Func // the start function of a machine with an input
}

// machineOf reads a machine declaration's checked facts: its state and
// signal types, its initial value and its step function.
func (l *lowerer) machineOf(tn *check.TypeName, named *check.Named, spec *syntax.MachineType, site syntax.Node) *machineDecl {
	m, ok := named.Underlying().(*check.MachineType)
	if !ok {
		ice(site, "the machine %s has no checked state and signal", tn.Name)
	}
	if len(named.TParams) > 0 {
		ice(site, "the machine %s has type parameters, which the grammar does not give a machine", tn.Name)
	}
	id, ok := spec.Step.(*syntax.Ident)
	if !ok || spec.Initial == nil {
		ice(site, "the machine %s has no initial value or no step name; the checker requires both", tn.Name)
	}
	step, ok := tn.Pkg.Info.Uses[id].(*check.Func)
	if !ok || step.Decl == nil || step.Decl.Body == nil || len(step.Sig.Params) != 2 || len(step.Sig.Results) != 1 {
		ice(id, "the step %s of machine %s is not a package function from its state and signal to a Decision", id.Name, tn.Name)
	}
	md := &machineDecl{state: m.State, signal: m.Signal, initial: spec.Initial, step: step}
	if iid, ok := spec.Initial.(*syntax.Ident); ok {
		if start, ok := tn.Pkg.Info.Uses[iid].(*check.Func); ok {
			// A machine with an input (V4): no initial value to compute at
			// start; a run calls the start function with its input.
			md.initial, md.start = nil, start
		}
	}
	return md
}

// machinePlan writes the plan of the machine td and records its initial
// value for the initializer.
func (l *lowerer) machinePlan(b *strings.Builder, td *typeDecl) {
	m := td.machine
	site := syntax.Node(td.tn.Decl.Name)
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	state, signal := tc.goType(m.state), tc.goType(m.signal)
	initial := l.pkgName(pkgKey{"machine initial", td.tn}, "ergoInitial"+td.tn.Name)
	fd := l.useFunc(m.step, site)
	result := tc.goType(m.step.Sig.Results[0])

	b.WriteString("type " + td.name + " struct{}\n\n")
	if m.start == nil {
		b.WriteString("var " + initial + " " + state + "\n\n")
		b.WriteString("func (" + td.name + ") Initial() " + state + " { return " + initial + " }\n\n")
	}
	// The Step waits for the step's body: the body may come later in the
	// source, and the plan's purity is a fact of that body.
	l.queue(td.tn.Decl.Name, func() {
		if !fd.lowered && len(l.refusals) > 0 {
			return // the body was refused, and that refusal is reported
		}
		b := &l.user
		if !l.isSourced(td.tn.Pkg) {
			b = &l.gen
		} else {
			b.WriteString(l.directiveAt(td.tn.Decl.Name, td.tn.Decl.Pos()))
		}
		scope := newNamer(l.names)
		s, e := scope.alloc("s"), scope.alloc("e")
		if machinePure(m.step, fd, site) {
			// A pure step function takes no environment (pure.go); another
			// step that reads none is passed nil.
			args := strings.Join(append(fd.envLead("nil"), s, e), ", ")
			b.WriteString("func (" + td.name + ") Step(" + s + " " + state + ", " + e + " " + signal + ") " + result + " {\n\treturn " + fd.name + "(" + args + ")\n}\n\n")
			return
		}
		env := scope.alloc("env")
		results := result
		if fd.fallible {
			results = "(" + result + ", error)"
		}
		b.WriteString("func (" + td.name + ") Step(" + env + " *" + l.env() + ", " + s + " " + state + ", " + e + " " + signal + ") " + results + " {\n\treturn " + fd.name + "(" + env + ", " + s + ", " + e + ")\n}\n\n")
	})
	if m.start == nil {
		l.machineInit(td, initial)
	}
}

// machinePure reports a step whose plan takes no environment: no error
// row, no capability row, and a lowered body that never names its
// environment. It reads the body's lowered fact, so it runs after the body
// is written. A non-empty footprint (a capability, a task start, an opaque
// call) always reads the environment; a body with one that reads none is a
// compiler defect, not a pure step.
func machinePure(step *check.Func, fd *funcDecl, site syntax.Node) bool {
	if !fd.lowered {
		ice(site, "the plan of a machine reads the step %s before its body is lowered", step.Name)
	}
	if !frameFootprint(step.Pkg.Info, step.Decl).Empty() && !fd.readsEnv {
		ice(site, "the step %s has a non-empty footprint and its lowered body reads no environment", step.Name)
	}
	return !fd.fallible && len(rowElems(step.Sig.Uses)) == 0 && !fd.readsEnv
}

// machineAfter is the After of a machine value's Machine (ConvMachine): the
// value of the machine's after function, or, when it has none, a function
// that gives every state None (no timer).
func (fc *funcCtx) machineAfter(conv check.Conversion, site syntax.Node) string {
	if after := conv.Table["After"]; after != nil {
		return fc.funcValue(after, after.Sig, site, nil)
	}
	return fc.machineNone(conv, "After", site)
}

// machineWork is the Work of a machine value's Machine (ConvMachine, MU4):
// a function that calls the machine's work function and gives the Start
// of its std machines.Work, or, when it has none, a function that gives
// every state None (no work). The work function has no rows of its own
// (checkMachineWork), so its error is always nil.
//
//	func(env *Env, s S) (Option[F], error) {
//		o, _ := work(env, s)
//		if w, ok := o.(Some[machines.Work[E]]); ok {
//			return Some[F]{Value: w.Value.Start}, nil
//		}
//		return None{}, nil
//	}
func (fc *funcCtx) machineWork(conv check.Conversion, site syntax.Node) string {
	work := conv.Table["Work"]
	if work == nil {
		return fc.machineNone(conv, "Work", site)
	}
	sig := fc.machineField(conv, "Work", site)
	some, none := optionVariants(sig.Results[0], site)
	wsome, _ := optionVariants(work.Sig.Results[0], site)
	g := fc.funcValue(work, work.Sig, site, nil)
	gv, o, w := fc.scope.alloc("work"), fc.scope.alloc("o"), fc.scope.alloc("w")
	return fc.envClosure(callerEnv, "env", site, func(env string) ([]string, string, string) {
		s := fc.scope.alloc("s")
		ind := strings.Repeat("\t", fc.depth+1)
		return []string{s + " " + fc.goType(sig.Params[0].Type)}, fc.valueResults(sig.Results[0]),
			ind + gv + " := " + g + "\n" +
				ind + o + ", _ := " + gv + "(" + env + ", " + s + ")\n" +
				ind + "if " + w + ", ok := " + o + ".(" + fc.goType(wsome) + "); ok {\n" +
				ind + "\treturn " + fc.goType(some) + "{Value: " + w + ".Value.Start}, nil\n" +
				ind + "}\n" +
				ind + "return " + fc.goType(none) + "{}, nil\n"
	})
}

// machineField is the type of the prelude Machine's field name, a
// function func(S) Option[…].
func (fc *funcCtx) machineField(conv check.Conversion, name string, site syntax.Node) *check.Signature {
	var sig *check.Signature
	if n, ok := conv.To.(*check.Named); ok {
		if st, ok := n.Underlying().(*check.Struct); ok {
			for _, f := range st.Fields {
				if f.Name == name {
					sig, _ = f.Type.(*check.Signature)
				}
			}
		}
	}
	if sig == nil || len(sig.Params) != 1 || len(sig.Results) != 1 {
		ice(site, "the prelude Machine has no %s func(S) Option[…]", name)
	}
	return sig
}

// machineNone is a function of the Machine field name, of the type
// func(S) Option[…], that gives every state None.
func (fc *funcCtx) machineNone(conv check.Conversion, name string, site syntax.Node) string {
	sig := fc.machineField(conv, name, site)
	_, none := optionVariants(sig.Results[0], site)
	return fc.envClosure(callerEnv, "_", site, func(string) ([]string, string, string) {
		ind := strings.Repeat("\t", fc.depth+1)
		return []string{"_ " + fc.goType(sig.Params[0].Type)}, fc.valueResults(sig.Results[0]),
			ind + "return " + fc.goType(none) + "{}, nil\n"
	})
}

// machineStart is the Start of a machine value's InputMachine (V4): the
// start function's value when it has one parameter, else a function of the
// tuple of its parameters that calls it with the tuple's elements.
func (fc *funcCtx) machineStart(start *check.Func, site syntax.Node) string {
	g := fc.funcValue(start, start.Sig, site, nil)
	sig := start.Sig
	if len(sig.Params) == 1 {
		return g
	}
	types := make([]check.Type, len(sig.Params))
	for i, p := range sig.Params {
		types[i] = p.Type
	}
	inType := fc.goType(&check.Tuple{Types: types})
	results := fc.valueResults(sig.Results[0])
	gv, in := fc.scope.alloc("start"), fc.scope.alloc("in")
	return fc.envClosure(callerEnv, "env", site, func(env string) ([]string, string, string) {
		args := []string{env}
		for i := range sig.Params {
			args = append(args, in+"."+tupleField(i))
		}
		ind := strings.Repeat("\t", fc.depth+1)
		return []string{in + " " + inType}, results,
			ind + gv + " := " + g + "\n" + ind + "return " + gv + "(" + strings.Join(args, ", ") + ")\n"
	})
}

// namesIdent reports whether the Go text src names the identifier id as a
// token: a string, a comment or a longer name that holds id does not count.
func namesIdent(src, id string) bool {
	if !strings.Contains(src, id) {
		return false
	}
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", -1, len(src)), []byte(src), nil, 0)
	for {
		_, tok, lit := s.Scan()
		switch {
		case tok == token.EOF:
			return false
		case tok == token.IDENT && lit == id:
			return true
		}
	}
}

// machineInit lowers the machine's initial value into the initializer, as
// packageVar lowers a let's, assigning the plan's variable.
func (l *lowerer) machineInit(td *typeDecl, initial string) {
	x := td.machine.initial
	fc := newFuncCtx(l, newNamer(l.names), true)
	fc.depth = 2
	fc.env = fc.scope.alloc("env") // initFunc's parameter (initEnv)
	fc.owner = "machine " + td.tn.Name
	fc.frame = newFrame(td.machine.state, false, false)
	l.recordDecl(fc.owner, x)
	val := fc.assigned(x)
	l.addInit(x, x, fc.out.String(), []string{initial + " = " + val})
	l.machineInits = append(l.machineInits, machineInitial{pkg: td.tn.Pkg, x: x})
}

// machineInitial is a machine's initial value and the package it is declared in.
type machineInitial struct {
	pkg *check.Package
	x   syntax.Expr
}
