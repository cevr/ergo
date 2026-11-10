package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/machinegraph"
	"ergo.local/prototype/internal/sketch/syntax"
	rtpkg "ergo.local/prototype/runtime/rt"
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
		if l.isSourced(td.tn.Pkg) {
			// Generated code: no directive maps it to the source.
			l.gen.WriteString(l.machineGraph(td, tc, state, signal))
		}
		scope := newNamer(l.names)
		s, e := scope.alloc("s"), scope.alloc("e")
		// A step over a record state lowers to a Go method of the record
		// (methodReceiver), so the state is its receiver (fu-mb2 F10).
		call := func(lead []string) string {
			if fd.method != nil {
				return "(" + s + ")." + fd.name + "(" + strings.Join(append(lead, e), ", ") + ")"
			}
			return fd.name + "(" + strings.Join(append(lead, s, e), ", ") + ")"
		}
		if machinePure(m.step, fd, site) {
			// A pure step function takes no environment (pure.go); another
			// step that reads none is passed nil.
			b.WriteString("func (" + td.name + ") Step(" + s + " " + state + ", " + e + " " + signal + ") " + result + " {\n\treturn " + call(fd.envLead("nil")) + "\n}\n\n")
			return
		}
		env := scope.alloc("env")
		results := result
		if fd.fallible {
			results = "(" + result + ", error)"
		}
		b.WriteString("func (" + td.name + ") Step(" + env + " *" + l.env() + ", " + s + " " + state + ", " + e + " " + signal + ") " + results + " {\n\treturn " + call([]string{env}) + "\n}\n\n")
	})
	if m.start == nil {
		l.machineInit(td, initial)
	}
}

// Machine graphs (MU15; binsize BS7; runtime.md, Machines and actors 15).
// A traced actor names the arm of each decision and the signal of each
// timer from its machine's graph: the step's arms, each with its .ergo
// position and the variants of the states and signals it takes, and the
// signal variant of each state's timer. The graph is static data: a
// package variable of constant composite literals, which the Go linker
// lays out and no initializer builds, and which it drops when no actor
// start is reached. The variant names are constants the lowering derives
// from the Go type (variantName), the name a runner gives a value it holds.
//
// An actor's start (MachineSupervise, MachineChild) takes the graph as its
// hidden graph argument (v1.Graph): ergoMachineGraph[S, E], a generated
// function whose type switch gives the graph of the machine of states S
// and signals E. It is the start's own type arguments that choose, so a
// Machine record built from parts (machines.Start, actor.StartDurable)
// keeps its declaration's arms. Two machines with the same state and
// signal types get no case, so their actors report no arm, as the runtime
// could not tell their arms apart.
//
// graphLowering "registry" is the reference, the lowering before BS7: an
// init function per machine registers its graph, with names VariantOf
// computes at start-up, in the runtime's registry (v1.RegisterMachine),
// and the hidden argument reads it (v1.RegisteredGraph). Select it with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.graphLowering=registry.
var graphLowering string

// graphRegistry reports the reference lowering of machine graphs.
func graphRegistry() bool { return graphLowering == "registry" }

// machineGraphEntry is one machine's static graph: the Go types of its
// states and signals and the name and value of its variable.
type machineGraphEntry struct {
	state, signal, name, value string
}

// machineGraph lowers the machine td's graph: under the reference, the
// init function that registers it; else an entry of l.machineGraphs,
// which graphAccessor writes. state and signal are the machine's Go types.
func (l *lowerer) machineGraph(td *typeDecl, tc *typeCtx, state, signal string) string {
	spec, ok := td.tn.Decl.Spec.(*syntax.MachineType)
	if !ok {
		return ""
	}
	g := machinegraph.Of(td.tn.Pkg, td.tn, spec, func(n syntax.Node, pos syntax.Pos) machinegraph.Site {
		_, path, line, _ := l.at(n, pos)
		return machinegraph.Site{File: path, Line: line}
	})
	if g == nil {
		return ""
	}
	rt := l.rt()
	// The Go text of each state's and each signal's variant name, by its
	// Ergo name: two maps, since a state and a signal variant may share
	// an Ergo name (two instances of one generic record).
	names := func(vs []machinegraph.Variant) map[string]string {
		goOf := map[string]string{}
		for _, v := range vs {
			t := tc.goType(v.Type)
			if graphRegistry() {
				goOf[v.Name] = rt + ".VariantOf[" + t + "]()"
				continue
			}
			name, ok := l.variantName(t)
			if !ok {
				ice(td.tn.Decl.Name, "the machine %s has the variant %s, whose Go type %s the lowering cannot name", td.tn.Name, v.Name, t)
			}
			goOf[v.Name] = strconv.Quote(name)
		}
		return goOf
	}
	stateOf, signalOf := names(g.States), names(g.Signals)
	list := func(vs []string, goOf map[string]string) string {
		if vs == nil {
			return "nil"
		}
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = goOf[v]
		}
		return "[]string{" + strings.Join(out, ", ") + "}"
	}
	var b strings.Builder
	b.WriteString(rt + ".MachineGraph{\n\tArms: []" + rt + ".MachineArm{\n")
	for _, a := range g.Arms {
		// A case of several tuple patterns is one runtime arm per
		// pattern, at one site, which the runner names once.
		alts := a.Alts
		if alts == nil {
			alts = []machinegraph.Alt{{States: a.States, Signals: a.Signals, Partial: a.Partial}}
		}
		for _, x := range alts {
			fmt.Fprintf(&b, "\t\t{Site: %s, States: %s, Signals: %s, Partial: %t},\n", strconv.Quote(a.Site.String()), list(x.States, stateOf), list(x.Signals, signalOf), x.Partial)
		}
	}
	b.WriteString("\t},\n\tTimers: []" + rt + ".MachineTimer{\n")
	keys := make([]string, 0, len(g.Timers))
	for k := range g.Timers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sig := g.Timers[k].Signal; sig != machinegraph.Any {
			b.WriteString("\t\t{State: " + stateOf[k] + ", Signal: " + signalOf[sig] + "},\n")
		}
	}
	b.WriteString("\t},\n}")
	if graphRegistry() {
		return "func init() {\n\t" + rt + ".RegisterMachine[" + state + ", " + signal + "](" + b.String() + ")\n}\n\n"
	}
	l.machineGraphs = append(l.machineGraphs, machineGraphEntry{state: state, signal: signal,
		name: l.pkgName(pkgKey{"machine graph", td.tn}, "ergoGraph"+td.tn.Name), value: b.String()})
	return ""
}

// graphArg is the hidden graph argument of an actor's start whose states
// and signals are the Go types state and signal.
func (l *lowerer) graphArg(state, signal string) string {
	if graphRegistry() {
		return l.rt() + ".RegisteredGraph[" + state + ", " + signal + "]"
	}
	l.graphsUsed = true
	return l.pkgName(pkgKey{"machine graphs", nil}, "ergoMachineGraph") + "[" + state + ", " + signal + "]"
}

// graphAccessor writes ergoMachineGraph, when an actor's start names it,
// and the graph variables its cases give: one case per pair of state and
// signal types that one machine alone has.
//
//	type ergoMachinePair[S, E any] struct{}
//
//	func ergoMachineGraph[S, E any]() *ergo.MachineGraph {
//		switch any(ergoMachinePair[S, E]{}).(type) {
//		case ergoMachinePair[Phase, Signal]:
//			return &ergoGraphFinder
//		}
//		return nil
//	}
//
//	var ergoGraphFinder = ergo.MachineGraph{…}
func (l *lowerer) graphAccessor() string {
	if !l.graphsUsed {
		return ""
	}
	pairs := map[[2]string]int{}
	for _, e := range l.machineGraphs {
		pairs[[2]string{e.state, e.signal}]++
	}
	scope := newNamer(l.names)
	s, e := scope.alloc("S"), scope.alloc("E")
	pair := l.pkgName(pkgKey{"machine pair", nil}, "ergoMachinePair")
	var b, vars strings.Builder
	b.WriteString("type " + pair + "[" + s + ", " + e + " any] struct{}\n\n")
	b.WriteString("func " + l.pkgName(pkgKey{"machine graphs", nil}, "ergoMachineGraph") + "[" + s + ", " + e + " any]() *" + l.rt() + ".MachineGraph {\n")
	b.WriteString("\tswitch any(" + pair + "[" + s + ", " + e + "]{}).(type) {\n")
	for _, g := range l.machineGraphs {
		if pairs[[2]string{g.state, g.signal}] != 1 {
			continue
		}
		b.WriteString("\tcase " + pair + "[" + g.state + ", " + g.signal + "]:\n\t\treturn &" + g.name + "\n")
		vars.WriteString("var " + g.name + " = " + g.value + "\n\n")
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")
	return b.String() + vars.String()
}

// variantName is the name a runner gives a value of the Go type t, the
// text the lowering writes for it in main.go: rt.TypeName of Go's runtime
// type string, as v1.VariantOf computes it at run time. ok is false for a
// type that runtimeSpelling does not spell.
func (l *lowerer) variantName(t string) (string, bool) {
	x, err := parser.ParseExpr(t)
	if err != nil {
		return "", false
	}
	paths := map[string]string{}
	for k, name := range l.pkgNames {
		if p, ok := k.obj.(string); ok && k.kind == "import" {
			paths[name] = p
		}
	}
	s, ok := runtimeSpelling(x, paths)
	if !ok {
		return "", false
	}
	return rtpkg.TypeName(s), true
}

// runtimeSpelling is Go's runtime type string of the type expression x of
// main.go, where paths gives each import name's path. It spells the forms
// goType writes (types.go): a predeclared type as Go names it, a type of
// package main as main.T, a type of another package with its import path
// (Go's runtime names the path inside a type's brackets; at the top
// level rt.TypeName drops the qualifier), type arguments apart by a bare
// comma, slices, arrays, maps, pointers, function types and the structs of
// tuples. ok is false for any other form: an interface, a channel, a
// struct with an embedded field or a tag, which no value type lowers to.
func runtimeSpelling(x ast.Expr, paths map[string]string) (string, bool) {
	list := func(fl []*ast.Field, sep string, named bool) (string, int, bool) {
		var out []string
		for _, f := range fl {
			s, ok := runtimeSpelling(f.Type, paths)
			if !ok || named && (len(f.Names) == 0 || f.Tag != nil) {
				return "", 0, false
			}
			if named {
				for _, n := range f.Names {
					out = append(out, n.Name+" "+s)
				}
				continue
			}
			for range max(len(f.Names), 1) {
				out = append(out, s)
			}
		}
		return strings.Join(out, sep), len(out), true
	}
	elem := func(prefix string, y ast.Expr) (string, bool) {
		s, ok := runtimeSpelling(y, paths)
		return prefix + s, ok
	}
	switch x := x.(type) {
	case *ast.Ident:
		if _, ok := types.Universe.Lookup(x.Name).(*types.TypeName); ok {
			return x.Name, true
		}
		return "main." + x.Name, true
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		if !ok || paths[pkg.Name] == "" {
			return "", false
		}
		return paths[pkg.Name] + "." + x.Sel.Name, true
	case *ast.IndexExpr:
		return runtimeSpelling(&ast.IndexListExpr{X: x.X, Indices: []ast.Expr{x.Index}}, paths)
	case *ast.IndexListExpr:
		head, ok := runtimeSpelling(x.X, paths)
		args := make([]string, len(x.Indices))
		for i, a := range x.Indices {
			s, ok2 := runtimeSpelling(a, paths)
			ok = ok && ok2
			args[i] = s
		}
		return head + "[" + strings.Join(args, ",") + "]", ok
	case *ast.StarExpr:
		return elem("*", x.X)
	case *ast.ArrayType:
		if x.Len == nil {
			return elem("[]", x.Elt)
		}
		n, ok := x.Len.(*ast.BasicLit)
		if !ok {
			return "", false
		}
		return elem("["+n.Value+"]", x.Elt)
	case *ast.MapType:
		k, ok := runtimeSpelling(x.Key, paths)
		v, ok2 := elem("]", x.Value)
		return "map[" + k + v, ok && ok2
	case *ast.FuncType:
		params, _, ok := list(x.Params.List, ", ", false)
		var rs []*ast.Field
		if x.Results != nil {
			rs = x.Results.List
		}
		results, n, ok2 := list(rs, ", ", false)
		switch {
		case n == 1:
			results = " " + results
		case n > 1:
			results = " (" + results + ")"
		}
		return "func(" + params + ")" + results, ok && ok2
	case *ast.StructType:
		fields, n, ok := list(x.Fields.List, "; ", true)
		if n == 0 {
			return "struct {}", ok
		}
		return "struct { " + fields + " }", ok
	}
	return "", false
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
// that gives every state None (no timer). A timer the after function arms
// with an In that is not positive is a defect that names the machine, the
// state and the duration (fs2; runtime.md, Machines and actors 9), so no
// runner that calls this After loops at one instant.
func (fc *funcCtx) machineAfter(conv check.Conversion, site syntax.Node) string {
	if after := conv.Table["After"]; after != nil {
		return fc.positiveTimers(conv, after, site)
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

// positiveTimers is the after function's value, wrapped so that a Some
// whose Timeout's In is not positive panics with v1.MachineTimerNotPositive
// (fs2). Go's time.NewTicker panics on a non-positive period for the same
// reason: such a timer fires at once, forever.
func (fc *funcCtx) positiveTimers(conv check.Conversion, after *check.Func, site syntax.Node) string {
	sig := after.Sig
	if len(sig.Params) != 1 || len(sig.Results) != 1 {
		ice(site, "the after function %s is not func(S) Option[Timeout[E]]", after.Name)
	}
	machine := "machine"
	if n, ok := conv.From.(*check.Named); ok {
		machine = n.Obj.Name
	}
	some, _ := optionVariants(sig.Results[0], site)
	g := fc.funcValue(after, sig, site, nil)
	gv, s, o, err, t := fc.scope.alloc("after"), fc.scope.alloc("s"), fc.scope.alloc("o"), fc.scope.alloc("err"), fc.scope.alloc("t")
	return fc.envClosure(callerEnv, "env", site, func(env string) ([]string, string, string) {
		ind := strings.Repeat("\t", fc.depth+1)
		body := ind + gv + " := " + g + "\n" +
			ind + o + ", " + err + " := " + gv + "(" + env + ", " + s + ")\n" +
			ind + "if " + t + ", ok := " + o + ".(" + fc.goType(some) + "); ok && " + t + ".Value.In <= 0 {\n" +
			ind + "\tpanic(" + fc.l.rtRef("MachineTimerNotPositive") + "(" + strconv.Quote(machine) + ", " + s + ", " + t + ".Value.In))\n" +
			ind + "}\n" +
			ind + "return " + o + ", " + err + "\n"
		return []string{s + " " + fc.goType(sig.Params[0].Type)}, fc.valueResults(sig.Results[0]), body
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
	l.analyzeValues(x, l.info)
	val := fc.assigned(x)
	l.addInit(x, x, fc.out.String(), []string{initial + " = " + val})
	l.machineInits = append(l.machineInits, machineInitial{pkg: td.tn.Pkg, x: x})
}

// machineInitial is a machine's initial value and the package it is declared in.
type machineInitial struct {
	pkg *check.Package
	x   syntax.Expr
}
