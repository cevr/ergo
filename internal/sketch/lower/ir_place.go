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
	if _, ok := b.l.info.Implicit[x]; ok {
		irNo("place with a conversion")
	}
	if !b.fc.isPlace(x) {
		irNo("place that is not addressable")
	}
	if id, ok := x.(*syntax.Ident); ok {
		if v := b.l.info.Handoffs[id]; v != nil && b.l.info.OwnedVars[v] != nil {
			i, local := b.locals[v]
			if !local {
				irNo("place of a handed-off local")
			}
			p := b.pos(id, id.Pos())
			e := &irExpr{K: "local", Pos: p, I: i}
			if hand := b.handoff(id, e); hand != e {
				// Like the text path, relinquish the entry first,
				// but pass the *original place*, not the value Hand
				// returns. A held take keeps the entry for heldCall.
				b.runFirst([]*irStmt{{K: "expr", Pos: p, X: hand}})
			}
			return e
		}
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

// fixed reports a place no statement can move: a local, a field of a
// fixed place that is not reached through a pointer, or *p and a field
// through p where p is a local no assignment changes (a parameter or a
// plain let: check.Var.Mut false, the emitter's fixedName). Statements may
// write *p, never p, so the place's address is the same before them and
// after, and a nil p fails at the store, after them, on both paths (the
// emitter binds p to a temporary first, of the same value).
func (b *irBuilder) fixed(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := b.l.info.Uses[x].(*check.Var)
		_, local := b.locals[v]
		_, held := b.held[v] // *p, p the in-place literal's parameter, which nothing assigns (ir_ownlit.go)
		return ok && (local || held)
	case *syntax.StarExpr:
		return b.fixed(x.X) && b.fc.fixedName(x.X)
	case *syntax.SelectorExpr:
		if _, isPtr := b.l.info.Types[x.X].(*check.Pointer); isPtr {
			return b.fixed(x.X) && b.fc.fixedName(x.X)
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
	first, to := b.storeTo(lhs) // ir_mapstore.go: a Map's entry, else a place
	pre, x := b.value(s.Rhs[0])
	if to.K == "mapAt" { // storeTo's: lhs is a Map's index
		_, intMap := b.intMapOf(b.l.info.Types[unparen(lhs).(*syntax.IndexExpr).X]) // ir_intmap.go
		if intMap && op != "" {
			irNo("int map entry op-assignment")
		}
		if op == "" {
			// One statement list for both of a Map's representations: the
			// target's own statements (a key's call), then the entry fixed
			// and the value's (ir_mapstore.go). Only the store differs: Go's
			// m[k] = v, or the int map's m.Put(k, v).
			body := append(first, b.fixEntry(lhs, to, pre)...)
			p := b.pos(s, s.Pos())
			if intMap {
				return append(body, intMapStore(p, to, x))
			}
			return append(body, &irStmt{K: "store", Pos: p, X: to, Y: x})
		}
	}
	if len(pre) > 0 && (op != "" || !b.fixed(lhs)) {
		// The emitter's target with later set (body.go): the place's
		// pointers, slices, maps and indexes are fixed first, and an
		// op-assignment reads the place before the value (assign's old).
		first = append(first, b.fixPlace(lhs, to)...)
		if op != "" {
			return append(first, b.storeOld(s, op, to, pre, x)...)
		}
	}
	if t := b.l.info.Types[lhs]; op == "*" && roundsFloat(t) {
		// p *= v of floats is p = T(p * v): the product rounds
		// (ir_float.go). p is read twice, so it must be a place no
		// evaluation moves.
		if to.K == "mapAt" || !b.fixed(lhs) {
			irNo("float product op-assignment to a place that is not fixed")
		}
		p := b.pos(s, s.Pos())
		return append(append(first, pre...), &irStmt{K: "store", Pos: p, X: to, Y: b.rounded(s, nil, t, &irExpr{K: "bin", Pos: p, Op: op, X: b.expr(lhs), Y: x})})
	}
	return append(append(first, pre...), &irStmt{K: "store", Pos: b.pos(s, s.Pos()), X: to, Y: x, Op: op})
}

// fixPlace binds the operands that pick the place to (lhs in the source)
// to temporaries, as the emitter's target does when something after the
// place writes statements (body.go, later): *p's p; a field through a
// pointer the pointer, unless no assignment changes it (fixedName); a
// slice's element its slice and an index that is no constant (a Map's
// entry is fixEntry's); an array's element its array place and its
// index. Statements that run after it cannot move the place, and an index
// out of range fails at the store, after them, on both paths (FLIP F7
// PORT-TEMP).
func (b *irBuilder) fixPlace(lhs syntax.Expr, to *irExpr) []*irStmt {
	var fix []*irStmt
	bind := func(e *irExpr, s syntax.Expr) *irExpr {
		if e.K == "const" {
			return e
		}
		v := b.temp("p", b.goType(b.operandType(s), s), e.Pos)
		fix = append(fix, &irStmt{K: "def", Pos: e.Pos, Local: v, X: e})
		return &irExpr{K: "local", Pos: e.Pos, I: v}
	}
	var walk func(x syntax.Expr, e *irExpr)
	walk = func(x syntax.Expr, e *irExpr) {
		x = unparen(x)
		switch x := x.(type) {
		case *syntax.Ident:
			return // a local or a global: the name is the place
		case *syntax.StarExpr:
			if e.K == "deref" {
				e.X = bind(e.X, x.X)
				return
			}
		case *syntax.SelectorExpr:
			if e.K == "field" {
				if _, isPtr := b.l.info.Types[x.X].(*check.Pointer); !isPtr {
					walk(x.X, e.X)
				} else if !b.fc.fixedName(x.X) {
					e.X = bind(e.X, x.X)
				}
				return
			}
		case *syntax.IndexExpr:
			if e.K == "index" {
				if _, isArray := b.l.info.Types[x.X].(*check.Array); isArray {
					walk(x.X, e.X)
				} else {
					e.X = bind(e.X, x.X)
				}
				e.Y = bind(e.Y, x.Indices[0])
				return
			}
		}
		irNo("store to a place of the IR kind %s", e.K)
	}
	walk(lhs, to)
	return fix
}

// storeOld is p op= v after fixPlace, for a value v (x) that runs
// statements (pre): the emitter's assign reads the place first, old := p,
// then runs v's statements and stores p = old op v; a float product
// rounds there, T(old * v) (floatLowering).
func (b *irBuilder) storeOld(s *syntax.AssignStmt, op string, to *irExpr, pre []*irStmt, x *irExpr) []*irStmt {
	p := b.pos(s, s.Pos())
	t := b.l.info.Types[s.Lhs[0]]
	gt := b.goType(t, s)
	read := *to // never a Map's entry: a Map read may miss, so the checker refuses m[k] op= v
	old := b.temp("old", gt, p)
	val := &irExpr{K: "bin", Pos: p, Op: op, X: &irExpr{K: "local", Pos: p, I: old}, Y: x}
	if op == "*" && floatLowering != "fuse" && isFloatType(t) {
		val = &irExpr{K: "conv", Pos: p, T: gt, X: val}
	}
	out := append([]*irStmt{{K: "def", Pos: p, Local: old, X: &read}}, pre...)
	return append(out, &irStmt{K: "store", Pos: p, X: to, Y: val})
}

// addrRecv reports whether c calls a *T function on a T receiver, which
// passes the receiver's address (calls.go: addr).
func (b *irBuilder) addrRecv(c *check.Call) bool {
	if !c.BorrowsReceiver() {
		return false
	}
	// In a copy, a type parameter is its bound type: a witness copy's S
	// bound to *Screen is a pointer already (G4, examples/vt).
	_, isPtr := b.fc.boundType(b.l.info.Types[c.Recv]).(*check.Pointer)
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
		pre, recv = b.collect(func() ([]*irStmt, *irExpr) {
			return nil, &irExpr{K: "addr", Pos: p, X: b.place(c.Recv)}
		})
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
		// The emitter's place takes &target(x, later): the receiver's
		// pointers, slices and indexes are fixed before the operands'
		// statements, so they cannot move it (fixPlace, FLIP F7 PORT-TEMP).
		pre = append(pre, b.fixPlace(c.Recv, recv.X)...)
	}
	return append(pre, argPre...), append([]*irExpr{recv}, vals...)
}
