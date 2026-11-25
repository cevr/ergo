package lower

// IR builder: with A, …, Z { block } (G6). The emitter's with (calls.go):
// each operand of a named context copies the environment, builds the
// context's providers into the copy by the context's builder, and runs
// what it holds against the copy, with no closure (rows.md, With 7 and 8).
// The block's tail is the with's value. See ir.go.

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// isWith reports a with expression: a call of kind CallWith.
func (b *irBuilder) isWith(x syntax.Expr) (*syntax.CallExpr, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	c := b.l.info.Calls[call]
	return call, c != nil && c.Kind == check.CallWith
}

// withStmt is the with x; ret says its block's tail is the function's
// result, to (when set) the local it yields into. A with whose block
// passes an untried failure on, other than into a try, a catch or the
// function's result, and a context value's stay the emitter's.
func (b *irBuilder) withStmt(x *syntax.CallExpr, ret bool, to *yieldTo) []*irStmt {
	w := b.l.info.Withs[x]
	if w == nil || len(x.Args) == 0 || x.Trailing == nil {
		irNo("with form")
	}
	if b.env < 0 {
		irNo("with in a pure function")
	}
	if ret && to == nil && owningChain(w) {
		// An owning with ends its scope after its block (EndWith), so
		// its block yields into locals and the function returns them
		// after it, as the emitter's withExpr yields into res.
		return b.owningRet(x)
	}
	// A with whose block's tail fails passes the failure on: as the
	// function's result (ret: the tail returns it, ir_fail.go's ret), or
	// into the error local of a try or a catch around it (failWith).
	if b.fc.fallibleExpr(x) && !(ret && to == nil) && (to == nil || !to.fails) {
		irNo("with whose block fails")
	}
	return b.withOperand(x, 0, w, ret, to)
}

// failWith is the fallible with x under try or catch (bind, ir_fail.go):
// the emitter's withExpr with an error destination. Its value local (-1:
// none) and its error local are declared first; the block's tail puts
// the value in the one and its untried failure in the other (failYield).
// A try inside the block leaves the function, as the emitter's does: a
// with is no closure.
func (b *irBuilder) failWith(x *syntax.CallExpr) (pre []*irStmt, v, e int) {
	p := b.pos(x, x.Pos())
	e = b.temp("err", "error", p)
	pre = []*irStmt{{K: "var", Pos: p, Local: e}}
	v = -1
	t := b.l.info.Types[x]
	if t != nil && !isNoValue(t) && !b.fc.noValue(x) {
		v = b.temp("v", b.goType(t, x), p)
		pre = append(pre, &irStmt{K: "var", Pos: p, Local: v})
	}
	return append(pre, b.withStmt(x, false, &yieldTo{local: v, t: t, fails: true, err: e})...), v, e
}

// failYield is x, the tail of a fallible with's block under try or catch:
// a fallible call's value and error go into to's locals, any other value
// into its value local.
func (b *irBuilder) failYield(x syntax.Expr, to *yieldTo, p irPos) []*irStmt {
	if _, conv := b.l.info.Implicit[x]; conv {
		irNo("with whose failing tail converts")
	}
	if b.isFailure(x) {
		irNo("with whose block ends in a failure value")
	}
	call := b.fallibleCall(x)
	if call == nil {
		if to.local < 0 {
			return b.discard(x)
		}
		pre, val := b.value(x)
		return append(pre, &irStmt{K: "set", Pos: p, Local: to.local, X: b.assignedTo(x, val, to.t)})
	}
	pre, v, e := b.bind(call)
	if to.local >= 0 {
		if v < 0 {
			irNo("with value of a call without one")
		}
		val := b.assignedTo(x, &irExpr{K: "local", Pos: p, I: v}, to.t)
		pre = append(pre, &irStmt{K: "set", Pos: p, Local: to.local, X: val})
	}
	return append(pre, &irStmt{K: "set", Pos: p, Local: to.err, X: &irExpr{K: "local", Pos: p, I: e}})
}

// owningChain reports a with any operand of which owns resources.
func owningChain(w *check.With) bool {
	for ; w != nil; w = w.Next {
		if ownsResources(w) {
			return true
		}
	}
	return false
}

// owningRet is the owning with x as the function's result: its value
// (withValue), or under a failure row its value and error (failWith),
// returned after its scope ended, as the emitter returns its withExpr's
// operand.
func (b *irBuilder) owningRet(x *syntax.CallExpr) []*irStmt {
	p := b.pos(x, x.Pos())
	if !b.fc.fallibleExpr(x) {
		return b.ret(x, p) // ir_fail.go: the with's value (withValue)
	}
	if b.law != nil {
		irNo("with that owns resources as a law's failing result")
	}
	pre, v, e := b.failWith(x)
	pre = append(pre, b.propagate(e, p))
	if v < 0 || b.result == nil {
		return append(pre, b.formed(&irStmt{K: "ret", Pos: p}, "unit"))
	}
	val := b.assignedTo(x, &irExpr{K: "local", Pos: p, I: v}, b.result) // ir_union.go
	return append(pre, b.formed(&irStmt{K: "ret", Pos: p, X: val}, "val"))
}

func (b *irBuilder) withOperand(x *syntax.CallExpr, i int, w *check.With, ret bool, to *yieldTo) []*irStmt {
	arg := x.Args[i]
	if (w.Next == nil) != (i == len(x.Args)-1) {
		irNo("with operands without their With facts")
	}
	switch {
	case w.Value != nil:
		irNo("with of a context owner")
	case w.Installs != nil:
		return b.installWith(x, i, w, arg, ret, to) // ir_contextvalue.go
	case len(w.Contexts) == 0:
		irNo("with form")
	case w.Contexts[0].Lit == nil && !b.l.declaredContext(w.Contexts[0]):
		irNo("with of an undeclared context")
	case w.Contexts[0].TestAmbient != nil && b.loops > 0:
		// Go defers once per iteration and the IR decoder refuses a
		// defer in a loop (ergoir deferStmt).
		irNo("with of a context that needs TestAmbient in a loop")
	}
	// The with lifetime owner (the emitter's with, calls.go): a with that
	// acquires a resource opens its own scope on the frame's, or on the
	// next owning with's out (OpenWith).
	owning := ownsResources(w)
	if owning && (b.frame == 0 || b.exits) {
		irNo("with that owns resources outside a frame scope")
	}
	lit := w.Contexts[0].Lit != nil // an inline or a local context
	p := b.pos(arg, arg.Pos())
	env := b.envValue()
	// env2 := *env; envP := &env2: the copy the block runs against (the
	// emitter's `env2 := *env` and `(&env2)`); a named context's builder
	// builds its providers into it, an inline or a local context's are
	// built in place (publish).
	cp := b.temp("env", env, p)
	ptr := b.temp("envp", "*"+env, p)
	out := []*irStmt{
		{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}},
		{K: "def", Pos: p, Local: ptr, X: &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: cp}}},
	}
	if ta := w.Contexts[0].TestAmbient; ta != nil {
		out = append(out, b.testAmbient(ptr, ta, p, arg)...)
	}
	if tl := w.Contexts[0].TestLoopback; tl != nil {
		value := &irExpr{K: "lit", Pos: p, T: b.l.helper("TestLoopback")}
		out = append(out, &irStmt{K: "store", Pos: p, X: &irExpr{K: "field", Pos: p, Name: b.l.envField(tl), X: &irExpr{K: "local", Pos: p, I: ptr}}, Y: &irExpr{K: "toiface", Pos: p, T: b.goType(tl, arg), X: value}})
	}
	ws := -1
	savedWith := b.with
	defer func() { b.with = savedWith }()
	if owning {
		ws = b.temp("ws", b.spelling("*"+b.l.rt()+".Scope"), p)
		out = append(out, &irStmt{K: "def", Pos: p, Local: ws, X: &irExpr{K: "mcall", Pos: p, Name: "OpenWith", X: b.frameArg(p)}})
		if b.l.holdsRows() {
			// The innermost owning with, for a held context (inforce.go).
			field := &irExpr{K: "field", Pos: p, Name: envWith, X: &irExpr{K: "local", Pos: p, I: ptr}}
			out = append(out, &irStmt{K: "store", Pos: p, X: field, Y: &irExpr{K: "local", Pos: p, I: ws}})
		}
		// The block's tasks and the providers' callbacks take the with's
		// scope (frameArg; the emitter's frame.with).
		b.with = ws + 1
	}
	if lit {
		b.l.localContext(w.Contexts[0], arg)
		out = append(out, b.publish(w, ptr, b.env, ws, arg)...)
	} else {
		build := b.l.contextBuilder(w, arg)
		call := &irExpr{K: "call", Pos: p, Name: build, Args: []*irExpr{{K: "local", Pos: p, I: ptr}}}
		if owning {
			// The owning builder takes the environment outside the with,
			// where its releases run, and the with's scope, which it
			// registers them in (emitContext).
			call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: b.env}, &irExpr{K: "local", Pos: p, I: ws})
		}
		if fallibleBuild(w) {
			// with try W: the builder returns the build's failure, which
			// leaves the with before its block runs (the emitter's with).
			e := b.temp("err", "error", p)
			out = append(out, &irStmt{K: "def", Pos: p, Local: e, X: call}, b.propagate(e, p))
		} else {
			out = append(out, &irStmt{K: "expr", Pos: p, X: call})
		}
	}
	rest := b.withRest(x, i, w, ptr, ret, to)
	out = append(out, rest...)
	if owning && !irListEnds(rest) {
		// The block's normal end ends the with's scope; every other exit
		// leaves it to the frame's End (v1.Scope.close).
		errArg := &irExpr{K: "zero", Pos: p, T: "*error"}
		if to != nil && to.fails {
			errArg = b.addrOf(to.err, p)
		}
		end := b.pos(x.Trailing, x.Trailing.Rbrace)
		out = append(out, &irStmt{K: "expr", Pos: end, X: &irExpr{K: "mcall", Pos: end, Name: "EndWith", X: &irExpr{K: "local", Pos: end, I: ws}, Args: []*irExpr{errArg}}})
	}
	return out
}

// withRest is what operand i of the with x holds, against the environment
// ptr points at: the next operand, or the block.
func (b *irBuilder) withRest(x *syntax.CallExpr, i int, w *check.With, ptr int, ret bool, to *yieldTo) []*irStmt {
	saved := b.env
	b.env = ptr
	defer func() { b.env = saved }()
	if w.Next != nil {
		return b.withOperand(x, i+1, w.Next, ret, to)
	}
	// The block's scope releases what it owns at its end (the emitter's
	// scoped; PORT-CALLS-2: a Listener and a Task let in main's with).
	return b.scoped(x.Trailing, b.tailTo(x.Trailing.List, ret && to == nil, to), b.pos(x.Trailing, x.Trailing.Rbrace)) // ir_own.go
}

type opsWitnessKey struct {
	adapter string
	idx     int
}

// opsWitnesses builds the witness field entries (w0, w1, ...) for an
// operations conversion adapter, matching calls.go's operations.
func (b *irBuilder) opsWitnesses(conv check.Conversion, key string, x syntax.Expr, p irPos) ([]string, []*irExpr) {
	to, ok := conv.To.(*check.Named)
	if !ok {
		irNo("operations conversion with witnesses")
	}
	iface, ok := to.Underlying().(*check.Interface)
	if !ok {
		irNo("operations conversion with witnesses")
	}
	var keys []string
	var elems []*irExpr
	wi := 0
	for _, op := range iface.Methods {
		f := conv.Table[op.Name]
		if f == nil || len(witnessParams(f.Sig)) == 0 {
			continue
		}
		inst := conv.Instances[op.Name]
		if inst == nil || inst.Func != f {
			irNo("operations conversion with witnesses")
		}
		open := false
		for _, t := range inst.TypeArgs {
			if len(freeTypeParams(t)) > 0 {
				open = true
			}
		}
		if open {
			ws := b.passedWitnesses(f, inst, witnessParams(f.Sig), p)
			for _, w := range ws {
				keys = append(keys, fmt.Sprintf("w%d", wi))
				elems = append(elems, w)
				wi++
			}
			continue
		}
		texts := b.fc.witnessArgs(f, inst, x)
		for _, text := range texts {
			field := fmt.Sprintf("w%d", wi)
			wkey := pkgKey{"ops witness", opsWitnessKey{adapter: key, idx: wi}}
			wname, ok := b.l.pkgNames[wkey]
			if !ok {
				irNo("operations conversion witness without its reserved name")
			}
			if b.st.census == nil && !b.l.texts[wkey] {
				b.l.texts[wkey] = true
				b.l.gen.WriteString("var " + wname + " = " + text + "\n\n")
			}
			keys = append(keys, field)
			elems = append(elems, &irExpr{K: "global", Pos: p, Name: wname})
			wi++
		}
	}
	return keys, elems
}

// opsConv is e, the value of x, through the operations conversion conv:
// the emitter's adapter (operations, sealedOperations), &A{v: e, env: env}
// (plus any table-function witness fields w0, w1, ...) for a value of the
// program whose type's functions provide the operations, &A{v: e} for a
// sealed std value, as the operation type. The emitter declares the
// adapter; the IR builds its literal. A value whose type is already an
// interface is itself; a Script's is &A{v: e}. A type parameter's and a
// generic adapter's stay the emitter's.
func (b *irBuilder) opsConv(conv check.Conversion, e *irExpr, x syntax.Expr) *irExpr {
	l := b.l
	if to, ok := conv.To.(*check.Named); ok && to.Obj.Pkg.Host() && !goAdapterConv(conv) {
		// A Go value into a Go interface (a go.Ref[strings.Reader] as an
		// io.Reader): Go's own implicit conversion, as the emitter's
		// convert leaves it, spelled for the decoder, which inserts none
		// (IR-GOCALLS-4). A Go interface value into another is Go's
		// assignment the decoder does not spell.
		if _, iface := underEqual(conv.From).(*check.Interface); iface {
			irNo("Go interface value into another Go interface")
		}
		b.hostImported(to) // ir_govar.go
		t := b.goType(conv.To, x)
		b.keepImport("var _ " + b.fc.goType(conv.To)) // main.go imports its package
		return &irExpr{K: "toiface", Pos: e.Pos, T: t, X: e}
	}
	if goAdapterConv(conv) {
		// A value of the program into a Go interface: goInterface's
		// adapter, whose methods are Go callbacks with Go's results and
		// error (io.Writer's Write), the callback shape callbackShape
		// refuses. The operations adapter the rest of this function
		// builds has Ergo's methods (the task first), which do not
		// implement the Go interface, so the IR compile refused the body
		// (LIB-ON-IR: TestGoCallsErgo's Count).
		irNo("value of the program into a Go interface")
	}
	if check.ScriptOf(conv.From) != nil {
		// A Script's adapter holds the Script alone (the emitter's
		// scriptOperations): &A{v: e}, as the operation type.
		b.fc.operations(conv, "_", x)
		key, _, ok := l.adapterName(conv)
		name := l.adapters[key]
		if !ok || name == "" {
			irNo("operations conversion of a script")
		}
		lit := &irExpr{K: "lit", Pos: e.Pos, T: name, Keys: []string{"v"}, Elems: []*irExpr{e}}
		return &irExpr{K: "toiface", Pos: e.Pos, T: b.goType(conv.To, x), X: &irExpr{K: "addr", Pos: e.Pos, X: lit}}
	}
	from, _ := conv.From.(*check.Named)
	sealed := from != nil && l.runtimeNamed(from, x)
	if !sealed && from != nil && from.Underlying() != nil {
		if _, ops := from.Underlying().(*check.Interface); ops {
			if rowWidening(conv) {
				name := b.fc.widenOps(conv, x)
				p := e.Pos
				lit := &irExpr{K: "lit", Pos: p, T: name, Keys: []string{"v"}, Elems: []*irExpr{e}}
				return &irExpr{K: "toiface", Pos: p, T: b.goType(conv.To, x), X: &irExpr{K: "addr", Pos: p, X: lit}}
			}
			return e
		}
	}
	if tp, _ := paramSource(conv.From); tp != nil {
		irNo("operations conversion of a type parameter")
	}
	if len(convParams(conv)) > 0 {
		irNo("generic operations conversion")
	}
	hasWitnesses := false
	for _, f := range conv.Table {
		if f != nil && len(witnessParams(f.Sig)) > 0 {
			hasWitnesses = true
			break
		}
	}
	if hasWitnesses && sealed {
		irNo("operations conversion with witnesses")
	}
	// The emitter declares the adapter (its text is the emitter's form
	// of this same value, unused here).
	b.fc.operations(conv, "_", x)
	key, _, ok := l.adapterName(conv)
	name := l.adapters[key]
	if !ok || name == "" {
		irNo("operations conversion without an adapter")
	}
	p := e.Pos
	lit := &irExpr{K: "lit", Pos: p, T: name, Keys: []string{"v"}, Elems: []*irExpr{e}}
	if !sealed {
		env := &irExpr{K: "zero", Pos: p, T: b.envPointer()}
		if b.env >= 0 {
			env = &irExpr{K: "local", Pos: p, I: b.env}
		}
		lit.Keys, lit.Elems = append(lit.Keys, "env"), append(lit.Elems, env)
	}
	if hasWitnesses {
		wk, we := b.opsWitnesses(conv, key, x, p)
		lit.Keys, lit.Elems = append(lit.Keys, wk...), append(lit.Elems, we...)
	}
	return &irExpr{K: "toiface", Pos: p, T: b.goType(conv.To, x), X: &irExpr{K: "addr", Pos: p, X: lit}}
}

// publish builds the inline or local context's providers of w into the
// environment the local ptr points at (the emitter's buildProviders and
// publishProvider): each acquisition's provider, in the checker's order,
// against the environment outside the with (outer) for an entry of the
// literal, the one being built for an embedded named context's, then its
// value, converted, into each capability's field. In an owning with (ws
// its scope's local, else -1) each provider it does not lend is built
// inside one masked acquisition of the scope (acquire).
func (b *irBuilder) publish(w *check.With, ptr, outer, ws int, site syntax.Node) []*irStmt {
	local := map[*check.ContextEntry]bool{}
	var lexical func(*check.Context)
	lexical = func(c *check.Context) {
		if c.Lit == nil {
			return
		}
		for _, e := range c.Entries {
			if e.Embedded != nil {
				lexical(e.Embedded)
			} else {
				local[e] = true
			}
		}
	}
	lexical(w.Contexts[0])
	// The package of each entry, by the context that holds it (the
	// emitter's owners).
	pkgs := map[*check.ContextEntry]*check.Package{}
	var owners func(*check.Context)
	owners = func(c *check.Context) {
		for _, e := range c.Entries {
			if _, ok := pkgs[e]; ok {
				continue
			}
			pkgs[e] = c.Package()
			if e.Embedded != nil {
				owners(e.Embedded)
			}
		}
	}
	owners(w.Contexts[0])
	var out []*irStmt
	for _, a := range w.Acquired {
		e := a.Entry
		// An entry of an embedded named context is built against the
		// environment being built (the emitter's buildProvider, not
		// local); one of another package's context reads that package's
		// facts, which the builder does not switch to.
		src := outer
		if !local[e] {
			if pk := pkgs[e]; pk == nil || pk.Info != b.l.info {
				irNo("with of a context that embeds a named one")
			}
			src = ptr
		}
		p := b.pos(e.Provider, e.Provider.Pos())
		if ws >= 0 && !a.Lent {
			// A lent provider is a place: its value cannot fail, and the
			// with registers no release for it (R11), so it is published
			// in place like a plain one (the emitter's buildProvider).
			out = append(out, b.acquire(a, ptr, src, outer, ws, p)...)
			continue
		}
		stmts, _ := b.publishOne(a, ptr, src, p, false)
		out = append(out, stmts...)
	}
	return out
}

// acquire is the emitter's withClose on the IR: the acquisition a built
// and published inside the function literal that v1.Scope.WithAcquire
// runs masked, which returns its release, the provider's checked Close
// on the with's own copy against the environment outside the with
// (outer), or nil for a provider with none. A failed acquisition ends the
// with's scope and fails the frame. A provider whose expression leaves
// the function (a try, a catch, a return, a break or a continue: the
// emitter's acquireFlow) stays the emitter's.
func (b *irBuilder) acquire(a *check.Acquisition, ptr, src, outer, ws int, p irPos) []*irStmt {
	if leavesFrame(a.Entry.Provider) {
		irNo("with whose provider leaves the function")
	}
	if a.Releases() && a.Drop != nil {
		irNo("with that releases a provider by its parts")
	}
	body, val := b.publishOne(a, ptr, src, p, a.Releases())
	end := p
	release := &irExpr{K: "zero", Pos: p, T: "func()"}
	if a.Releases() {
		// The release is closeCall's on a slot that points at the copy,
		// read as the copy itself (onValue): the literal captures the
		// value, as the emitter's providerRelease does, and takes its
		// address only for a Close that takes a pointer.
		t := b.l.info.Types[a.Entry.Provider]
		// slot is no local: onValue replaces each read of it.
		const slot = -2
		call, ok := b.closeCall(irSlot{t: t, d: &check.Drop{Type: t, Close: a.Close}, site: a.Entry.Provider}, outer, slot, p) // ir_own.go
		if !ok {
			irNo("with that releases a provider by a Close the IR does not call")
		}
		release = &irExpr{K: "func", Pos: p, T: "func()", End: &end, Body: []*irStmt{{K: "expr", Pos: p, X: onValue(call, slot, val)}}}
	}
	body = append(body, &irStmt{K: "ret", Pos: p, X: release})
	open := &irExpr{K: "func", Pos: p, T: "func() (func(), error)", End: &end, Body: body}
	e := b.temp("acquireErr", "error", p)
	k := "fail"
	if !b.fc.frame.fallible {
		k = "panic"
	}
	wsL := &irExpr{K: "local", Pos: p, I: ws}
	return []*irStmt{
		{K: "def", Pos: p, Local: e, X: &irExpr{K: "mcall", Pos: p, Name: "WithAcquire", X: wsL, Args: []*irExpr{open}}},
		{K: "if", Pos: p, End: p, X: b.notNil(e, p), Then: []*irStmt{
			{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "EndWith", X: &irExpr{K: "local", Pos: p, I: ws}, Args: []*irExpr{b.addrOf(e, p)}}},
			b.formed(&irStmt{K: k, Pos: p, X: &irExpr{K: "local", Pos: p, I: e}}, "fail:error"),
		}},
	}
}

// onValue is x with the pointer local slot replaced by the local val it
// points at: *slot is val, slot is &val.
func onValue(x *irExpr, slot, val int) *irExpr {
	if x == nil {
		return nil
	}
	if x.K == "deref" && x.X != nil && x.X.K == "local" && x.X.I == slot {
		return &irExpr{K: "local", Pos: x.Pos, I: val}
	}
	if x.K == "local" && x.I == slot {
		return &irExpr{K: "addr", Pos: x.Pos, X: &irExpr{K: "local", Pos: x.Pos, I: val}}
	}
	y := *x
	y.X, y.Y = onValue(x.X, slot, val), onValue(x.Y, slot, val)
	y.Args, y.Elems = onValues(x.Args, slot, val), onValues(x.Elems, slot, val)
	if x.Body != nil {
		y.Body = make([]*irStmt, len(x.Body))
		for i, st := range x.Body {
			c := *st
			c.X, c.Y = onValue(st.X, slot, val), onValue(st.Y, slot, val)
			if st.Then != nil || st.Else != nil || st.Body != nil {
				irNo("with that releases a provider by a Close the IR does not call")
			}
			y.Body[i] = &c
		}
	}
	return &y
}

func onValues(xs []*irExpr, slot, val int) []*irExpr {
	if xs == nil {
		return nil
	}
	out := make([]*irExpr, len(xs))
	for i, x := range xs {
		out[i] = onValue(x, slot, val)
	}
	return out
}

// leavesFrame reports an expression that can leave the function it is
// in, outside a function literal of its own.
func leavesFrame(x syntax.Expr) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.TryExpr, *syntax.CatchExpr, *syntax.ReturnStmt, *syntax.DieStmt, *syntax.BranchStmt:
			found = true
		}
		return !found
	})
	return found
}

// publishOne builds the provider of the acquisition a against the
// environment src and stores its value into each of its capabilities'
// fields of the environment ptr points at. own spills the value into
// the with's own copy, its local val, which its release closes.
func (b *irBuilder) publishOne(a *check.Acquisition, ptr, src int, p irPos, own bool) (out []*irStmt, val int) {
	e := a.Entry
	val = -1
	saved, savedRaw := b.env, b.raw
	b.env = src
	if own {
		// The with's own copy is the provider's value as it is (the
		// emitter's publishProvider), which its Close takes: each
		// capability's conversion applies to it below.
		b.raw = e.Provider
	}
	pre, value := b.value(e.Provider)
	b.env, b.raw = saved, savedRaw
	out = append(out, pre...)
	// The checker may record the entry's conversion at the provider too
	// (Info.Implicit), which value applied: the value is the capability's
	// already.
	_, converted := b.l.info.Implicit[e.Provider]
	converted = converted && !own
	if converted && len(a.Caps) > 1 {
		irNo("context entry of several capabilities with a conversion")
	}
	if len(a.Caps) > 1 || own {
		t := b.l.info.Types[e.Provider]
		if t == nil {
			irNo("context entry without a type")
		}
		val = b.temp("prov", b.goType(t, e.Provider), p)
		out = append(out, &irStmt{K: "def", Pos: p, Local: val, X: value})
		value = &irExpr{K: "local", Pos: p, I: val}
	}
	for _, i := range a.Caps {
		capT, ok := e.Caps[i].(*check.Named)
		if !ok {
			irNo("context entry key that is not a named capability")
		}
		conv := check.Conversion{}
		if i < len(e.Conversions) {
			conv = e.Conversions[i]
		}
		v := value
		switch {
		case converted:
		case conv.Kind == 0:
			// A value of a type that implements the capability's Go
			// interface goes into the field as Go assigns it.
			if pt := b.l.info.Types[e.Provider]; pt != nil && b.goType(pt, e.Provider) != b.goType(capT, e.Provider) {
				v = &irExpr{K: "toiface", Pos: p, T: b.goType(capT, e.Provider), X: value}
			}
		case conv.Kind == check.ConvOperations:
			b.env = src
			v = b.opsConv(conv, value, e.Provider)
			b.env = saved
		default:
			irNo("context entry conversion %d", conv.Kind)
		}
		field := &irExpr{K: "field", Pos: p, Name: b.l.envField(capT), X: &irExpr{K: "local", Pos: p, I: ptr}}
		out = append(out, &irStmt{K: "store", Pos: p, X: field, Y: v})
	}
	return out, val
}

// testAmbient is the emitter's testAmbientOpen (calls.go) on the IR: the
// runner's provider of the test's ambient ta, over a fresh folder of the
// case, goes into the environment ptr, and its end, deferred, removes the
// folder when the test's function returns (G10; rows.md, Test's ambient).
// The end is a method of the harness's provider (tests.go): the IR decoder
// defers a call of the package or a method, not a runtime function.
func (b *irBuilder) testAmbient(ptr int, ta *check.Named, p irPos, site syntax.Node) []*irStmt {
	at := b.l.helper("TestAmbient")
	amb := b.temp("ambient", at, p)
	open := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "OpenTestDirectory"}
	return []*irStmt{
		{K: "def", Pos: p, Local: amb, X: &irExpr{K: "lit", Pos: p, T: at, Keys: []string{"dir"}, Elems: []*irExpr{open}}},
		{K: "defer", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "end", X: &irExpr{K: "local", Pos: p, I: amb}}},
		{K: "store", Pos: p, X: &irExpr{K: "field", Pos: p, Name: b.l.envField(ta), X: &irExpr{K: "local", Pos: p, I: ptr}}, Y: &irExpr{K: "toiface", Pos: p, T: b.goType(ta, site), X: &irExpr{K: "local", Pos: p, I: amb}}},
	}
}

// withValue is the with x in a value position: a local of its type,
// declared first, and the with yielding into it (ifValue).
func (b *irBuilder) withValue(x *syntax.CallExpr) ([]*irStmt, *irExpr) {
	t, ok := b.l.info.Types[x]
	if !ok || b.fc.noValue(x) {
		irNo("with without a value")
	}
	p := b.pos(x, x.Pos())
	i := b.temp(fmt.Sprintf("with%d", b.next), b.goType(t, x), p)
	pre := append([]*irStmt{{K: "var", Pos: p, Local: i}}, b.withStmt(x, true, &yieldTo{local: i, t: t})...)
	return pre, &irExpr{K: "local", Pos: p, I: i}
}
