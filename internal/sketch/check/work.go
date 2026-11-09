package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The work of a machine's states (MU4; runtime.md, Machines and actors
// 12). A machine declaration's `work: w` entry names a package function
// w(s S) Option[machines.Work[E, R]]: the runner starts the work of each entry
// after the commit and interrupts it when the entry ends. Its capability
// row R is charged to the machine: the Machine value's R holds it beside
// the step's (machineValue).
//
// A Work literal of the forms machines.Work{Run: f, OnSuccess: g, OnFailure: h}
// and machines.Work{Listen: f, OnFailure: h} builds the record's Start (the work
// rule, effect-machine's task handler table as one checker rule):
//   - Run is a function func() T fails F needs R; its result becomes a
//     signal through OnSuccess func(t T) E. OnSuccess may be left out
//     when T is assignable to E (the result is a signal).
//   - Listen is a function func(send func(e E)) fails F needs R: a
//     listener sends as it goes, and its end sends nothing.
//   - A typed failure of Run or Listen becomes a signal through OnFailure
//     func(f U) E, where U is F's one type or the row value of F. It may be
//     left out when every type of F is a variant of E.
//   - A map left out that the types need is S108, and its text names the
//     map and the types that need it.
// An interruption is not a failure (rows.md, Catch 6), so the end of an
// entry sends nothing; a defect is not one either, and it ends the actor.
//
// Prior art: effect-machine's .task(state, run, {onSuccess, onFailure})
// requires each handler by TypeScript overloads, whose miss is TS2769 "No
// overload matches this call" (docs/async-work.md reads it in about 40
// lines); XState's invoke has onDone and onError, both optional and
// untyped; Elm's Task.attempt takes one function of a Result. Ergo states
// the table once and names the missing map.

// WorkLit is a checked Work literal of the work rule's forms, which the
// lowering builds Start from.
type WorkLit struct {
	// Run is the one-shot function, or nil for a listener.
	Run syntax.Expr
	// Listen is the listener, or nil for a one-shot work.
	Listen syntax.Expr
	// OnSuccess and OnFailure are the maps, nil when left out.
	OnSuccess, OnFailure syntax.Expr
	// Result is Run's result type T; nil for a listener.
	Result Type
	// Failures is Run's or Listen's error row F.
	Failures *Row
	// Failure is the type OnFailure takes: F's one type, or F's row value.
	Failure Type
	// Signal is the machine's signal E; Send is the type of send.
	Signal Type
	Send   *Signature
}

// workPath is the std package that declares Work. Work is not a prelude
// name: a prelude name is one no program may declare, and a program's own
// Work (a type or a function) is common.
const workPath = "machines"

// isWorkType reports whether tn is std machines.Work.
func isWorkType(tn *TypeName) bool {
	return tn != nil && tn.Name == "Work" && tn.Pkg != nil && tn.Pkg.std && tn.Pkg.Path == workPath
}

// isStdWork reports whether t is an instance of std machines.Work.
func isStdWork(t Type) bool {
	n, ok := t.(*Named)
	return ok && isWorkType(n.origin().Obj)
}

// workLitForm reports whether x is a Work literal of the rule's forms: its
// written type names std machines.Work (qualified, or Work inside the
// machines package) and it sets Run or Listen.
func (c *checker) workLitForm(x *syntax.CompositeLit) bool {
	if x.Type == nil || x.Base != nil {
		return false
	}
	base := x.Type
	if ix, ok := base.(*syntax.IndexExpr); ok {
		base = ix.X
	}
	var tn *TypeName
	switch b := base.(type) {
	case *syntax.Ident:
		tn, _ = c.lookup(b.Name).(*TypeName)
	case *syntax.SelectorExpr:
		// The literal's type resolves the name again (and marks the
		// import used); here only its object matters.
		id, ok := b.X.(*syntax.Ident)
		if !ok {
			return false
		}
		if pn, ok := c.lookup(id.Name).(*PkgName); ok && !pn.broken && pn.Pkg != nil {
			tn, _ = pn.Pkg.Scope.names[b.Sel.Name].(*TypeName)
		}
	}
	if !isWorkType(tn) {
		return false
	}
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if k, ok := kv.Key.(*syntax.Ident); ok && (k.Name == "Run" || k.Name == "Listen") {
				return true
			}
		}
	}
	return false
}

// workLit checks a Work literal of the rule's forms against hint, the
// Work[E, R] the context expects, or the literal's written type.
func (c *checker) workLit(x *syntax.CompositeLit, hint Type) operand {
	mark := c.undiagSeq
	t := hint
	if _, written := x.Type.(*syntax.IndexExpr); written || !isStdWork(hint) {
		t = c.literalType(x, hint)
	}
	if t == nil || isInvalid(t) || !isStdWork(t) {
		if t != nil && !isInvalid(t) {
			c.errorf(x, CodeType, "a Work literal with Run or Listen needs its machines.Work[E, R] from its context, or written type arguments (runtime.md, Machines and actors 12)")
		}
		for _, e := range x.Elts {
			c.strayValue(workValue(e), mark, true)
		}
		return bad(x)
	}
	n := t.(*Named)
	signal := n.TArgs[0]
	uses := newRow()
	if ra, ok := n.TArgs[1].(*RowArg); ok && ra.Row != nil {
		uses = ra.Row
	}
	st, _ := n.Underlying().(*Struct)
	var send *Signature
	if st != nil {
		if f, _ := st.field("Start"); f != nil {
			if sig, ok := f.Type.(*Signature); ok && len(sig.Params) == 1 {
				send, _ = sig.Params[0].Type.(*Signature)
			}
		}
	}
	if send == nil {
		return bad(x)
	}
	lit := &WorkLit{Signal: signal, Send: send}
	keys := map[string]syntax.Expr{}
	ok := true
	for _, e := range x.Elts {
		kv, isKV := e.(*syntax.KeyValueExpr)
		if !isKV {
			c.errorf(e, CodeType, "a Work literal is keyed: Run or Listen, OnSuccess and OnFailure (runtime.md, Machines and actors 12)")
			c.strayValue(e, mark, false)
			ok = false
			continue
		}
		k, isID := kv.Key.(*syntax.Ident)
		switch {
		case !isID || k.Name != "Run" && k.Name != "Listen" && k.Name != "OnSuccess" && k.Name != "OnFailure":
			c.errorf(kv.Key, CodeType, "a Work literal with Run or Listen sets only Run or Listen, OnSuccess and OnFailure; Start is built from them (runtime.md, Machines and actors 12)")
			c.strayValue(kv.Value, mark, true)
			ok = false
		case keys[k.Name] != nil:
			c.errorf(k, CodeType, "%s is set twice", k.Name)
			c.strayValue(kv.Value, mark, false)
			ok = false
		default:
			keys[k.Name] = kv.Value
		}
	}
	lit.Run, lit.Listen, lit.OnSuccess, lit.OnFailure = keys["Run"], keys["Listen"], keys["OnSuccess"], keys["OnFailure"]
	if lit.Run != nil && lit.Listen != nil {
		c.errorf(x, CodeType, "a Work literal sets Run or Listen, not both: Run gives one result, Listen sends as it goes (runtime.md, Machines and actors 12)")
		ok = false
	}
	if lit.Listen != nil && lit.OnSuccess != nil {
		c.errorf(lit.OnSuccess, CodeType, "a Listen work has no result, so it has no OnSuccess: it sends each signal itself (runtime.md, Machines and actors 12)")
		c.strayValue(lit.OnSuccess, mark, true)
		lit.OnSuccess = nil
		ok = false
	}
	// The work function: its result and its error row decide the maps.
	var sig *Signature
	fn := lit.Run
	if fn == nil {
		fn = lit.Listen
	}
	typed := false // fn is a function of the right shape: its rows decide the maps
	if fn != nil && ok {
		// No expected type: the function's result and its error row are
		// what the rule reads, so they are its own, written or inferred
		// from its body; its parameter types are written.
		op := c.valueExpr(fn, nil)
		sig, _ = op.typ.(*Signature)
		switch {
		case op.invalid():
			ok = false
		case sig == nil || len(sig.TParams) > 0:
			c.errorf(fn, CodeType, "%s is %s, not a function (runtime.md, Machines and actors 12)", workKey(lit), op.typ)
			ok = false
		case lit.Run != nil && (len(sig.Params) != 0 || len(sig.Results) != 1):
			c.errorf(fn, CodeType, "a Work's Run is a function func() T with one result, which becomes the signal; it has the type %s (a work that sends as it goes is a Listen) (runtime.md, Machines and actors 12)", op.typ)
			ok = false
		case lit.Listen != nil && (len(sig.Params) != 1 || len(sig.Results) != 0):
			c.errorf(fn, CodeType, "a Work's Listen is a function func(send %s) with no result; it has the type %s (runtime.md, Machines and actors 12)", send, op.typ)
			ok = false
		default:
			errs := sig.Errors
			if errs == nil {
				errs = newRow()
			}
			full := &Signature{Params: sig.Params, Results: sig.Results, Fallible: sig.Fallible, Errors: errs, Uses: uses}
			if lit.Listen != nil {
				full.Params = []*Var{{Name: "send", Type: send}}
			}
			if !c.assign(&op, full, "the work's "+workKey(lit)) {
				ok = false
			} else {
				typed = true
			}
			lit.Failures = errs.known(c.types)
			if lit.Run != nil {
				lit.Result = sig.Results[0]
			}
		}
	} else if fn == nil && ok {
		c.errorf(x, CodeType, "a Work literal sets Run or Listen (runtime.md, Machines and actors 12)")
		ok = false
	}
	// OnSuccess: required unless the result is a signal.
	if lit.OnSuccess != nil {
		if lit.Result != nil {
			want := &Signature{Params: []*Var{{Name: "t", Type: lit.Result}}, Results: []Type{signal}, Errors: newRow(), Uses: uses}
			op := c.valueExpr(lit.OnSuccess, want)
			if !c.assign(&op, want, "the work's OnSuccess") {
				ok = false
			}
		} else {
			c.strayValue(lit.OnSuccess, mark, false)
		}
	} else if typed && lit.Result != nil {
		if !c.assignableTo(lit.Result, signal) {
			c.errorf(x, CodeType, "this Work's Run gives %s, which is not a signal %s: write OnSuccess: func(t %s) %s, which makes the signal of the result (runtime.md, Machines and actors 12)", c.localTypeText(lit.Result), c.localTypeText(signal), c.localTypeText(lit.Result), c.localTypeText(signal))
			ok = false
		}
	}
	// OnFailure: required unless every failure is a signal.
	if lit.Failures != nil && !lit.Failures.Empty() {
		elems := lit.Failures.Elems()
		lit.Failure = c.rowValue(lit.Failures)
		if len(elems) == 1 {
			lit.Failure = elems[0]
		}
	}
	if lit.OnFailure != nil {
		if lit.Failure != nil {
			want := &Signature{Params: []*Var{{Name: "f", Type: lit.Failure}}, Results: []Type{signal}, Errors: newRow(), Uses: uses}
			op := c.valueExpr(lit.OnFailure, want)
			if !c.assign(&op, want, "the work's OnFailure") {
				ok = false
			}
		} else {
			if typed {
				c.errorf(lit.OnFailure, CodeType, "this Work's %s fails with nothing, so OnFailure is never called; leave it out (runtime.md, Machines and actors 12)", workKey(lit))
				ok = false
			}
			c.strayValue(lit.OnFailure, mark, false)
		}
	} else if typed && lit.Failure != nil {
		var outside []string
		for _, f := range lit.Failures.Elems() {
			if !c.rowInUnion(f, signal) {
				outside = append(outside, c.localTypeText(f))
			}
		}
		if len(outside) > 0 {
			param := "f " + c.localTypeText(lit.Failure)
			if len(lit.Failures.Elems()) > 1 {
				param = "f" // a row value: the literal takes its type from OnFailure's
			}
			c.errorf(x, CodeType, "this Work's %s can fail with %s, which %s not a signal %s: write OnFailure: func(%s) %s, which makes the signal of a failure (runtime.md, Machines and actors 12)", workKey(lit), strings.Join(outside, ", "), isAre(len(outside)), c.localTypeText(signal), param, c.localTypeText(signal))
			ok = false
		}
	}
	if ok && c.silent == 0 {
		if c.pkg.Info.Works == nil {
			c.pkg.Info.Works = map[*syntax.CompositeLit]*WorkLit{}
		}
		c.pkg.Info.Works[x] = lit
	}
	return operand{mode: modeValue, typ: t, expr: x}
}

// assignableTo reports whether a value of type v may be assigned to t.
func (c *checker) assignableTo(v, t Type) bool {
	a, _ := c.assignability(v, t)
	return a.ok()
}

// rowInUnion reports whether the error type f is a signal of the union
// signal: assignable to it as a variant.
func (c *checker) rowInUnion(f, signal Type) bool {
	return c.assignableTo(f, signal)
}

func workKey(lit *WorkLit) string {
	if lit.Listen != nil {
		return "Listen"
	}
	return "Run"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// workValue is a literal element's value, for a refusal route.
func workValue(e syntax.Expr) syntax.Expr {
	if kv, ok := e.(*syntax.KeyValueExpr); ok {
		return kv.Value
	}
	return e
}

// checkMachineWork checks a machine's `work: w` entry: w names a package
// function from the state to Option[machines.Work[E, R]] with no rows of its own;
// R is the work's capability row, charged to the machine (runtime.md,
// Machines and actors 12). It returns w, or nil when the entry is refused.
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
	if isExtern(f) {
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return nil
	}
	if f.Sig == nil {
		return nil
	}
	r := c.machineWorkRow(f.Sig, state, signal)
	if r == nil {
		c.errorf(id, CodeType, "the work %s of machine %s must have the type %s, with no error or capability row of its own: R is the work's capability row, which the machine needs (runtime.md, Machines and actors 12)", id.Name, tn.Name, want)
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
// Option[machines.Work[E, R]] with no type parameters, else nil.
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
	w, ok := o.TArgs[0].(*Named)
	if !ok || !isStdWork(w) || len(w.TArgs) != 2 || !c.identical(w.TArgs[0], signal) {
		return nil
	}
	ra, ok := w.TArgs[1].(*RowArg)
	if !ok {
		return nil
	}
	if ra.Row == nil {
		return newRow()
	}
	return ra.Row
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
