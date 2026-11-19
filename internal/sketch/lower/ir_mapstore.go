package lower

// IR builder: a store to a Map's entry (FLIP F7 LY-PLACE), m[k] = v and
// m[k] op= v. A Map write is total (D-D4, the Map rule): it inserts or
// replaces the entry, as the emitter's target writes it (body.go,
// m[k] = v on the Go map). Go cannot take an entry's address, so the
// entry is no place (isPlace) and the IR names it with its own kind,
// "mapAt", which the reader takes only as a store's target: the map's
// value and the key after its implicit conversion (a variant into a flat
// union's key, as the emitter's assigned).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// storeTo is the target of a store to lhs, a Map's entry or a place
// (ir_place.go), after the statements its map and key, or the place's
// operands, run first. A key that runs statements (a call hoisted to a
// temporary, FLIP F7 PORT-TEMP) runs them after the map is bound to a
// temporary, so they cannot move it: the emitter's target spills the map
// when the index emits (body.go), and runs the index's statements after.
// A slice's element orders its slice and index as a read of it does
// (ir_expr.go's inOrder): the slice is spilled before an index that runs
// statements.
func (b *irBuilder) storeTo(lhs syntax.Expr) ([]*irStmt, *irExpr) {
	place := func() ([]*irStmt, *irExpr) { return nil, b.place(lhs) }
	x, ok := unparen(lhs).(*syntax.IndexExpr)
	if !ok || len(x.Indices) != 1 {
		return b.collect(place)
	}
	m, isMap := b.l.info.Types[x.X].(*check.Map)
	if !isMap {
		return b.collect(place)
	}
	b.goType(m, x)
	pre, to := b.collect(func() ([]*irStmt, *irExpr) { return nil, b.expr(x.X) })
	keyPre, key := b.collect(func() ([]*irStmt, *irExpr) { return nil, b.expr(x.Indices[0]) })
	if len(keyPre) > 0 {
		v := b.temp("m", b.goType(m, x.X), to.Pos)
		pre = append(pre, &irStmt{K: "def", Pos: to.Pos, Local: v, X: to})
		to = &irExpr{K: "local", Pos: to.Pos, I: v}
	}
	return append(pre, keyPre...), &irExpr{K: "mapAt", Pos: b.pos(x, x.Pos()), X: to, Y: key}
}

// fixEntry is the statements of a value stored to the Map entry to, lhs
// in the source: the emitter's target with later set (body.go) binds
// the map and a key that is no constant to temporaries before the
// value's statements run, so they cannot move the entry. It is pre
// unchanged for any other target, or when pre is empty.
func (b *irBuilder) fixEntry(lhs syntax.Expr, to *irExpr, pre []*irStmt) []*irStmt {
	if to.K != "mapAt" || len(pre) == 0 {
		return pre
	}
	x := unparen(lhs).(*syntax.IndexExpr)
	var fix []*irStmt
	m := b.l.info.Types[x.X].(*check.Map)
	bind := func(e *irExpr, t check.Type, s syntax.Expr) *irExpr {
		v := b.temp("m", b.goType(t, s), e.Pos)
		fix = append(fix, &irStmt{K: "def", Pos: e.Pos, Local: v, X: e})
		return &irExpr{K: "local", Pos: e.Pos, I: v}
	}
	to.X = bind(to.X, m, x.X)
	if to.Y.K != "const" {
		// The key after its conversion: the map's key type.
		to.Y = bind(to.Y, m.Key, x.Indices[0])
	}
	return append(fix, pre...)
}
