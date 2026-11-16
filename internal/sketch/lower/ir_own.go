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
// The release helper is Go text in the generated part, written from the
// emitter's own release primitives (providerRelease, runtimeCall): the
// IR has no function literals for a frame's release, and Go's recover
// reads the frame's panic only in the deferred function itself. The
// helper takes the frame's state by pointer and reads it when it runs.
// Debt: RELEASE-IR makes the helper a named IR function (no Go text),
// queued before F8; self-hosting does not port this writer.
//
// This first form covers a frame with no error row whose values are
// released only at the frame's exit: a release inside the body (a
// statement's temporaries, a scope's end) stays the emitter's.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
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
}

type irSlot struct {
	local int
	t     check.Type
	d     *check.Drop
}

// irStaticFrame reports a body whose frame the IR builds: the static
// release form with no error row (decls.go lifts the frame's gate).
func irStaticFrame(fc *funcCtx, fallible bool) bool {
	return fc.frame != nil && fc.frame.static != nil && !fallible
}

// openOwn starts the static frame of the body of f, whose parameters are
// params: its live local, its deferred release and its taken parameters'
// registrations, before the entry poll (the emitter's openFrame, ownVar).
func (b *irBuilder) openOwn(f *check.Func, params []*check.Var) []*irStmt {
	if !irStaticFrame(b.fc, b.fc.frame.fallible) {
		return nil
	}
	if b.env < 0 {
		irNo("owned local") // a pure body has no task to settle its exit on
	}
	b.ownBodyChecks(f.Decl.Body)
	p := b.pos(f.Decl, f.Decl.Pos())
	o := &irOwn{kept: -1, bit: map[*check.Var]int{}, site: f.Decl, p: p}
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
		out = append(out, b.ownLet(v, i, f.Decl)...)
	}
	return out
}

// ownBodyChecks stops a static frame this form does not build: a release
// inside the body (Info.Releases, TryReleases), an owned temporary or arm
// value, a record update's replaced fields, a taking receiver passed by
// its address.
func (b *irBuilder) ownBodyChecks(body *syntax.BlockStmt) {
	info := b.l.info
	syntax.Inspect(body, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		if _, lit := n.(*syntax.FuncLit); lit {
			return false // a frame of its own
		}
		if _, stmt := n.(syntax.Stmt); !stmt && len(info.Releases[n]) > 0 {
			// A let's or a scope's end (a block, a case clause): a
			// statement's own is tailTo's (releasesAtExit).
			irNo("statement that releases owned values")
		}
		switch n := n.(type) {
		case *syntax.TryExpr:
			if len(info.TryReleases[n]) > 0 {
				irNo("statement that releases owned values")
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

// ownable stops an owned local outside a static frame the IR builds.
func (b *irBuilder) ownable(v *check.Var) {
	if b.l.info.OwnedVars[v] != nil && b.own == nil {
		irNo("owned local")
	}
}

// ownLet registers the owned local v, bound to the local i, in its slot:
// the step that receives the value (R8; the emitter's ownVar).
func (b *irBuilder) ownLet(v *check.Var, i int, site syntax.Node) []*irStmt {
	d := b.l.info.OwnedVars[v]
	if d == nil {
		return nil
	}
	o := b.own
	if o == nil {
		irNo("owned local")
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
		o.slots = append(o.slots, irSlot{local: b.temp("slot", typ, p), t: v.Type, d: d})
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
	if o == nil {
		return
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
	t.WriteString("\t" + rel + " := " + fc.runtimeCall("StaticBegin", fc.ownBinding("StaticBegin", site), "", "", []string{"nil", "recover()"}, env, nil, nil, site) + "\n")
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
	t.WriteString("\t" + fc.runtimeCall("Static.End", fc.ownBinding("Static.End", site), "", "&"+rel, []string{"nil"}, "", nil, nil, site) + "\n}\n\n")
	l.gen.WriteString(t.String())
}
