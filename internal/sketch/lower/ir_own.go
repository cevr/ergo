package lower

// IR builder: owned values in a static frame (FLIP F7 wave 2, FORK lane).
// See ir.go and static.go.
//
// A body whose plain frame takes the static release form (staticPlan,
// P-L8) keeps each owned value in a slot local and its presence in one
// bit of the live local, as the emitter's static form does. The IR body
// starts with one deferred call of the body's release helper, which
// releases the values whose bit is still set, last acquired first, and
// settles the exit (runtime/v1 static.go): a let registers its value
// (v1.OwnStatic), a hand-off clears its bit (v1.HandStatic), a returned
// value moves it to the kept set (v1.ReturnStatic).
//
// The release helper is a named IR function (RELEASE-IR, releaseFuncs):
// Go's recover reads the frame's panic only in the deferred function
// itself, so the helper is that function, and it takes the frame's state
// by pointer and reads it when it runs. Only its header is Go text, as
// every IR body's. Each value's release is a function literal of the IR
// that Static.Part runs. A slot whose Close the IR does not call (a host
// value's, an operation's, a generic one; closeCall) keeps the helpers
// Go text, written from the emitter's release primitives
// (providerRelease, runtimeCall): the rest of the RELEASE-IR debt, before
// F8; self-hosting does not port this writer.
//
// A frame with an error row hands its helper the error result's address
// (the derivative's "result" expression), as the emitter's End(&err)
// does. A release inside the body (a scope's end, a let's or a
// statement's planned releases, a break or a continue; the emitter's
// releaseAt and scoped) is the emitter's staticRelease: the value's bit
// is tested and cleared, then the slot's Free helper releases it (an IR
// function too, or Go text beside a Go-text release). A planned release at a place the builder does not
// reach (a condition's temporaries, a match subject) stops the body.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// irOwn is the static frame of the IR body being built.
type irOwn struct {
	live, kept int // locals; kept is -1 until a return keeps a value
	slots      []irSlot
	bit        map[*check.Var]int
	deferred   *irStmt       // the frame's deferred release, filled by ownDone
	returned   *syntax.Ident // the local a return hands off (returning)
	site       syntax.Node
	p          irPos
	// keys are the places of the body whose planned releases the builder
	// has not reached yet (releaseAt, scoped); pending the owned locals
	// bound and not yet registered (ownable, ownLet). Both end empty
	// (ownVisited).
	keys    map[syntax.Node]bool
	pending map[*check.Var]bool
	// list is the frame scope's list form (GP-SPAWN, the emitter's own,
	// Hand, Returned and Owned.Release): each owned local is an entry of
	// the scope (v1.Owning), its handle the local handle[v], and the
	// scope's End releases what is still listed. live, kept, slots, bit
	// and deferred are the static form's and stay unused.
	list   bool
	handle map[*check.Var]int
}

type irSlot struct {
	local  int
	t      check.Type
	d      *check.Drop
	helper string      // the slot's release inside the body, once used
	site   syntax.Node // where the slot first owns its value (staticOwn's site)
}

// irFrame reports a body whose frame the IR builds (decls.go lifts the
// frame's gate): the static release form, or a frame scope that owns no
// value (GP-FRAME, from FORK's shelved scope-frame.patch: the frame of a
// body that starts a task, registers an exit action or receives a Task).
// A fallible one has named results (namedResults); its exit settles on
// the error result's address ("result", the emitter's End(&err)). A
// plain frame not in the static form keeps its owned values in the list
// form of a plain scope (openScope, openList; OWNED-LOCAL).
func irFrame(fc *funcCtx) bool {
	if fc.frame == nil || fc.frame.static != nil {
		return fc.frame != nil
	}
	if fc.frame.plain {
		return frameListLowering != "off" // owned values only: the list form (openList)
	}
	return frameScopeLowering != "off"
}

// frameScopeLowering selects the frame scope on the IR: "" builds it,
// "off" keeps such a body's source (the reference GP-FRAME's receipt
// counts against). Set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.frameScopeLowering=off.
var frameScopeLowering string

// openOwn starts the frame of the body of f, whose parameters are params:
// a frame scope (openScope), or the static frame's live local, its
// deferred release and its taken parameters' registrations, before the
// entry poll (the emitter's openFrame, ownVar).
func (b *irBuilder) openOwn(f *check.Func, params []*check.Var) []*irStmt {
	if b.fc.frame.static == nil {
		if b.l.bodyNeedsFrame(f.Decl.Body.List) || b.l.ownsParams(params) {
			return b.openScope(f, params)
		}
		return nil
	}
	if b.env < 0 {
		irNo("owned local") // a pure body has no task to settle its exit on
	}
	p := b.pos(f.Decl, f.Decl.Pos())
	o := &irOwn{kept: -1, bit: map[*check.Var]int{}, site: f.Decl, p: p, keys: map[syntax.Node]bool{}, pending: map[*check.Var]bool{}}
	b.ownBodyChecks(f.Decl.Body, o)
	// The body's own scope ends at the frame's exit: its values are the
	// deferred release's, in the same order (last registered first).
	delete(o.keys, f.Decl.Body)
	o.live = b.temp("live", "uint64", p)
	o.deferred = &irStmt{K: "defer", Pos: p}
	b.own = o
	out := []*irStmt{o.deferred}
	for _, v := range params {
		if v == nil || b.l.info.OwnedVars[v] == nil {
			continue
		}
		i, ok := b.locals[v]
		if !ok {
			irNo("owned parameters")
		}
		out = append(out, b.ownLet(v, i, f.Decl.Name, nil)...) // the emitter's ownVar site
	}
	return out
}

// openScope opens the frame scope of f's body, whose parameters are
// params (irFrame): its local, made from the caller's task, and its
// deferred End on the error result's address, or nil with no error row
// (the emitter's openFrame). A std call that takes the frame passes the
// local's address (frameArg). The scope's owned values are the emitter's
// list form: a taken parameter registered here, and an owned local or
// temporary where it is bound (ownable, releaseAt). A plain frame (owned
// values only) opens v1.NewOwnerScope.
func (b *irBuilder) openScope(f *check.Func, params []*check.Var) []*irStmt {
	p := b.pos(f.Decl, f.Decl.Pos())
	errArg := &irExpr{K: "zero", Pos: p, T: "*error"}
	if b.fc.frame.fallible {
		// The frame's named error result, last (namedResults).
		last := 0
		if b.result != nil {
			last = 1
		}
		errArg = &irExpr{K: "addr", Pos: p, X: &irExpr{K: "result", Pos: p, I: last}}
	}
	return b.openScopeOf(f.Decl.Body, f.Decl, f.Decl.Name, params, errArg, true)
}

// openScopeOf is openScope's frame of body, whose node is site (a
// declaration or a function literal) and whose parameters are params:
// errArg is the address its End settles; exits admits the static exit
// form, which a function literal's frame does not take (the emitter's
// funcLitClosure opens a scope).
func (b *irBuilder) openScopeOf(body *syntax.BlockStmt, site, name syntax.Node, params []*check.Var, errArg *irExpr, exits bool) []*irStmt {
	plain, takes := b.fc.frame.plain, b.l.ownsParams(params)
	if (plain || takes) && frameListLowering == "off" {
		irNo("owned local") // owned values in a frame scope: the list form
	}
	if b.env < 0 {
		irNo("body that needs a frame") // the checker gives a frame a task
	}
	p := b.pos(site, site.Pos())
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	if exits && !plain && !takes && b.l.staticExits(body.List) {
		return b.openExits(p, task, errArg) // ir_exits.go (STATIC-EXIT)
	}
	// A frame that opens its scope only for its owned values opens a
	// plain one, whose End is no interruption point (the emitter's
	// openFrame).
	ctor := "NewScope"
	if plain {
		ctor = "NewOwnerScope"
	}
	fs := b.temp("fs", b.spelling(b.l.rt()+".Scope"), p)
	b.frame = fs + 1
	b.keepImport("var _ = " + b.l.rt() + "." + ctor)
	if (takes || b.ownsLocals(body)) && frameListLowering != "off" {
		b.openList(body, site, p)
	}
	out := []*irStmt{
		{K: "var", Pos: p, Local: fs},
		{K: "set", Pos: p, Local: fs, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: ctor, Args: []*irExpr{task}}},
		{K: "defer", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "End", X: b.addrOf(fs, p), Args: []*irExpr{errArg}}},
	}
	// A taken parameter is registered before the entry poll (R8; the
	// emitter's ownVar after openFrame).
	for _, v := range params {
		if v == nil || b.l.info.OwnedVars[v] == nil {
			continue
		}
		i, ok := b.locals[v]
		if !ok {
			irNo("owned parameters")
		}
		out = append(out, b.ownLet(v, i, name, nil)...)
	}
	return out
}

// frameListLowering selects the list form of a frame scope's owned locals
// on the IR: "" builds it, "off" keeps such a body's source (the
// reference GP-SPAWN's receipt counts against), "env" builds it with
// every Task's release closing over the frame's environment (the
// reference of v1.ReleaseTask's non-capturing release). Set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.frameListLowering=off.
var frameListLowering string

// ownsLocals reports a body that binds a local the checker gives an owner
// (Info.OwnedVars), outside the function literals in it (frames of their
// own).
func (b *irBuilder) ownsLocals(body *syntax.BlockStmt) bool {
	owns := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		if n == nil || owns {
			return false
		}
		if _, lit := n.(*syntax.FuncLit); lit {
			return false
		}
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := b.l.info.Defs[id].(*check.Var); ok && b.l.info.OwnedVars[v] != nil {
				owns = true
			}
		}
		return true
	})
	return owns
}

// openList starts the list form of the frame scope of body, whose node is
// site (a declaration or a function literal) (the
// emitter's own, owning, handoff and releaseAt on a frame that is not in
// the static form): the checks a static frame runs (ownBodyChecks), and
// an assignment to an owned local stops it (R41 registers the new value
// and releases the old, which this form does not write).
func (b *irBuilder) openList(body *syntax.BlockStmt, site syntax.Node, p irPos) {
	o := &irOwn{list: true, kept: -1, live: -1, handle: map[*check.Var]int{}, bit: map[*check.Var]int{}, site: site, p: p, keys: map[syntax.Node]bool{}, pending: map[*check.Var]bool{}}
	b.ownBodyChecks(body, o)
	syntax.Inspect(body, func(n syntax.Node) bool {
		if _, lit := n.(*syntax.FuncLit); lit || n == nil {
			return false
		}
		if s, ok := n.(*syntax.AssignStmt); ok {
			for _, x := range s.Lhs {
				if id, ok := unparen(x).(*syntax.Ident); ok {
					if v, ok := b.l.info.Uses[id].(*check.Var); ok && b.l.info.OwnedVars[v] != nil {
						irNo("owned local") // R41: the list form's reassignment
					}
				}
			}
		}
		return true
	})
	delete(o.keys, body) // the body's own scope ends at the frame's End
	b.own = o
}

// listLet registers the owned local v, bound to the local i, on the frame
// scope: a handle of the scope (Scope.Owner) and v1.Owning with v's
// release (the emitter's own and owning).
func (b *irBuilder) listLet(v *check.Var, i int, d *check.Drop, site syntax.Node, value syntax.Expr) []*irStmt {
	o := b.own
	if d.Glue() {
		irNo("owned local") // OwningParts: a generated release
	}
	p := b.pos(site, site.Pos())
	typ := b.goType(v.Type, site)
	if _, seen := o.handle[v]; seen {
		irNo("owned local") // a local bound twice
	}
	// The handle is defined where the local is: its scope is the local's.
	h := b.temp("own", "*"+b.spelling(b.l.rt()+".Owned"), p)
	o.handle[v] = h
	here := ""
	if n, ok := v.Type.(*check.Named); ok && len(n.TArgs) > 0 && b.startedHere(value) {
		here = b.goType(n.TArgs[0], site) // v1.Task's T: the IR names a generic function with its type arguments
	}
	rel := b.listRelease(d, v.Type, typ, p, here)
	out := []*irStmt{
		{K: "def", Pos: p, Local: h, X: &irExpr{K: "mcall", Pos: p, Name: "Owner", X: b.frameArg(p)}},
		{K: "expr", Pos: p, X: b.ownCall("Owning", typ, p, &irExpr{K: "local", Pos: p, I: h}, &irExpr{K: "local", Pos: p, I: i}, rel)},
	}
	if b.l.writtenInPlace(v) {
		// A local the code writes in place is released with its writes
		// (v1.OwningAt; the emitter's ownAt).
		at := b.listRelease(d, v.Type, typ, p, here)
		out = append(out, &irStmt{K: "expr", Pos: p, X: b.ownCall("OwningAt", typ, p, &irExpr{K: "local", Pos: p, I: h}, b.addrOf(i, p), at)})
	}
	return out
}

// listRelease is the release of an owned value of type t, Go type typ: a
// function literal of the value that calls its checked Close with the
// frame's environment (the emitter's ownRelease); a Close the IR does not
// call keeps the source body. here, when not "", is the Go type of the
// value of a Task this frame's scope started (startedHere): its release
// is v1.ReleaseTask[here], Close on the
// task that started it, which is the frame's, so the literal captures
// nothing and Go allocates no closure for it (PARK2's release closure).
func (b *irBuilder) listRelease(d *check.Drop, t check.Type, typ string, p irPos, here string) *irExpr {
	owned := b.temp("owned", typ, p)
	end := p
	lit := &irExpr{K: "func", Pos: p, T: "func(" + typ + ")", Params: []int{owned}, End: &end}
	if c, ok := b.taskClose(d, owned, p); ok {
		if here != "" && frameListLowering != "env" {
			c = &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "ReleaseTask", V: here, Args: []*irExpr{{K: "local", Pos: p, I: owned}}}
		}
		lit.Body = []*irStmt{{K: "expr", Pos: p, X: c}}
		return lit
	}
	// closeCall reads the value through a pointer local (a static slot's).
	ptr := b.temp("ptr", "*"+typ, p)
	c, ok := b.closeCall(irSlot{t: t, d: d}, b.env, ptr, p)
	if !ok {
		irNo("owned local")
	}
	lit.Body = []*irStmt{
		{K: "def", Pos: p, Local: ptr, X: b.addrOf(owned, p)},
		{K: "expr", Pos: p, X: c},
	}
	return lit
}

// taskClose is the release of the owned value in the local owned when
// its Close is a std operation's runtime method that takes the caller's
// task alone (task.Task's Close: the joining task is the frame's), as the
// emitter's providerRelease writes it through runtimeCall. ok is false for
// any other Close.
func (b *irBuilder) taskClose(d *check.Drop, owned int, p irPos) (*irExpr, bool) {
	cl := d.Close
	if cl == nil || cl.Func != nil || cl.Op == nil {
		return nil, false
	}
	n, ok := cl.Receiver.(*check.Named)
	if !ok || !b.l.runtimeNamed(n, b.own.site) {
		return nil, false
	}
	name := n.Obj.Name + "." + cl.Op.Name
	if _, bound := runtimeBindings()[name]; !bound {
		return nil, false
	}
	body, err := runtimeBody(name)
	if err != nil || len(body.Hidden) != 1 || body.Hidden[0] != v1.CallerTask || len(body.Params) != 0 || len(body.Results) != 0 {
		return nil, false
	}
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	return &irExpr{K: "mcall", Pos: p, Name: memberName(cl.Op.Name), X: &irExpr{K: "local", Pos: p, I: owned}, Args: []*irExpr{task}}, true
}

// startedHere reports a let's operand that is a call starting a task on
// this body's frame scope (startsOnFrame), so the Task's starting task is
// the frame's.
func (b *irBuilder) startedHere(value syntax.Expr) bool {
	x, ok := value.(*syntax.CallExpr)
	if !ok {
		return false
	}
	c := b.l.info.Calls[x]
	return c != nil && c.StartsTask && b.startsOnFrame(c)
}

// startsOnFrame reports a call c that starts a task on the body's frame
// scope: a std extern whose runtime/v1 body takes the hidden frame
// (task.Go, tasks.NewSet; needsFrame), so the scope owns what it starts.
// Any other call whose result holds a Task keeps its source body.
func (b *irBuilder) startsOnFrame(c *check.Call) bool {
	if c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg.Host() || c.Func.Decl == nil || c.Func.Decl.Extern == nil {
		return false
	}
	body, _, ok := b.l.stdBody(b.l.funcDecl(c.Func))
	return ok && hasHidden(body.Hidden, v1.Frame)
}

// ergoBodyCall reports a call c of a function with an Ergo body
// (tasks.NewSet, a user's function) whose result holds a running task:
// the callee started it on its own frame and hands it back, as any owned
// result (Returned), so the caller's frame registers it as an owned local
// (listLet) with its Close, as the emitter's own does (PORT-CALLS). A
// function value or an operation keeps its source body.
func ergoBodyCall(c *check.Call) bool {
	f := c.Func
	return c.Kind == check.CallFunc && f != nil && !f.Pkg.Host() && f.Decl != nil && f.Decl.Extern == nil && f.Decl.Body != nil
}

// frameArg is the hidden frame argument of a std call (v1.Frame): the
// address of the body's frame scope (openScope; the emitter's
// frameScope). A call outside one keeps its source body.
func (b *irBuilder) frameArg(p irPos) *irExpr {
	if b.frame == 0 {
		irNo("std call with hidden argument frame")
	}
	return b.addrOf(b.frame-1, p)
}

// closeMasked is the body of a checked Close f, built: the whole of it
// (its frame, entry poll and statements) runs as the function literal
// that v1.Release runs masked, with the caller's task first (the
// emitter's closeBody). A return in the body returns from the literal,
// and so from the Close. A Close with a generated release of its
// receiver's parts after it (CloseParts, glueWith) keeps its source body.
func (b *irBuilder) closeMasked(f *check.Func, body []*irStmt) []*irStmt {
	if !isClose(f) {
		return body
	}
	if f.Pkg.Info.CloseParts[f] != nil || b.env < 0 {
		irNo("checked Close (closeBody masks it)")
	}
	const name = "Release"
	rb, err := runtimeBody(name)
	if err != nil || len(rb.Hidden) != 1 || rb.Hidden[0] != v1.CallerTask {
		ice(f.Decl, "the release route %s takes the caller's task alone: %v", name, err)
	}
	p := b.pos(f.Decl, f.Decl.Pos())
	end := b.pos(f.Decl, f.Decl.Body.Rbrace)
	lit := &irExpr{K: "func", Pos: p, T: "func()", Body: body, End: &end}
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	b.keepImport("var _ = " + b.l.rt() + "." + name)
	return []*irStmt{{K: "expr", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, Args: []*irExpr{task, lit}}}}
}

// releaseFuncs writes the static frame's release helpers as IR functions
// (RELEASE-IR): helper, the deferred release whose Go parameters are
// params (env, live, kept, err, the slots, as ownDone passes them), and
// each slot's in-body release (slotRelease's helper). Only their
// declarations are Go text, a header with no body, as every IR body's.
// It reports false, and writes nothing, when a slot's Close is one the IR
// does not call (closeCall); the helpers are then Go text.
func (b *irBuilder) releaseFuncs(name, helper string, params []string) bool {
	o, l := b.own, b.l
	for _, s := range o.slots {
		if _, ok := b.closeCall(s, 0, 0, o.p); !ok {
			return false
		}
	}
	p := o.p
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	deref := func(i int) *irExpr { return &irExpr{K: "deref", Pos: p, X: local(i)} }
	// The deferred release: env, live, kept, err, then the slots.
	h := b.st.newBuilder(b.fc, nil) // a helper of b's analyzed body
	h.env, h.names, h.own = 0, b.names, o
	h.fn = &irFunc{Name: helper, Pos: p, End: p}
	liveL, keptL, errL := 1, -1, -1
	h.next = 2
	if o.kept >= 0 {
		keptL = h.next
		h.next++
	}
	if b.fc.frame.fallible {
		errL = h.next
		h.next++
	}
	slot0 := h.next
	h.next += len(o.slots)
	errArg := &irExpr{K: "zero", Pos: p, T: "*error"}
	if errL >= 0 {
		errArg = local(errL)
	}
	rel := h.temp("rel", h.spelling(l.rt()+".Static"), p)
	h.keepImport("var _ = " + l.rt() + ".StaticBegin")
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: local(0)}
	relCall := func(m string, args ...*irExpr) *irExpr {
		return &irExpr{K: "mcall", Pos: p, Name: m, X: h.addrOf(rel, p), Args: args}
	}
	set := func(set *irExpr, k int) *irExpr {
		return &irExpr{K: "bin", Pos: p, Op: "!=", X: &irExpr{K: "bin", Pos: p, Op: "&", X: set, Y: h.bitConst(k, p)}, Y: &irExpr{K: "const", Pos: p, T: "uint64", V: "0"}}
	}
	part := func(setOf func() *irExpr, k int) *irStmt {
		c, _ := h.closeCall(o.slots[k], 0, slot0+k, p)
		end := p
		lit := &irExpr{K: "func", Pos: p, T: "func()", Body: []*irStmt{{K: "expr", Pos: p, X: c}}, End: &end}
		return &irStmt{K: "if", Pos: p, X: set(setOf(), k), End: p, Then: []*irStmt{{K: "expr", Pos: p, X: relCall("Part", lit)}}}
	}
	body := []*irStmt{
		{K: "var", Pos: p, Local: rel},
		{K: "set", Pos: p, Local: rel, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "StaticBegin", Args: []*irExpr{task, errArg, {K: "recover", Pos: p}}}},
	}
	for k := len(o.slots) - 1; k >= 0; k-- {
		body = append(body, part(func() *irExpr { return deref(liveL) }, k))
	}
	if keptL >= 0 {
		var failed []*irStmt
		for k := len(o.slots) - 1; k >= 0; k-- {
			failed = append(failed, part(func() *irExpr { return deref(keptL) }, k))
		}
		// *kept != 0 && rel.Failed(): Failed runs only when a value is kept.
		body = append(body, &irStmt{K: "if", Pos: p, End: p,
			X:    &irExpr{K: "bin", Pos: p, Op: "!=", X: deref(keptL), Y: &irExpr{K: "const", Pos: p, T: "uint64", V: "0"}},
			Then: []*irStmt{{K: "if", Pos: p, End: p, X: relCall("Failed"), Then: failed}}})
	}
	body = append(body, &irStmt{K: "expr", Pos: p, X: relCall("End", errArg)})
	h.fn.Body = irUseLocals(body)
	b.st.out.Funcs = append(b.st.out.Funcs, *h.fn)
	var t strings.Builder
	t.WriteString("// " + helper + " is the deferred release of " + name + "'s static frame; its body is IR (FLIP F7, RELEASE-IR).\n")
	t.WriteString("func " + helper + "(" + strings.Join(params, ", ") + ")\n\n")
	// A slot released inside the body (slotRelease): its release alone,
	// whose defect leaves at once (the emitter's staticRelease).
	for _, s := range o.slots {
		if s.helper == "" {
			continue
		}
		f := b.st.newBuilder(b.fc, nil) // a helper of b's analyzed body
		f.env, f.names, f.next, f.own = 0, b.names, 2, o
		c, _ := f.closeCall(s, 0, 1, p)
		f.fn = &irFunc{Name: s.helper, Pos: p, End: p, Body: []*irStmt{{K: "expr", Pos: p, X: c}}}
		b.st.out.Funcs = append(b.st.out.Funcs, *f.fn)
		t.WriteString("// " + s.helper + " releases one value of " + name + "'s static frame inside its body; its body is IR (FLIP F7, RELEASE-IR).\n")
		t.WriteString("func " + s.helper + "(" + b.fc.envExpr(o.site) + " *" + l.env() + ", " + l.names.alloc("slot") + " *" + b.fc.goType(s.t) + ")\n\n")
	}
	l.gen.WriteString(t.String())
	return true
}

// closeCall is the call of slot s's checked Close on the value the local
// slot points at, with the environment the local env (the emitter's
// providerRelease of a program's Close): a method call or a function
// call, the receiver taken by address or through a pointer as Close's
// parameter needs, a generic one with the provider's type arguments; a
// std operation type's Close is stdCloseCall's, a Go value's
// hostCloseCall's (ir_gocall.go). ok is false for a Close the IR does not
// call: a std or extern function (the checker refuses it, C26).
func (b *irBuilder) closeCall(s irSlot, env, slot int, p irPos) (*irExpr, bool) {
	cl := s.d.Close
	if cl != nil && cl.Func == nil && cl.Op != nil {
		return b.stdCloseCall(cl, s, slot, p)
	}
	if cl != nil && cl.HostClose() {
		return b.hostCloseCall(cl, s, env, slot, p) // ir_gocall.go
	}
	if cl == nil || cl.Func == nil || !isClose(cl.Func) || len(cl.Func.Sig.Params) != 1 {
		return nil, false
	}
	if len(cl.Func.Sig.TParams) > 0 && len(witnessParams(cl.Func.Sig)) > 0 {
		return nil, false // the checker refuses it (S109, closeWitnessed)
	}
	fd := b.l.useFunc(cl.Func, b.fc.site)
	if _, _, std := b.l.stdBody(fd); std || fd.extern != "" {
		return nil, false
	}
	_, byPtr := cl.Func.Sig.Params[0].Type.(*check.Pointer)
	_, ptrValue := cl.Receiver.(*check.Pointer)
	recv := &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: slot}}
	switch {
	case byPtr && !ptrValue:
		recv = &irExpr{K: "local", Pos: p, I: slot}
	case !byPtr && ptrValue:
		recv = &irExpr{K: "deref", Pos: p, X: recv}
	}
	e := &irExpr{K: "local", Pos: p, I: env}
	if fd.method != nil {
		return &irExpr{K: "mcall", Pos: p, Name: fd.name, X: recv, Args: []*irExpr{e}}, true
	}
	// A generic Close function takes the provider's type arguments
	// (closeTypeArgs), as providerRelease writes them; a generic type's
	// Close method takes them from its receiver.
	var targs []string
	if len(cl.Func.Sig.TParams) > 0 {
		args := closeTypeArgs(cl, b.own.site)
		for i, tp := range cl.Func.Sig.TParams {
			if b.l.goParam(tp) {
				targs = append(targs, b.spelling(b.fc.goArg(tp, args[i])))
			}
		}
	}
	return &irExpr{K: "call", Pos: p, Name: fd.name, T: strings.Join(targs, ", "), Args: []*irExpr{e, recv}}, true
}

// stdCloseCall is the release of a value of a std operation type at slot
// (fs.Mapped): its runtime method, as the emitter's opCall writes it
// through runtimeCall, for a Close that takes nothing and returns nothing
// (no hidden argument, no operand) on a Go pointer. Any other keeps the
// Go-text release.
func (b *irBuilder) stdCloseCall(cl *check.Close, s irSlot, slot int, p irPos) (*irExpr, bool) {
	n, ok := cl.Receiver.(*check.Named)
	if !ok || !b.l.runtimeNamed(n, b.own.site) || !strings.HasPrefix(b.fc.goType(s.t), "*") {
		return nil, false
	}
	name := n.Obj.Name + "." + cl.Op.Name
	if _, bound := runtimeBindings()[name]; !bound {
		return nil, false
	}
	body, err := runtimeBody(name)
	if err != nil || len(body.Hidden) != 0 || len(body.Params) != 0 || len(body.Results) != 0 {
		return nil, false
	}
	return &irExpr{K: "mcall", Pos: p, Name: memberName(cl.Op.Name), X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: slot}}}, true
}

// ownBodyChecks stops a static frame this form does not build: an owned
// temporary or arm value, a record
// update's replaced fields, a taking receiver passed by its address, a
// match pattern that owns. It records in o the places with planned
// releases (Info.Releases), which the builder must each reach.
func (b *irBuilder) ownBodyChecks(body *syntax.BlockStmt, o *irOwn) {
	info := b.l.info
	syntax.Inspect(body, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		if _, lit := n.(*syntax.FuncLit); lit {
			return false // a frame of its own
		}
		if len(info.Releases[n]) > 0 {
			o.keys[n] = true
		}
		// A try's failure edge (Info.TryReleases) writes nothing here: it
		// leaves the frame through its deferred release, which releases
		// what the frame holds then (the live bits, or the scope's list),
		// as the source path's does (OWNED-LOCAL-2).
		switch n := n.(type) {
		case *syntax.CaseClause:
			if po, ok := info.PatternOwners[n]; ok && (len(po.Sources) > 0 || len(po.Bindings) > 0) {
				irNo("owned local") // patternOwners registers them
			}
		case *syntax.CompositeLit:
			if len(info.Replaced[n]) > 0 {
				irNo("owned temporary")
			}
		case *syntax.CallExpr:
			if c := info.Calls[n]; c != nil && c.Recv != nil && c.Func != nil && c.Func.Ptr {
				if id, ok := unparen(c.Recv).(*syntax.Ident); ok && info.Handoffs[id] != nil {
					irNo("owned local")
				}
			}
		}
		if x, ok := n.(syntax.Expr); ok && (info.OwnedTemps[x] != nil || info.ArmValues[x] != nil) {
			irNo("owned temporary")
		}
		return true
	})
}

// releasesAtExit reports a statement s of a static frame whose planned
// releases (Info.Releases[s]) the emitter never writes, because its Go
// form leaves the frame: a return, or the body's last statement when it
// returns a plain value (result is set). The frame's deferred release
// releases them. Any other statement's releases stay the emitter's.
func (b *irBuilder) releasesAtExit(s syntax.Stmt, result bool) bool {
	if b.own == nil {
		return false
	}
	if b.leavesFrame(s, result) {
		delete(b.own.keys, s)
		return true
	}
	return false
}

func (b *irBuilder) leavesFrame(s syntax.Stmt, result bool) bool {
	switch s := s.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.ExprStmt:
		if !result || b.result == nil {
			return false
		}
		if isBranch(s.X) {
			return false // an if's or a match's arms yield: its Go form may end normally
		}
		if _, with := b.isWith(s.X); with {
			return false
		}
		return true
	}
	return false
}

// ownable stops an owned local outside a static frame the IR builds; in
// one, the local waits for its registration (ownLet, ownVisited).
func (b *irBuilder) ownable(v *check.Var) {
	if b.l.info.OwnedVars[v] == nil {
		return
	}
	if b.own == nil {
		irNo("owned local")
	}
	b.own.pending[v] = true
}

// releaseAt is the releases the plan records at key, a place that ends
// inside the frame, in the plan's order (last declared first; a static
// frame reassigns no owned local, so it is the order of registration):
// each value whose bit is still set is released now (the emitter's
// releaseAt and staticRelease). A value with no owner (a type
// parameter's, B2) is its instance's to release.
func (b *irBuilder) releaseAt(key syntax.Node, p irPos) []*irStmt {
	rs := b.l.info.Releases[key]
	o := b.own
	if o != nil {
		delete(o.keys, key)
	}
	var out []*irStmt
	for _, r := range rs {
		if r.Var == nil {
			if b.l.info.OwnedTemps[r.Temp] != nil {
				irNo("owned temporary")
			}
			continue
		}
		if b.l.info.OwnedVars[r.Var] == nil {
			continue
		}
		if o == nil {
			irNo("statement that releases owned values") // a frame the IR does not build
		}
		if o.list {
			// Owned.Release: the entry, if a hand-off on this path has
			// not removed it (R21; the emitter's releaseAt).
			h, ok := o.handle[r.Var]
			if !ok {
				irNo("statement that releases owned values") // not registered on this path
			}
			out = append(out, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Release", X: &irExpr{K: "local", Pos: p, I: h}}})
			continue
		}
		k, ok := o.bit[r.Var]
		if !ok {
			irNo("statement that releases owned values") // not registered on this path
		}
		out = append(out, b.slotRelease(k, p))
	}
	return out
}

// scoped is list, the statements of the scope key, followed by the
// scope's releases when list reaches its end (the emitter's scoped). A
// scope that leaves (a return, a break, a continue) has its values
// released by the frame's exit or by the jump's own releases.
func (b *irBuilder) scoped(key syntax.Node, list []*irStmt, p irPos) []*irStmt {
	if len(b.l.info.Releases[key]) == 0 {
		return list
	}
	rel := b.releaseAt(key, p)
	if irListEnds(list) {
		return list
	}
	return append(list, rel...)
}

// irListEnds reports a list after whose last statement nothing runs: a
// terminating one, a break or a continue.
func irListEnds(list []*irStmt) bool {
	if n := len(list); n > 0 && (list[n-1].K == "break" || list[n-1].K == "continue") {
		return true
	}
	return irListTerminates(list)
}

// slotRelease releases slot k's value if its bit is set: the bit is
// cleared first, so a defect in the release leaves the value released
// once (R9), and the frame's exit releases the rest.
func (b *irBuilder) slotRelease(k int, p irPos) *irStmt {
	o := b.own
	s := &o.slots[k]
	if s.helper == "" {
		s.helper = b.l.names.alloc(strings.ReplaceAll(b.fn.Name, ".", "_") + "Free")
	}
	live := func() *irExpr { return &irExpr{K: "local", Pos: p, I: o.live} }
	set := &irExpr{K: "bin", Pos: p, Op: "!=", X: &irExpr{K: "bin", Pos: p, Op: "&", X: live(), Y: b.bitConst(k, p)}, Y: &irExpr{K: "const", Pos: p, T: "uint64", V: "0"}}
	return &irStmt{K: "if", Pos: p, X: set, End: p, Then: []*irStmt{
		{K: "set", Pos: p, Local: o.live, X: &irExpr{K: "bin", Pos: p, Op: "&^", X: live(), Y: b.bitConst(k, p)}},
		{K: "expr", Pos: p, X: &irExpr{K: "call", Pos: p, Name: s.helper, Args: []*irExpr{{K: "local", Pos: p, I: b.env}, b.addrOf(s.local, p)}}},
	}}
}

// ownVisited stops a static frame whose body left a planned release
// unreached or an owned local unregistered (a binding the builder does
// not register: a pattern's, a loop's). In a census a body that stopped
// already is not charged for the places it never reached.
func (b *irBuilder) ownVisited() {
	o := b.own
	if o == nil || len(b.reasons) > 0 {
		return
	}
	if len(o.keys) > 0 {
		irNo("statement that releases owned values")
	}
	if len(o.pending) > 0 {
		irNo("owned local")
	}
}

// ownLet registers the owned local v, bound to the local i, in its slot:
// the step that receives the value (R8; the emitter's ownVar). value is
// the let's operand, or nil for a parameter.
func (b *irBuilder) ownLet(v *check.Var, i int, site syntax.Node, value syntax.Expr) []*irStmt {
	d := b.l.info.OwnedVars[v]
	if d == nil {
		return nil
	}
	o := b.own
	if o == nil {
		irNo("owned local")
	}
	delete(o.pending, v)
	if o.list {
		return b.listLet(v, i, d, site, value)
	}
	if d.Glue() || d.Close == nil {
		irNo("owned local") // staticPlan admits a type's own Close only
	}
	p := b.pos(site, site.Pos())
	k, seen := o.bit[v]
	if !seen {
		k = len(o.slots)
		if k >= 64 {
			irNo("owned local")
		}
		o.bit[v] = k
		typ := b.goType(v.Type, site)
		o.slots = append(o.slots, irSlot{local: b.temp("slot", typ, p), t: v.Type, d: d, site: site})
	}
	typ := b.goType(v.Type, site)
	call := b.ownCall("OwnStatic", typ, p, b.addrOf(o.live, p), b.bitConst(k, p), b.addrOf(o.slots[k].local, p), &irExpr{K: "local", Pos: p, I: i})
	return []*irStmt{{K: "expr", Pos: p, X: call}}
}

// handoff is the read e of id, a use of an owned local: as it is, or at a
// hand-off with the local's bit cleared, or moved to the kept set when the
// frame returns it (R10; the emitter's handoff).
func (b *irBuilder) handoff(id *syntax.Ident, e *irExpr) *irExpr {
	v := b.l.info.Handoffs[id]
	if v == nil || b.l.info.OwnedVars[v] == nil {
		return e
	}
	o := b.own
	if o == nil {
		irNo("owned local")
	}
	if o.list {
		return b.listHand(o, v, id, e)
	}
	k, ok := o.bit[v]
	if !ok {
		irNo("owned local")
	}
	p := e.Pos
	typ := b.goType(v.Type, id)
	if id == o.returned {
		if o.kept < 0 {
			o.kept = b.temp("kept", "uint64", o.p)
		}
		return b.ownCall("ReturnStatic", typ, p, b.addrOf(o.live, p), b.addrOf(o.kept, p), b.bitConst(k, p), e)
	}
	if b.l.info.ReturnedTemps[id] {
		irNo("owned local") // an arm's tail of a returned if or match
	}
	return b.ownCall("HandStatic", typ, p, b.addrOf(o.live, p), b.bitConst(k, p), e)
}

// listHand is the read e of id, a hand-off of the list form's owned local
// v: v1.Hand removes its entry, v1.Returned keeps it until the frame's
// exit is known when the frame returns it (R10; the emitter's handoff).
func (b *irBuilder) listHand(o *irOwn, v *check.Var, id *syntax.Ident, e *irExpr) *irExpr {
	h, ok := o.handle[v]
	if !ok {
		irNo("owned local") // not registered on this path
	}
	name := "Hand"
	if id == o.returned || b.l.info.ReturnedTemps[id] {
		// A return's operand, or a tail the frame returns (an arm's of a
		// returned if or match included): kept until the frame's exit is
		// known (R10; the emitter's tails.go sets fc.returned for it).
		name = "Returned"
	}
	return b.ownCall(name, b.goType(v.Type, id), e.Pos, &irExpr{K: "local", Pos: e.Pos, I: h}, e)
}

// returning marks x, when it is a local, as the value a return hands off
// while its value is built (the emitter's fc.returned); the result undoes
// the mark.
func (b *irBuilder) returning(x syntax.Expr) func() {
	o := b.own
	if o == nil {
		return func() {}
	}
	prev := o.returned
	o.returned, _ = unparen(x).(*syntax.Ident)
	return func() { o.returned = prev }
}

// ownedBodyName is the Go name of fd's IR body: for a function with owned
// parameters (values.go V5), its owned body's, whose bind copies nothing;
// f's own name is then the wrapper that copies each owned argument and
// calls it (decls.go writes it, ownedWrapper: Go text, the OWNED-WRAP-IR
// debt). name is the body's name otherwise.
func (b *irBuilder) ownedBodyName(fd *funcDecl, name string, drop bool) string {
	f := fd.fn
	if drop || fd.extern != "" || len(b.l.ownedParams(f)) == 0 {
		return name
	}
	return b.l.pkgName(ownedKey(f), f.Name+"Owned")
}

// ownedWrapper writes the wrapper of fd's owned body as an IR function
// (OWNED-WRAP-IR): under f's own name, it copies each owned argument as
// cloneValue copies it (copyOf) and calls the owned body bodyName, as the
// emitter's ownedWrapper writes it. head, params and results are its Go
// header, args its parameters' names after the receiver recv, typeArgs
// the body's type arguments. Only the header is Go text. It reports false
// when the IR does not build a copy (a defined Map or slice type): the
// caller then writes the wrapper as Go text.
func (st *irState) ownedWrapper(fc *funcCtx, fd *funcDecl, head, params, results, bodyName string, typeArgs, args []string, recv string, site syntax.Node) (ok bool) {
	if st.census != nil {
		return false // a census counts bodies; a wrapper is not one
	}
	f := fd.fn
	b := st.newBuilder(fc, nil) // a wrapper: no source statements
	b.fn = &irFunc{}
	keep := st.keep
	defer func() {
		if r := recover(); r != nil {
			if _, no := r.(irUnsupported); !no {
				panic(r)
			}
			st.keep, ok = keep, false
		}
	}()
	if keep != nil {
		// A refused wrapper keeps no import of its own.
		st.keep = map[string]bool{}
		for k := range keep {
			st.keep[k] = true
		}
	}
	p := b.pos(site, site.Pos())
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	owned := map[int]bool{}
	for _, o := range b.l.ownedParams(f) {
		owned[o.index] = true
	}
	first := 0
	var recvE *irExpr
	if recv != "" {
		first = 1
		recvE = local(0)
		if owned[0] {
			recvE = b.copyOf(f.Sig.Params[0].Type, site, recvE)
		}
	}
	lead := len(args) - (len(f.Sig.Params) - first)
	ops := make([]*irExpr, len(args))
	for i := range args {
		ops[i] = local(first + i)
		if j := i - lead + first; i >= lead && owned[j] {
			ops[i] = b.copyOf(f.Sig.Params[j].Type, site, ops[i])
		}
	}
	var call *irExpr
	if recvE != nil {
		call = &irExpr{K: "mcall", Pos: p, Name: bodyName, X: recvE, Args: ops}
	} else {
		ts := make([]string, len(typeArgs))
		for i, t := range typeArgs {
			ts[i] = b.spelling(t)
		}
		call = &irExpr{K: "call", Pos: p, Name: bodyName, T: strings.Join(ts, ", "), Args: ops}
	}
	stmt := &irStmt{K: "expr", Pos: p, X: call}
	if strings.TrimSpace(results) != "" {
		stmt = &irStmt{K: "ret", Pos: p, X: call}
	}
	name := fd.name
	if fd.method != nil {
		name = fd.method.name + "." + name
	}
	st.out.Funcs = append(st.out.Funcs, irFunc{Name: name, Pos: p, End: p, Body: []*irStmt{stmt}})
	fc.l.gen.WriteString("// " + fd.name + " copies its owned arguments into " + bodyName + "; its body is IR (FLIP F7, OWNED-WRAP-IR).\n" + head + params + results + irHeadEnd(fc.goGeneric) + "\n\n") // ir_placeholder.go (PATCH-MIN M3)
	return true
}

// consumedForm is the runtime/v1 body a std call x of body (named name)
// calls: its owned form when the value analysis found its first operand
// consumed (vals.consumes: FromBytesOwned's builderText(bs) takes bs over
// with no copy), or in a test build its checking form, as the emitter's
// stdCall chooses them (ownedFormAt); name otherwise (FORK-ALLOC).
func (b *irBuilder) consumedForm(x *syntax.CallExpr, body v1.Body, name string) string {
	if b.l.vals == nil || !b.l.vals.consumes[x] || body.Owned == "" {
		return name
	}
	form := b.fc.ownedFormAt(x, body)
	if _, bound := runtimeBindings()[form]; !bound {
		irNo("std call with no runtime binding")
	}
	return form
}

// ownedCallName is the Go name a call x of f names: the owned body when
// the value analysis decided the call passes its owned arguments
// unshared (vals.ownCall; an argument that must be copied is a value copy
// the IR refuses, ir_expr.go), else name (the emitter's callText).
func (b *irBuilder) ownedCallName(x *syntax.CallExpr, f *check.Func, name string) string {
	if b.l.vals == nil || !b.l.vals.ownCall[x] {
		return name
	}
	return b.l.pkgName(ownedKey(f), f.Name+"Owned")
}

// ownCall is the call of the generic runtime/v1 function name on one
// value of the Go type typ.
func (b *irBuilder) ownCall(name, typ string, p irPos, args ...*irExpr) *irExpr {
	if _, bound := runtimeBindings()[name]; !bound {
		ice(b.own.site, "the ownership binding %s has no runtime/v1 binding", name)
	}
	b.keepImport("var _ " + b.l.rt() + ".Site") // a generic function: no value of it stands alone
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, V: typ, Args: args}
}

func (b *irBuilder) addrOf(local int, p irPos) *irExpr {
	return &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: local}}
}

func (b *irBuilder) bitConst(k int, p irPos) *irExpr {
	return &irExpr{K: "const", Pos: p, T: "uint64", V: strconv.FormatUint(1<<uint(k), 10)}
}

// ownDone ends a static frame of a body the IR built: the deferred call
// of its release helper, whose Go text it writes in the generated part.
func (b *irBuilder) ownDone(name string) {
	o := b.own
	if o == nil || o.list {
		return // the list form's values are the scope's End's
	}
	fc, l, site := b.fc, b.l, o.site
	env := fc.envExpr(site)
	helper := l.names.alloc(strings.ReplaceAll(name, ".", "_") + "Release") // a method's name is T.m
	live, kept, rel := l.names.alloc("live"), l.names.alloc("kept"), l.names.alloc("rel")
	params := []string{env + " *" + l.env(), live + " *uint64"}
	args := []*irExpr{{K: "local", Pos: o.p, I: b.env}, b.addrOf(o.live, o.p)}
	if o.kept >= 0 {
		params = append(params, kept+" *uint64")
		args = append(args, b.addrOf(o.kept, o.p))
	}
	errArg := "nil"
	if fc.frame.fallible {
		// The frame's named error result, last (namedResults): End
		// settles the exit into it after the body returned.
		errArg = l.names.alloc("err")
		params = append(params, errArg+" *error")
		last := 0
		if b.result != nil {
			last = 1
		}
		args = append(args, &irExpr{K: "addr", Pos: o.p, X: &irExpr{K: "result", Pos: o.p, I: last}})
	}
	slots := make([]string, len(o.slots))
	for k, s := range o.slots {
		slots[k] = l.names.alloc("slot")
		params = append(params, slots[k]+" *"+fc.goType(s.t))
		args = append(args, b.addrOf(s.local, o.p))
	}
	o.deferred.X = &irExpr{K: "call", Pos: o.p, Name: helper, Args: args}
	// The frame's locals are declared before the deferred call reads
	// their addresses (Go's writer takes a local used before its
	// declaration for a closure's free variable).
	decls := []*irStmt{{K: "var", Pos: o.p, Local: o.live}}
	if o.kept >= 0 {
		decls = append(decls, &irStmt{K: "var", Pos: o.p, Local: o.kept})
	}
	for _, s := range o.slots {
		decls = append(decls, &irStmt{K: "var", Pos: o.p, Local: s.local})
	}
	b.fn.Body = append(decls, b.fn.Body...)
	if b.releaseFuncs(name, helper, params) {
		return
	}
	release := func(k int) string {
		s := o.slots[k]
		return fc.providerRelease(&check.Acquisition{Close: s.d.Close}, env, "(*"+slots[k]+")", site)
	}
	part := func(set string, k int) string {
		return "\tif (*" + set + ")&(" + staticBit(k) + ") != 0 {\n\t\t" +
			fc.runtimeCall("Static.Part", fc.ownBinding("Static.Part", site), "", "&"+rel, []string{"func() { " + release(k) + " }"}, "", nil, nil, site) + "\n\t}\n"
	}
	var t strings.Builder
	t.WriteString("// " + helper + " is the deferred release of " + name + "'s static frame (IR, FLIP F7).\n")
	t.WriteString("func " + helper + "(" + strings.Join(params, ", ") + ") {\n")
	t.WriteString("\t" + rel + " := " + fc.runtimeCall("StaticBegin", fc.ownBinding("StaticBegin", site), "", "", []string{errArg, "recover()"}, env, nil, nil, site) + "\n")
	for k := len(o.slots) - 1; k >= 0; k-- {
		t.WriteString(part(live, k))
	}
	if o.kept >= 0 {
		t.WriteString("\tif *" + kept + " != 0 && " + fc.runtimeCall("Static.Failed", fc.ownBinding("Static.Failed", site), "", "&"+rel, nil, "", nil, nil, site) + " {\n")
		for k := len(o.slots) - 1; k >= 0; k-- {
			t.WriteString(part(kept, k))
		}
		t.WriteString("\t}\n")
	}
	t.WriteString("\t" + fc.runtimeCall("Static.End", fc.ownBinding("Static.End", site), "", "&"+rel, []string{errArg}, "", nil, nil, site) + "\n}\n\n")
	// A slot released inside the body (slotRelease): its release alone,
	// whose defect leaves at once (the emitter's staticRelease).
	for _, s := range o.slots {
		if s.helper == "" {
			continue
		}
		slot := l.names.alloc("slot")
		t.WriteString("// " + s.helper + " releases one value of " + name + "'s static frame inside its body (IR, FLIP F7).\n")
		t.WriteString("func " + s.helper + "(" + env + " *" + l.env() + ", " + slot + " *" + fc.goType(s.t) + ") {\n\t")
		t.WriteString(fc.providerRelease(&check.Acquisition{Close: s.d.Close}, env, "(*"+slot+")", site) + "\n}\n\n")
	}
	l.gen.WriteString(t.String())
}
