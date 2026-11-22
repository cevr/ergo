package lower

// IR builder: machine values (FLIP F7 wave 2, "machine value"; MACHINE-IR).
//
// A machine declaration's Go form is its plan, a zero-size struct
// (machinePlan, machine.go), so a value of it is the literal X{} and the
// type needs no admission beyond its name. Where a machine value converts
// to the prelude Machine or InputMachine (ConvMachine), the IR calls one
// generated function per machine, ergoMachineX(v X) Machine[…]. Its body
// is IR (MACHINE-IR), the emitter's conversion (expr.go convert) written
// as a record literal of function literals:
//
//	Initial: v.Initial()                       (or Start: func(env, in) { return start(in) })
//	Step:    func(env, s, e) { return step(s, e) }
//	After:   func(env, s) { o := after(s); if o is Some w && w.Value.In <= 0 { panic(…) }; return o }
//	         (or func(env, s) { return None })
//	Work:    func(env, s) { o := work(s); if o is Some w { return Some{w.Value.Start} }; return None }
//	         (or func(env, s) { return None })
//	Exit:    as Work, the exit function's action as it is, when the prelude
//	         Machine has the field (FK9-g1c's exit entry)
//
// A Machine field the builder does not set is refused, never left zero.
// The lowerer writes the function's head into the generated part, with no
// body (as every IR body's). None of the literals captures the caller's
// frame (each takes its environment as a parameter, callerEnv), so one
// package function serves every site.
//
// Prior art: Gleam and ReScript compile a record of functions to a host
// object built once; Elixir's gen_statem callbacks are module functions,
// not closures over the caller.

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// machineValueKey is the package-scope key of the machine tn's generated
// value function.
func machineValueKey(tn *check.TypeName) pkgKey { return pkgKey{"machine value", tn} }

// reserveMachineValue names, in the naming pass, the generated value
// function of the machine tn (machineValueFunc). Reserving one the
// lowering does not emit only renames a local.
func (l *lowerer) reserveMachineValue(tn *check.TypeName) {
	l.pkgName(machineValueKey(tn), "ergoMachine"+tn.Name)
}

// admitMachine admits a machine declaration's type: its plan, a Go struct
// with no fields and no type parameters (the grammar gives a machine none).
func (b *irBuilder) admitMachine(t *check.Named) {
	if len(t.TArgs) > 0 {
		irNo("machine value with type arguments")
	}
}

// funcField reports that a record field's type t is a function type,
// which a record's admission skips: the IR never spells a field's type
// (the decoder reads it from the Go struct), and every place that would
// spell or call a function value (a local of it, a call of a value) admits
// or refuses the function type there. So a record of functions, such as
// the prelude Machine a machine value converts to, moves through an IR
// body as a whole value.
func funcField(t check.Type) bool {
	_, ok := t.(*check.Signature)
	return ok
}

// funcSpelling is the length of the function type punctuation that starts
// s, or 0: a function type stands in a Go type spelling as a type argument
// (the prelude Machine's Work field, Option[func(*Env, func(*Env, E)
// (struct{}, error)) (struct{}, error)]), and the decoder reads it there
// (ergoir_types.go typeList keeps a list's commas inside its parentheses).
// Its names are spelled as every name is (spelling).
func funcSpelling(s string) int {
	// ") " is a function type's parameters closed before a single result.
	for _, tok := range []string{"func(", "struct{}", ") (", ") ", ")"} {
		if strings.HasPrefix(s, tok) {
			return len(tok)
		}
	}
	return 0
}

// machineLit is the IR of the machine literal x, X{}: the plan's zero
// value.
func (b *irBuilder) machineLit(x *syntax.CompositeLit, lit *irExpr) *irExpr {
	if len(x.Elts) > 0 {
		irNo("machine literal with fields")
	}
	return lit
}

// machineValue is e, a value of a machine declaration, converted to the
// prelude Machine or InputMachine (ConvMachine): a call of the machine's
// generated value function.
func (b *irBuilder) machineValue(conv check.Conversion, e *irExpr, site syntax.Node) *irExpr {
	n, ok := conv.From.(*check.Named)
	if !ok || b.l.typeDecl(n.Obj, site).kind != kindMachine {
		irNo("machine conversion from a non-machine")
	}
	b.admitMachine(n)
	return &irExpr{K: "call", Pos: e.Pos, Name: b.l.machineValueFunc(conv, n, b.st, site), Args: []*irExpr{e}}
}

// machineValueFunc is the name of the machine n's generated value
// function, func(v X) To, built once as an IR function (machineBody). A
// second conversion of n to another Machine type is refused: the checker
// gives a machine one Machine type (its step's signature fixes O, F and
// R). A refusal of the body refuses the body that converts, which keeps
// the emitter's conversion; the function is not recorded, so the next
// conversion tries again.
func (l *lowerer) machineValueFunc(conv check.Conversion, n *check.Named, st *irState, site syntax.Node) string {
	key := machineValueKey(n.Obj)
	name := l.pkgName(key, "ergoMachine"+n.Obj.Name)
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	to := fc.goType(conv.To)
	if have, ok := l.machines[key]; ok {
		if have != to {
			irNo("machine value at a second Machine type")
		}
		return name
	}
	v := scope.alloc("v")
	b := st.newBuilder(fc, nil) // an adapter: no source statements
	b.names = newNamer(scope)
	b.next = 1 // v, the plan value
	decl := n.Obj.Decl
	p := b.pos(decl, decl.Pos())
	b.fn = &irFunc{Name: name, Pos: p, End: p}
	b.fn.Body = irUseLocals([]*irStmt{{K: "ret", Pos: p, X: b.machineRecord(conv, n, p, decl)}}) // ir_end.go
	if l.machines == nil {
		l.machines = map[pkgKey]string{}
	}
	l.machines[key] = to
	l.gen.WriteString("func " + name + "(" + v + " " + fc.goType(conv.From) + ") " + to + "\n\n")
	if st.census == nil {
		st.out.Funcs = append(st.out.Funcs, *b.fn)
		st.report.IR = append(st.report.IR, name)
	}
	return name
}

// machineRecord is the Machine or InputMachine conv converts the plan
// value (local 0) to, at p: the emitter's convert, field by field.
func (b *irBuilder) machineRecord(conv check.Conversion, n *check.Named, p irPos, site syntax.Node) *irExpr {
	to, ok := conv.To.(*check.Named)
	if !ok {
		irNo("machine conversion to an unnamed type")
	}
	td := b.l.typeDecl(to.Obj, site)
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(conv.To, site)}
	field := func(name string, e *irExpr) {
		g, ok := td.fieldName[name]
		if !ok {
			irNo("machine conversion without the field %s", name)
		}
		lit.Keys, lit.Elems = append(lit.Keys, g), append(lit.Elems, e)
	}
	step := conv.Table["Step"]
	if step == nil {
		irNo("machine conversion without a step")
	}
	if start := conv.Table["Start"]; start != nil {
		sig := b.fc.machineField(conv, "Start", site)
		field("Start", b.machineFunc(sig, p, site, func(env int, ps []int) []*irStmt {
			if len(start.Sig.Params) != 1 {
				irNo("machine start with several parameters")
			}
			return []*irStmt{{K: "ret", Pos: p, X: b.machineCall(start, env, []*irExpr{{K: "local", Pos: p, I: ps[0]}}, p, site)}}
		}))
	} else {
		field("Initial", &irExpr{K: "mcall", Pos: p, Name: "Initial", X: &irExpr{K: "local", Pos: p, I: 0}})
	}
	stepSig, ok := recordFieldType(conv.To, "Step").(*check.Signature)
	if !ok || len(stepSig.Params) != 2 {
		irNo("machine conversion to a Step of another form")
	}
	field("Step", b.machineFunc(stepSig, p, site, func(env int, ps []int) []*irStmt {
		args := []*irExpr{{K: "local", Pos: p, I: ps[0]}, {K: "local", Pos: p, I: ps[1]}}
		return []*irStmt{{K: "ret", Pos: p, X: b.machineCall(step, env, args, p, site)}}
	}))
	field("After", b.machineAfter(conv, n, p, site))
	field("Work", b.machineOption(conv, "Work", p, site, func(t check.Type, x *irExpr) *irExpr {
		return b.recordField(t, x, "Start", site) // machines.Work's Start
	}))
	if _, ok := td.fieldName["Exit"]; ok {
		// The state's exit action (FK9-g1c, runtime.md 12a): the exit
		// function's action as it is.
		field("Exit", b.machineOption(conv, "Exit", p, site, func(_ check.Type, x *irExpr) *irExpr { return x }))
	}
	// A field of the Machine this builder does not know is refused, never
	// left zero: a nil function field would panic in the runner.
	if s, ok := to.Underlying().(*check.Struct); !ok || len(s.Fields) != len(lit.Keys) {
		irNo("machine conversion to a Machine with other fields")
	}
	return lit
}

// machineFunc is a function literal of the type of the Machine field sig,
// its environment and parameters fresh locals: body builds its statements.
func (b *irBuilder) machineFunc(sig *check.Signature, p irPos, site syntax.Node, body func(env int, params []int) []*irStmt) *irExpr {
	lit := &irExpr{K: "func", Pos: p, T: b.sigType(sig, site), End: &p} // ir_closure.go
	env := b.temp("env", b.envPointer(), p)
	lit.Params = []int{env}
	var ps []int
	for _, prm := range sig.Params {
		ps = append(ps, b.temp("p", b.goType(prm.Type, site), p))
	}
	lit.Params = append(lit.Params, ps...)
	lit.Body = body(env, ps)
	return lit
}

// machineCall is the call of the machine's entry function f (its step,
// start, after or work) with args, as a call of a declared function in
// an IR body writes it (ir_call.go): the environment first, after a
// method's receiver, when f takes one.
func (b *irBuilder) machineCall(f *check.Func, env int, args []*irExpr, p irPos, site syntax.Node) *irExpr {
	fd := b.l.useFunc(f, site)
	switch {
	case fd.extern != "":
		irNo("machine entry that is an extern")
	case len(f.Sig.TParams) > 0:
		irNo("generic machine entry")
	case len(b.l.ownedParams(f)) > 0:
		irNo("call that passes owned values")
	case len(witnessParams(f.Sig)) > 0:
		irNo("call that passes witnesses")
	}
	if !fd.envFree {
		at := 0
		if fd.method != nil {
			at = 1
		}
		args = append(args[:at:at], append([]*irExpr{{K: "local", Pos: p, I: env}}, args[at:]...)...)
	}
	if fd.method != nil {
		return &irExpr{K: "mcall", Pos: p, Name: fd.name, X: args[0], Args: args[1:]}
	}
	return &irExpr{K: "call", Pos: p, Name: fd.name, Args: args}
}

// machineNone is the value None of the Option instance t.
func (b *irBuilder) machineNone(t check.Type, p irPos, site syntax.Node) *irExpr {
	_, none := optionVariants(t, site)
	return b.variantInto(none, t, &irExpr{K: "zero", Pos: p, T: b.goType(none, site)}, site) // ir_union.go
}

// machineSome is a match on the local o, of the Option instance t, whose
// one arm binds the Some to a fresh local and runs then on it (the
// emitter's optionSomeIf).
func (b *irBuilder) machineSome(t check.Type, o int, p irPos, site syntax.Node, then func(w int) []*irStmt) *irStmt {
	some, _ := optionVariants(t, site)
	gs := b.goType(some, site)
	w := b.temp("w", gs, p)
	if fu := b.flatOf(t); fu != nil {
		if fu.niche {
			irNo("Option's pointer niche")
		}
		k := fu.tagOf(some)
		body := append([]*irStmt{{K: "def", Pos: p, Local: w, X: b.narrow(fu, o, k, p, site)}}, then(w)...)
		return &irStmt{K: "switch", Pos: p, End: p, X: b.tagOf(o, p), Cases: []*irCase{{Pos: p, Vals: []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}}, Body: body}}}
	}
	body := append([]*irStmt{{K: "def", Pos: p, Local: w, X: &irExpr{K: "assert", Pos: p, T: gs, X: &irExpr{K: "local", Pos: p, I: o}}}}, then(w)...)
	return &irStmt{K: "tswitch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: o}, Cases: []*irCase{{Pos: p, Types: []string{gs}, Body: body}}}
}

// recordField is the field name of the record value x of the named type
// t.
func (b *irBuilder) recordField(t check.Type, x *irExpr, name string, site syntax.Node) *irExpr {
	n, ok := t.(*check.Named)
	if !ok {
		irNo("field %s of an unnamed type", name)
	}
	g, ok := b.l.typeDecl(n.Obj, site).fieldName[name]
	if !ok {
		irNo("field %s of %s", name, n.Obj.Name)
	}
	return &irExpr{K: "field", Pos: x.Pos, Name: g, X: x}
}

// recordFieldType is the type of the field name of the record type t.
func recordFieldType(t check.Type, name string) check.Type {
	if n, ok := t.(*check.Named); ok {
		if s, ok := n.Underlying().(*check.Struct); ok {
			for _, f := range s.Fields {
				if f.Name == name {
					return f.Type
				}
			}
		}
	}
	irNo("field %s of %s", name, t)
	return nil
}

// machineAfter is the After of the Machine (the emitter's machineAfter and
// positiveTimers): the after function's Option, after a Some whose
// Timeout's In is not positive panics with the defect that names the
// machine, the state and the duration (fs2); or None for every state.
func (b *irBuilder) machineAfter(conv check.Conversion, n *check.Named, p irPos, site syntax.Node) *irExpr {
	sig := b.fc.machineField(conv, "After", site)
	after := conv.Table["After"]
	return b.machineFunc(sig, p, site, func(env int, ps []int) []*irStmt {
		if after == nil {
			return []*irStmt{{K: "ret", Pos: p, X: b.machineNone(sig.Results[0], p, site)}}
		}
		if len(after.Sig.Params) != 1 || len(after.Sig.Results) != 1 || b.l.funcDecl(after).fallible {
			irNo("machine after entry of another form")
		}
		if everyOption(after.Sig.Results[0]) {
			// A machines.Every timer: its own form (the emitter's
			// everyTimers; LY-FIX: read as a Timeout, its Option did not
			// type).
			return b.everyAfter(sig, after, n, env, ps[0], p, site)
		}
		ot := after.Sig.Results[0]
		o := b.temp("o", b.goType(ot, site), p)
		out := []*irStmt{{K: "def", Pos: p, Local: o, X: b.machineCall(after, env, []*irExpr{{K: "local", Pos: p, I: ps[0]}}, p, site)}}
		some, _ := optionVariants(ot, site)
		timeout := recordFieldType(some, "Value")
		inType := recordFieldType(timeout, "In")
		st := sig.Params[0].Type
		gst := b.timerImport(st, site)
		out = append(out, b.machineSome(ot, o, p, site, func(w int) []*irStmt {
			in := func() *irExpr {
				return b.recordField(timeout, b.recordField(some, &irExpr{K: "local", Pos: p, I: w}, "Value", site), "In", site)
			}
			return []*irStmt{b.timerGuard(n, gst, ps[0], in, inType, p)}
		}))
		return append(out, &irStmt{K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: o}})
	})
}

// everyAfter is the body of the After of a machine whose after function
// gives a machines.Every (the emitter's everyTimers, FKG1), on the
// environment env and the state s: the after function's Option; on a
// Some, a timer whose In is not positive panics as machineAfter's does,
// the reading is time.Clock's when the runner calls it for a timer that
// fires (v1.MachineFiringNow) and the zero time otherwise, and the
// Timeout carries In and the Signal made from the reading; None
// otherwise. A Signal that fails returns its error (with the value's zero,
// which no runner reads beside an error). The fields are read by their
// shape: Some's one field, the Every's function field (Signal) and its
// other field (In), which the field's Timeout has too.
func (b *irBuilder) everyAfter(field *check.Signature, after *check.Func, n *check.Named, env, s int, p irPos, site syntax.Node) []*irStmt {
	asig := after.Sig
	clock, isClock := check.EveryClock(asig).(*check.Named)
	ot := asig.Results[0]
	every, isEvery := ot.(*check.Named).TArgs[0].(*check.Named)
	if !isClock || !isEvery {
		irNo("machine after entry of another form")
	}
	st, isStruct := every.Underlying().(*check.Struct)
	if !isStruct || len(st.Fields) != 2 {
		irNo("machine after entry of another form")
	}
	var inName, sigName string
	var at, inType check.Type
	for _, f := range st.Fields {
		if fs, isSig := f.Type.(*check.Signature); isSig && len(fs.Params) == 1 {
			sigName, at = f.Name, fs.Params[0].Type
		} else {
			inName, inType = f.Name, f.Type
		}
	}
	if sigName == "" || inName == "" {
		irNo("machine after entry of another form")
	}
	rt := field.Results[0]
	some, _ := optionVariants(ot, site)
	valueName, _ := onlyField(some) // ir_intrinsic.go
	rsome, _ := optionVariants(rt, site)
	_, timeout := onlyField(rsome)
	gst := b.timerImport(asig.Params[0].Type, site)
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	o := b.temp("o", b.goType(ot, site), p)
	out := []*irStmt{{K: "def", Pos: p, Local: o, X: b.machineCall(after, env, []*irExpr{local(s)}, p, site)}}
	out = append(out, b.machineSome(ot, o, p, site, func(w int) []*irStmt {
		ev := func() *irExpr { return b.recordField(some, local(w), valueName, site) }
		in := func() *irExpr { return b.recordField(every, ev(), inName, site) }
		when := b.temp("at", b.goType(at, site), p)
		task := func() *irExpr { return &irExpr{K: "field", Pos: p, Name: envTask, X: local(env)} }
		now := &irExpr{K: "mcall", Pos: p, Name: "Now", X: &irExpr{K: "field", Pos: p, Name: b.l.envField(clock), X: local(env)}, Args: []*irExpr{task()}}
		e := b.temp("e", b.goType(recordFieldType(timeout, sigName), site), p)
		errv := b.temp("err", "error", p)
		signal := &irExpr{K: "vcall", Pos: p, X: b.recordField(every, ev(), sigName, site), Args: []*irExpr{local(env), local(when)}}
		tval := &irExpr{K: "lit", Pos: p, T: b.goType(timeout, site), Keys: []string{b.goField(timeout, inName, site), b.goField(timeout, sigName, site)}, Elems: []*irExpr{in(), local(e)}}
		sval := &irExpr{K: "lit", Pos: p, T: b.goType(rsome, site), Keys: []string{b.goField(rsome, valueName, site)}, Elems: []*irExpr{tval}}
		return []*irStmt{
			b.timerGuard(n, gst, s, in, inType, p),
			{K: "var", Pos: p, Local: when},
			{K: "if", Pos: p, End: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "MachineFiringNow", Args: []*irExpr{task()}}, Then: []*irStmt{{K: "set", Pos: p, Local: when, X: now}}},
			{K: "def2", Pos: p, Local: e, Val: &errv, X: signal},
			{K: "if", Pos: p, End: p, X: b.notNil(errv, p), Then: []*irStmt{{K: "fail", Pos: p, X: local(errv)}}},
			{K: "ret", Pos: p, X: b.variantInto(rsome, rt, sval, site)},
		}
	}))
	return append(out, &irStmt{K: "ret", Pos: p, X: b.machineNone(rt, p, site)})
}

// goField is the Go name of the field name of the record type t.
func (b *irBuilder) goField(t check.Type, name string, site syntax.Node) string {
	return b.recordField(t, &irExpr{K: "zero"}, name, site).Name
}

// timerImport is the Go type of the state st, whose defect an After's
// timerGuard raises: rtRef, because the defect writes the state with
// rt.TypedText, so main.go imports the gofmt leaf (v1.NeedsHostText).
func (b *irBuilder) timerImport(st check.Type, site syntax.Node) string {
	gst := b.goType(st, site)
	b.keepImport("var _ = " + b.l.rtRef(timerPanic) + "[" + gst + "]")
	return gst
}

// timerGuard is `if In <= 0 { panic }` of an After (fs2): the defect
// names the machine n, the state's local s (its Go type gst) and the
// duration in() of the type inType.
func (b *irBuilder) timerGuard(n *check.Named, gst string, s int, in func() *irExpr, inType check.Type, p irPos) *irStmt {
	cond := &irExpr{K: "bin", Pos: p, Op: "<=", X: in(), Y: b.constant(inType, constant.MakeInt64(0), nil, p)}
	raise := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: timerPanic, V: gst, Args: []*irExpr{
		irStringConst(p, "string", n.Obj.Name), {K: "local", Pos: p, I: s}, in()}}
	return &irStmt{K: "if", Pos: p, End: p, X: cond, Then: []*irStmt{{K: "expr", Pos: p, X: raise}}}
}

// timerPanic is the runtime/v1 body an After's IR calls on a timer whose
// In is not positive.
const timerPanic = "MachineTimerPanic"

// machineOption is the Machine's field name, Work or Exit (the emitter's
// machineWork and machineExit): the entry function's Option, its Some's
// Value through pick (Work's Start; Exit's action as it is), as the
// field's Option, or None for every state when the machine has no entry.
func (b *irBuilder) machineOption(conv check.Conversion, name string, p irPos, site syntax.Node, pick func(t check.Type, x *irExpr) *irExpr) *irExpr {
	sig := b.fc.machineField(conv, name, site)
	entry := conv.Table[name]
	return b.machineFunc(sig, p, site, func(env int, ps []int) []*irStmt {
		rt := sig.Results[0]
		if entry == nil {
			return []*irStmt{{K: "ret", Pos: p, X: b.machineNone(rt, p, site)}}
		}
		if len(entry.Sig.Params) != 1 || len(entry.Sig.Results) != 1 || b.l.funcDecl(entry).fallible {
			irNo("machine %s entry of another form", name)
		}
		ot := entry.Sig.Results[0]
		o := b.temp("o", b.goType(ot, site), p)
		out := []*irStmt{{K: "def", Pos: p, Local: o, X: b.machineCall(entry, env, []*irExpr{{K: "local", Pos: p, I: ps[0]}}, p, site)}}
		some, _ := optionVariants(ot, site)
		w := recordFieldType(some, "Value")
		rsome, _ := optionVariants(rt, site)
		out = append(out, b.machineSome(ot, o, p, site, func(wl int) []*irStmt {
			v := pick(w, b.recordField(some, &irExpr{K: "local", Pos: p, I: wl}, "Value", site))
			val := &irExpr{K: "lit", Pos: p, T: b.goType(rsome, site)}
			val = b.recordFieldLit(rsome, val, "Value", v, site)
			return []*irStmt{{K: "ret", Pos: p, X: b.variantInto(rsome, rt, val, site)}}
		}))
		return append(out, &irStmt{K: "ret", Pos: p, X: b.machineNone(rt, p, site)})
	})
}

// recordFieldLit adds the field name, of value x, to lit, a literal of the
// record type t.
func (b *irBuilder) recordFieldLit(t check.Type, lit *irExpr, name string, x *irExpr, site syntax.Node) *irExpr {
	n, ok := t.(*check.Named)
	if !ok {
		irNo("field %s of an unnamed type", name)
	}
	g, ok := b.l.typeDecl(n.Obj, site).fieldName[name]
	if !ok {
		irNo("field %s of %s", name, n.Obj.Name)
	}
	lit.Keys, lit.Elems = append(lit.Keys, g), append(lit.Elems, x)
	return lit
}
