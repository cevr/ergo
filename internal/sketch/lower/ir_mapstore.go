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

// storeTo is the target of a store to lhs: a Map's entry, or a place
// (ir_place.go).
func (b *irBuilder) storeTo(lhs syntax.Expr) *irExpr {
	x, ok := unparen(lhs).(*syntax.IndexExpr)
	if !ok || len(x.Indices) != 1 {
		return b.place(lhs)
	}
	if _, isMap := b.l.info.Types[x.X].(*check.Map); !isMap {
		return b.place(lhs)
	}
	b.goType(b.l.info.Types[x.X], x)
	return &irExpr{K: "mapAt", Pos: b.pos(x, x.Pos()), X: b.expr(x.X), Y: b.expr(x.Indices[0])}
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
