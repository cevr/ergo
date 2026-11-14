package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Effects run through providers (ORACLE O1). A capability is an operation
// type a function's row needs; the provider a `with` binds runs each of
// its operations (types.md, Capabilities 2; rows.md, With). The
// evaluator holds no host resource: a provider is an Ergo value (a
// record whose functions the checker's table names, or a native std
// object such as a sync.Ref), so running one changes only the
// evaluator's own values. A host provider reaches a host extern, which
// the evaluator never runs: that obligation is skipped at the call.

// capEnv is the capabilities bound at a point of the run, innermost
// first: each with pushes its bindings for its block, and a call passes
// its caller's to the callee (a function that needs a capability sees the
// provider of the nearest with around its call).
type capEnv struct {
	key  string
	op   Value // an *operation or an opObject
	next *capEnv
}

func (c *capEnv) lookup(key string) (Value, bool) {
	for ; c != nil; c = c.next {
		if c.key == key {
			return c.op, true
		}
	}
	return nil, false
}

// toOperation is v put into an operation type by conv: a provider value
// and the checked table of its operations. A value that is already an
// operation value or a native object keeps its own operations.
func (in *interp) toOperation(fr *frame, at syntax.Node, v Value, conv check.Conversion) (Value, *abrupt) {
	switch conv.Kind {
	case 0:
		return v, nil
	case check.ConvOperations:
		if check.ScriptOf(conv.From) != nil {
			return nil, fr.unsupported(at, "a testing.Script provider yet")
		}
		if _, ok := conv.From.(*check.Pointer); ok {
			return nil, fr.unsupported(at, "a pointer provider yet")
		}
		switch v.(type) {
		case *operation, opObject:
			if len(conv.Table) == 0 {
				return v, nil
			}
		}
		if ab := in.build(fr, at, 1, 2, 0); ab != nil {
			return nil, ab
		}
		return &operation{value: v, conv: conv, env: fr.env}, nil
	case check.ConvMachine:
		if p, ok := v.(*machinePlan); ok {
			return in.machineValue(fr, at, p, conv)
		}
		return nil, fr.unsupported(at, "a conversion of %s to a Machine yet", describe(v))
	}
	return nil, fr.unsupported(at, "a conversion to the operation type %s yet", conv.To)
}

// dispatch runs the operation name of recv, an operation value or a
// native std object, on args.
func (in *interp) dispatch(fr *frame, at *syntax.CallExpr, recv Value, name string, args []Value) (Value, *abrupt) {
	switch r := recv.(type) {
	case opObject:
		return r.opCall(in, fr, at, name, args)
	case *operation:
		fn := r.conv.Table[name]
		if fn == nil {
			if inner, ok := r.value.(*operation); ok {
				return in.dispatch(fr, at, inner, name, args)
			}
			if inner, ok := r.value.(opObject); ok {
				return in.dispatch(fr, at, inner, name, args)
			}
			ice("no checked method for operation %s", name)
		}
		if fn.Ptr {
			return nil, fr.unsupported(at, "a call that borrows its receiver yet")
		}
		var targs []rtype
		if inst := r.conv.Instances[name]; inst != nil {
			for _, t := range inst.TypeArgs {
				targs = append(targs, r.env.resolve(t))
			}
		}
		return in.callResolved(fr, at, fn, targs, append([]Value{r.value}, args...))
	}
	return nil, fr.unsupported(at, "an operation of %s, a provider the evaluator did not build", describe(recv))
}

// providerCall runs a capability's operation on the provider the nearest
// with binds (CallProviderOp).
func (in *interp) providerCall(fr *frame, x *syntax.CallExpr, c *check.Call) (Value, *abrupt) {
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		ice("a capability call with no selector")
	}
	key := typeKey(fr.rtypeOf(sel.X))
	op, ok := fr.caps.lookup(key)
	if !ok {
		return nil, fr.unsupported(x, "the capability %s, which no with of the run binds", fr.typeOf(sel.X))
	}
	// The interruption point of a capability call (runtime.md, I1 2):
	// before its operands, in an impure frame; a checked Close starts a
	// release instead.
	if fr.info.Closes[x] == nil {
		if ab := in.poll(fr, x); ab != nil {
			return nil, ab
		}
	}
	args := make([]Value, 0, len(x.Args))
	for _, a := range x.Args {
		v, ab := in.expr(fr, a)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	in.handParts(fr, x.Args...)
	return in.dispatch(fr, x, op, c.Op.Name, args)
}

// withExpr runs `with A, …, Z { block }` as its nesting (rows.md, With 7):
// each operand builds its providers and binds them for the operands
// after it and the block. The block yields the with's value by the tail
// rule; its failure is the with's.
func (in *interp) withExpr(fr *frame, x *syntax.CallExpr) (Value, *abrupt) {
	w := fr.info.Withs[x]
	if w == nil {
		ice("a with with no Withs entry")
	}
	if len(x.Args) == 0 || x.Trailing == nil {
		ice("a with without a context and a block")
	}
	saved := fr.caps
	defer func() { fr.caps = saved }()
	v, ab := in.withOperand(fr, x, 0, w)
	if ab != nil {
		return nil, ab
	}
	if v == nil {
		v = unit{}
	}
	return v, nil
}

func (in *interp) withOperand(fr *frame, x *syntax.CallExpr, i int, w *check.With) (Value, *abrupt) {
	arg := x.Args[i]
	if (w.Next == nil) != (i == len(x.Args)-1) {
		ice("a with of %d operands whose With.Next chain ends at operand %d", len(x.Args), i+1)
	}
	outer := fr.caps
	inner := func() (Value, *abrupt) {
		if w.Next != nil {
			return in.withOperand(fr, x, i+1, w.Next)
		}
		return in.scope(fr, x.Trailing, x.Trailing.List, false)
	}
	switch {
	case w.Value != nil:
		return in.bindContextValue(fr, arg, w.Value, inner)
	case w.Installs != nil:
		v, ab := in.expr(fr, arg)
		if ab != nil {
			return nil, ab
		}
		cv, ok := v.(*contextValue)
		if !ok {
			return nil, fr.unsupported(arg, "a Context value the evaluator did not build (%s)", describe(v))
		}
		for _, b := range cv.binds {
			fr.caps = &capEnv{key: b.key, op: b.op, next: fr.caps}
		}
		return inner()
	case len(w.Contexts) == 0:
		ice("a with operand with neither a context nor a context binding")
	}
	caps, built, ab := in.acquire(fr, arg, w)
	if ab != nil {
		return nil, ab
	}
	fr.caps = caps
	// An owning with is an owner of its own (runtime/v1 OpenWith): what
	// its block registers is released when the block ends, on every
	// exit, before the providers it built (own.go).
	var wo *owner
	for _, b := range built {
		if b.a.Releases() {
			fr.ownerOf()
			wo = &owner{}
			break
		}
	}
	prev := fr.own
	if wo != nil {
		fr.own = wo
	}
	v, ab := inner()
	fr.own = prev
	ab = in.releaseOwner(fr, wo, ab)
	// The block ends: the with releases what it built, in the
	// environment outside it (release.go).
	if ab = in.releaseAll(fr, arg, built, outer, ab); ab != nil {
		return nil, ab
	}
	return v, nil
}

// acquire builds the with's providers (With.Acquired), each once, in the
// checker's acquisition order, and returns the bindings of the block: the
// caller's, with each discharged capability bound to its entry's
// provider. An entry of an inline or a local context sees the bindings
// outside the with, and the enclosing frame's locals; an entry of a
// named context is evaluated in a frame of the context's package, and
// sees the providers built before it (rows.md, Context 3, 4 and 9).
//
// built lists the providers in acquisition order, for the with's
// releases (release.go). A failure, a defect or a stop while a provider is
// built releases the providers built before it, last first, before it
// leaves (resources.md R7, Context ownership 8).
func (in *interp) acquire(fr *frame, at syntax.Node, w *check.With) (caps *capEnv, built []acquired, ab *abrupt) {
	outer := fr.caps
	defer func() {
		if ab != nil {
			ab = in.releaseAll(fr, at, built, outer, ab)
			built = nil
		}
	}()
	lexical := map[*check.ContextEntry]bool{}
	pkgs := map[*check.ContextEntry]*check.Context{}
	var walk func(c *check.Context, lit bool)
	walk = func(c *check.Context, lit bool) {
		lit = lit && c.Lit != nil
		for _, e := range c.Entries {
			if _, seen := pkgs[e]; seen {
				continue
			}
			pkgs[e] = c
			if e.Embedded != nil {
				walk(e.Embedded, lit)
			} else if lit {
				lexical[e] = true
			}
		}
	}
	walk(w.Contexts[0], true)
	values := map[*check.ContextEntry]Value{}
	building := outer
	for _, a := range w.Acquired {
		e := a.Entry
		var v Value
		if lexical[e] {
			v, ab = in.rawExpr(fr, e.Provider)
		} else {
			ctx := pkgs[e]
			if ctx == nil || ctx.Decl == nil {
				return nil, built, fr.unsupported(at, "a provider of a context the evaluator cannot place yet")
			}
			cf := in.newFrame(ctx.Package(), ctx.Decl)
			cf.caps = building
			v, ab = in.rawExpr(cf, e.Provider)
		}
		if ab != nil {
			return nil, built, ab
		}
		// Built and registered in one step (R8): a later failure
		// releases it.
		values[e] = v
		built = append(built, acquired{a: a, v: v})
		for _, j := range a.Caps {
			op, ab := in.entryOperation(fr, at, e, j, v)
			if ab != nil {
				return nil, built, ab
			}
			building = &capEnv{key: typeKey(fr.rt(e.Caps[j])), op: op, next: building}
		}
	}
	caps = outer
	for i, cap := range w.Discharged {
		if i >= len(w.Entries) {
			ice("a with discharges %d capabilities with %d entries", len(w.Discharged), len(w.Entries))
		}
		e := w.Entries[i]
		v, ok := values[e]
		if !ok {
			ice("a with discharges %s by an entry it did not acquire", cap)
		}
		key := typeKey(fr.rt(cap))
		j := -1
		for k, c := range e.Caps {
			if typeKey(fr.rt(c)) == key {
				j = k
				break
			}
		}
		if j < 0 {
			ice("the entry for %s does not provide it", cap)
		}
		op, ab := in.entryOperation(fr, at, e, j, v)
		if ab != nil {
			return nil, built, ab
		}
		caps = &capEnv{key: key, op: op, next: caps}
	}
	return caps, built, nil
}

// entryOperation is the provider v of entry e put into its capability j.
func (in *interp) entryOperation(fr *frame, at syntax.Node, e *check.ContextEntry, j int, v Value) (Value, *abrupt) {
	conv := check.Conversion{}
	if j < len(e.Conversions) {
		conv = e.Conversions[j]
	}
	return in.toOperation(fr, at, v, conv)
}

// rawExpr evaluates x without the implicit conversion the checker records
// at it: a context entry's provider is converted once per capability it
// binds (ContextEntry.Conversions).
func (in *interp) rawExpr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if ab := in.charge(fr, x, 1); ab != nil {
		return nil, ab
	}
	return in.exprOf(fr, x)
}

// callValue calls a function value: a function literal's closure, in the
// frame that made it, or a declared function, as a call of it by name
// where the value is called.
func (in *interp) callValue(fr *frame, at syntax.Node, f Value, args []Value) (Value, *abrupt) {
	call, _ := at.(*syntax.CallExpr)
	switch f := f.(type) {
	case *closure:
		return in.callClosure(fr, call, f, args)
	case *funcRef:
		if call == nil && (f.fn.Decl == nil || f.fn.Decl.Body == nil || f.fn.Decl.Extern != nil || f.fn.Pkg.Host()) && nativeOf(f.fn) == nil {
			return nil, fr.unsupported(at, "a Go function (%s), even a pure one", f.fn.Name)
		}
		return in.callResolved(fr, call, f.fn, f.targs, args)
	case *nativeFunc:
		return f.call(in, fr, at, args)
	case seqSource:
		// A Seq a native made, called with its yield (seq.go).
		for {
			v, more, ab := f.next(in, fr, at)
			if ab != nil || !more {
				return unit{}, ab
			}
			goOn, ab := in.callValue(fr, at, args[0], []Value{v})
			if ab != nil {
				return nil, ab
			}
			if goOn != true {
				return unit{}, nil
			}
		}
	}
	return nil, fr.unsupported(at, "a call of %s as a function", describe(f))
}

// memberOf is the exported member name of the package p: the object its
// scope declares, which the package's own Defs and initializers use.
func (in *interp) memberOf(p *check.Package, name string) check.Object {
	if in.members == nil {
		in.members = map[*check.Package]map[string]check.Object{}
	}
	m := in.members[p]
	if m == nil {
		m = map[string]check.Object{}
		for _, o := range p.Exported() {
			switch o := o.(type) {
			case *check.Var:
				m[o.Name] = o
			case *check.Func:
				m[o.Name] = o
			}
		}
		in.members[p] = m
	}
	return m[name]
}

// funcRef is a declared function as a value: a package function, with its
// type arguments when it is a generic one's instance.
type funcRef struct {
	fn    *check.Func
	targs []rtype
}

// funcValue is the value of a name that resolves to the function fn: at
// is the name, or the instance expression of a generic one (Max[int]).
func (in *interp) funcValue(fr *frame, at syntax.Expr, fn *check.Func) (Value, *abrupt) {
	if fn.Ptr {
		return nil, fr.unsupported(at, "a function that borrows its receiver (%s) as a value yet", fn.Name)
	}
	ref := &funcRef{fn: fn}
	if len(fn.Sig.TParams) > 0 {
		inst := fr.info.Instances[at]
		if inst == nil || len(inst.Witnesses) > 0 {
			return nil, fr.unsupported(at, "a generic function (%s) as a value yet", fn.Name)
		}
		for _, t := range inst.TypeArgs {
			ref.targs = append(ref.targs, fr.rt(t))
		}
	}
	return ref, nil
}
