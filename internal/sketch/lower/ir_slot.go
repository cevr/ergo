package lower

// IR builder: the slot variant (UNI2; slot.go) on the IR.
//
// A union result its caller only matches goes to the callee's slot
// variant, which writes the variant into one of the caller's slots and
// returns its tag, so the variant is never boxed into the union's Go
// interface (slot.go has the rule and its prior art). The IR has both
// halves the emitter has:
//
//   - the site: the subject of a simple match, `match [try] f() {…}`,
//     calls f's slot variant with a slot local per variant, and the match
//     switches on the tag it returns, each case reading its slot (an
//     irScalar, as a scalar-form local's match does: ir_match.go);
//   - the body: the slot variant itself, f's body with the slot pointers
//     after its parameters, each return writing its value into its
//     variant's slot and returning the tag (the emitter's slotReturn):
//     a value of a variant's type is stored whole; a call of a slot
//     variant of the same union passes the slots on; a scalar-form local
//     copies its tag's slot; any other union value is taken apart by a
//     type switch.
//
// The callee qualifies by the emitter's rule (slotCallee) and the site by
// the shortcut's receipt ("uni-slot"), so the IR calls the slot variant
// where the text path would, decided by declared facts alone.

import (
	"slices"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irSlotOut is a slot variant body's slots: the parameter locals of the
// slot pointers, by variant, and the union.
type irSlotOut struct {
	slots    []int
	union    check.Type
	variants []check.Type
	rets     map[*irStmt]bool // the returns slotRet wrote (slotRetsOnly)
}

// slotTarget is the slot variant of the callee of call when the call may
// take it for a result of the union u (the emitter's slotAdmit): a plain
// function of this body's package that slotCallee admits, whose result is
// u. It names the slot variant, which queues its body.
func (b *irBuilder) slotTarget(call *syntax.CallExpr, u check.Type) (name string, fd *funcDecl, ok bool) {
	if call == nil || slotLowering == "off" || b.fc.goGeneric || call.Trailing != nil {
		return "", nil, false
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Func.Pkg != b.l.bodyPkg() {
		return "", nil, false
	}
	fd = b.l.funcDecl(c.Func)
	if fd == nil || !b.l.slotCallee(c.Func, fd) || b.goType(c.Func.Sig.Results[0], call) != b.goType(u, call) {
		return "", nil, false
	}
	if b.copyName(c.Func) != "" || b.monoName(c.Func) != "" {
		return "", nil, false
	}
	if !b.l.takes("uni-slot") {
		return "", nil, false
	}
	b.l.took("uni-slot")
	return b.l.slotName(c.Func, fd), fd, true
}

// slotCallExpr is the call of the slot variant name in place of call,
// with the slot pointers args after its own operands. ok is false when
// the call's IR form is not the plain call of fd's body the slot variant
// stands for.
func (b *irBuilder) slotCallExpr(call *syntax.CallExpr, name string, fd *funcDecl, args []*irExpr) ([]*irStmt, *irExpr) {
	pre, e := b.rawCall(call)
	if e == nil || e.K != "call" || e.Name != fd.name {
		irNo("slot site of another call form")
	}
	y := *e
	y.Name = name
	y.Args = append(slices.Clone(e.Args), args...)
	return pre, &y
}

// slotSubject is the subject x of a simple match on a union u, when it is
// a call that takes its callee's slot variant: the slot locals and the
// tag, as an irScalar the match reads, and the statements that write
// them (a fallible call's failure leaves first, as try does).
func (b *irBuilder) slotSubject(x syntax.Expr, u check.Type) ([]*irStmt, *irScalar, bool) {
	call := joinCall(x)
	if call == nil {
		return nil, nil, false
	}
	if _, try := unparen(x).(*syntax.TryExpr); !try && b.fallibleCall(x) != nil {
		return nil, nil, false // an untried failure: no subject of a match
	}
	n, ok := u.(*check.Named)
	if !ok {
		return nil, nil, false
	}
	un, ok := n.Underlying().(*check.Union)
	if !ok {
		return nil, nil, false
	}
	name, fd, ok := b.slotTarget(call, u)
	if !ok {
		return nil, nil, false
	}
	p := b.pos(x, x.Pos())
	sc := &irScalar{union: u, variants: un.Variants}
	var out []*irStmt
	var args []*irExpr
	for _, vt := range un.Variants {
		s := b.temp("m", b.goType(vt, x), p)
		sc.slots = append(sc.slots, s)
		out = append(out, &irStmt{K: "var", Pos: p, Local: s})
		args = append(args, b.addrOf(s, p))
	}
	pre, e := b.slotCallExpr(call, name, fd, args)
	out = append(out, pre...)
	sc.tag = b.temp("m", "uint8", p)
	if b.fallibleCall(call) == nil {
		out = append(out, &irStmt{K: "def", Pos: p, Local: sc.tag, X: e})
		return out, sc, true
	}
	err := b.temp("err", "error", p)
	out = append(out, &irStmt{K: "def2", Pos: p, Local: sc.tag, Val: &err, X: e}, b.propagate(err, p))
	return out, sc, true
}

// slotBody marks b's body as fd's slot variant: its slot pointers are the
// parameter locals after the Ergo parameters (the emitter's slotParams).
func (b *irBuilder) slotBody(fc *funcCtx) {
	n, _ := b.result.(*check.Named)
	if n == nil {
		irNo("slot variant without a union result")
	}
	u, _ := n.Underlying().(*check.Union)
	if u == nil || fc.frame.slot == nil || len(fc.frame.slot.variants) != len(u.Variants) {
		irNo("slot variant without a union result")
	}
	out := &irSlotOut{union: b.result, variants: u.Variants, rets: map[*irStmt]bool{}}
	for range u.Variants {
		out.slots = append(out.slots, b.next)
		b.next++
	}
	b.slotOut = out
}

// slotRet is the return of x from a slot variant (the emitter's
// slotReturn).
func (b *irBuilder) slotRet(x syntax.Expr, p irPos) []*irStmt {
	out := b.slotOut
	ret := func(tag int) *irStmt {
		s := &irStmt{K: "ret", Pos: p, X: &irExpr{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(tag)}}
		out.rets[s] = true
		return s
	}
	slotArgs := func() []*irExpr {
		var args []*irExpr
		for _, s := range out.slots {
			args = append(args, &irExpr{K: "local", Pos: p, I: s})
		}
		return args
	}
	if call := b.fallibleCall(x); call != nil {
		if name, fd, ok := b.slotTarget(call, out.union); ok && call == unparen(x) && !implicitAt(b.l.info, x) {
			// return f(), f's slot variant: its tag and failure.
			pre, e := b.slotCallExpr(call, name, fd, slotArgs())
			s := &irStmt{K: "ret", Pos: p, X: e, Pair: true}
			out.rets[s] = true
			return append(pre, s)
		}
		pre, v, e := b.bind(call)
		if v < 0 {
			irNo("slot variant return of a call without a value")
		}
		pre = append(pre, b.propagate(e, p))
		return append(pre, b.slotScatter(b.implicit(x, &irExpr{K: "local", Pos: p, I: v}), x, ret, p)...)
	}
	if src := b.scalarIdent(x); src != nil && b.sameVariants(src.variants, out.variants, x) {
		// A scalar-form local: its slot by its tag.
		sw := &irStmt{K: "switch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: src.tag}}
		for k := range out.variants {
			sw.Cases = append(sw.Cases, &irCase{Pos: p, Vals: []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}}, Body: []*irStmt{
				{K: "store", Pos: p, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: out.slots[k]}}, Y: &irExpr{K: "local", Pos: p, I: src.slots[k]}},
				ret(k),
			}})
		}
		sw.Else = []*irStmt{b.unionDefect(out.union, x, p)}
		return []*irStmt{sw}
	}
	if t := b.l.info.Types[x]; t != nil {
		g := b.goType(t, x)
		for k, vt := range out.variants {
			if b.goType(vt, x) != g {
				continue
			}
			// A value of a variant's type: stored whole, never boxed.
			saved := b.raw
			b.raw = x
			pre, v := b.value(x)
			b.raw = saved
			pre = append(pre, &irStmt{K: "store", Pos: p, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: out.slots[k]}}, Y: v})
			return append(pre, ret(k))
		}
	}
	if call := joinCall(x); call != nil && !implicitAt(b.l.info, x) {
		_, tried := unparen(x).(*syntax.TryExpr)
		if name, fd, ok := b.slotTarget(call, out.union); ok && (tried || call == unparen(x)) {
			// return [try] f(), f's slot variant: it writes the slots,
			// and a tried one's failure leaves first.
			pre, e := b.slotCallExpr(call, name, fd, slotArgs())
			if !tried {
				s := &irStmt{K: "ret", Pos: p, X: e}
				out.rets[s] = true
				return append(pre, s)
			}
			tag, err := b.temp("tag", "uint8", p), b.temp("err", "error", p)
			pre = append(pre, &irStmt{K: "def2", Pos: p, Local: tag, Val: &err, X: e}, b.propagate(err, p))
			s := &irStmt{K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: tag}}
			out.rets[s] = true
			return append(pre, s)
		}
	}
	pre, v := b.value(x)
	return append(pre, b.slotScatter(v, x, ret, p)...)
}

// slotScatter writes the union value e into the slot of its variant by a
// type switch, each case returning its tag (the emitter's slotScatter).
func (b *irBuilder) slotScatter(e *irExpr, site syntax.Node, ret func(int) *irStmt, p irPos) []*irStmt {
	out := b.slotOut
	gu := b.goType(out.union, site)
	x := b.temp("x", gu, p)
	sw := &irStmt{K: "tswitch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: x}}
	for k, vt := range out.variants {
		gv := b.goType(vt, site)
		sw.Cases = append(sw.Cases, &irCase{Pos: p, Types: []string{gv}, Body: []*irStmt{
			{K: "store", Pos: p, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: out.slots[k]}}, Y: &irExpr{K: "assert", Pos: p, T: gv, X: &irExpr{K: "local", Pos: p, I: x}}},
			ret(k),
		}})
	}
	sw.Else = []*irStmt{b.unionDefect(out.union, site, p)}
	return []*irStmt{{K: "def", Pos: p, Local: x, X: e}, sw}
}

// unionDefect is the emitter's panic for a union value that is none of
// its variants.
func (b *irBuilder) unionDefect(u check.Type, site syntax.Node, p irPos) *irStmt {
	msg := "ergo: defect: a value of " + b.goType(u, site) + " that is none of its variants"
	return &irStmt{K: "panic", Pos: p, X: &irExpr{K: "const", Pos: p, T: "string", V: msg}}
}

// sameVariants reports two variant lists of the same Go types, in order.
func (b *irBuilder) sameVariants(a, c []check.Type, site syntax.Node) bool {
	if len(a) != len(c) {
		return false
	}
	for i := range a {
		if b.goType(a[i], site) != b.goType(c[i], site) {
			return false
		}
	}
	return true
}

// slotRetsOnly refuses a slot variant body with a return slotRet did not
// write: its Go result is the tag, which only slotRet returns.
func (b *irBuilder) slotRetsOnly(list []*irStmt) {
	for _, s := range list {
		if s == nil {
			continue
		}
		if s.K == "ret" && !b.slotOut.rets[s] {
			irNo("slot variant return of another form")
		}
		b.slotRetsOnly(s.Then)
		b.slotRetsOnly(s.Else)
		b.slotRetsOnly(s.Body)
		for _, c := range s.Cases {
			b.slotRetsOnly(c.Body)
		}
	}
}

// irSlotLowering selects the slot variant on the IR: "" takes its sites
// and builds its body; "off" keeps every site's box and the body the
// emitter's Go text, the reference before IR-FALLBACK-REMAINDER r4. Set
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.irSlotLowering=off.
var irSlotLowering string
