package lower

// IR builder: places (FLIP F2). A place is where a value lives: a local,
// *p, a field of a place or through a pointer, a slice's element, and
// any chain of them, as the emitter's target (body.go) and isPlace take
// one. The IR writes to a place (a "store", p = v and p op= v), takes its
// address ("addr", &p) and passes it as the receiver of a *T function
// called on a T. Each is the place itself, never a copy of its value: an
// address of a copy would write the copy (the mutant ir-place-addr-copy).
// A receiver that is no place (a call's result) is a copy in the emitter
// too: its value in a temporary, whose address Go can take.
//
// The emitter fixes a place's pointers and indexes in temporaries before
// a value that writes statements (target's later), so a defect of the
// place (an index out of range, a nil pointer) comes after those
// statements. The IR does that only for a place it need not fix: a chain
// of fields from a local, whose address no statement moves. Any other
// place before a value with statements keeps its source body.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// place is x as an IR place.
func (b *irBuilder) place(x syntax.Expr) *irExpr {
	x = unparen(x)
	if id, ok := x.(*syntax.Ident); ok && b.l.info.Handoffs[id] != nil {
		irNo("place of a handed-off local") // a Close through its address
	}
	if sel, ok := x.(*syntax.SelectorExpr); ok {
		if _, taken := b.l.info.FieldTakes[sel]; taken {
			irNo("place of a taken field") // takeField (release.go)
		}
	}
	if _, ok := b.l.info.Implicit[x]; ok {
		irNo("place with a conversion")
	}
	if !b.fc.isPlace(x) {
		irNo("place that is not addressable")
	}
	e := b.exprOf(x)
	switch e.K {
	case "local", "global", "deref", "index", "field":
		// isPlace checked the chain: a field of a value that is no
		// place goes through a pointer.
		return e
	}
	irNo("place of the IR kind %s", e.K)
	return nil
}

// fixed reports a place no statement can move: a local, or a field of a
// fixed place that is not reached through a pointer.
func (b *irBuilder) fixed(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := b.l.info.Uses[x].(*check.Var)
		_, local := b.locals[v]
		return ok && local
	case *syntax.SelectorExpr:
		if _, isPtr := b.l.info.Types[x.X].(*check.Pointer); isPtr {
			return false
		}
		return b.fixed(x.X)
	}
	return false
}

// store is the IR of p = v or p op= v, p a place that is not a name.
func (b *irBuilder) store(s *syntax.AssignStmt, op string) []*irStmt {
	lhs := s.Lhs[0]
	if op == "" && b.fc.mapStoreForm(s) {
		// p = p.Put(…) with the call writing p's own Go map in place: the
		// map it returns is p's, so the call is the statement and the store
		// back is dropped, as the emitter's mapStore writes it (forms.go).
		// Any other call the value rule writes in place (Append, a Map
		// call that p does not hand) is stored back as written: the call's
		// own IR is the in-place form (valueSource).
		return b.discard(s.Rhs[0])
	}
	if op != "" {
		b.operator(op, b.l.info.Types[lhs], s)
	}
	to := b.place(lhs)
	pre, x := b.value(s.Rhs[0])
	if len(pre) > 0 && (op != "" || !b.fixed(lhs)) {
		irNo("store to a place a value with statements may move")
	}
	return append(pre, &irStmt{K: "store", Pos: b.pos(s, s.Pos()), X: to, Y: x, Op: op})
}

// addrRecv reports whether c calls a *T function on a T receiver, which
// passes the receiver's address (calls.go: addr).
func (b *irBuilder) addrRecv(c *check.Call) bool {
	if c.Recv == nil || !c.Func.Ptr {
		return false
	}
	_, isPtr := b.l.info.Types[c.Recv].(*check.Pointer)
	return !isPtr
}

// recvAddrCall is the operands of a *T function called on a T: the
// receiver's address first, then args (the emitter's place).
func (b *irBuilder) recvAddrCall(c *check.Call, args []syntax.Expr) ([]*irStmt, []*irExpr) {
	p := b.pos(c.Recv, c.Recv.Pos())
	var pre []*irStmt
	var recv *irExpr
	isPlace := b.fc.isPlace(unparen(c.Recv))
	if isPlace {
		recv = &irExpr{K: "addr", Pos: p, X: b.place(c.Recv)}
	} else {
		// A value that is no place: in a temporary, whose address Go
		// can take; the call writes the temporary.
		var v *irExpr
		pre, v = b.value(c.Recv)
		t := b.temp("r", b.goType(b.l.info.Types[c.Recv], c.Recv), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: t, X: v})
		recv = &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: t}}
	}
	argPre, vals := b.operands(args)
	if len(argPre) > 0 && isPlace && !b.fixed(c.Recv) {
		irNo("call on a receiver's address that operands with statements may move")
	}
	return append(pre, argPre...), append([]*irExpr{recv}, vals...)
}
