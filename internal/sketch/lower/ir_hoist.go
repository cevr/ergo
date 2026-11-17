package lower

// IR builder: a record parameter's fields read in a loop (VW1b).
//
// Go keeps a struct of more than four fields in memory (it is not
// SSA-able), so a loop that reads p.f of such a parameter p loads the field
// again each pass, and across a switch of many cases Go's register
// allocator reloads every field the cases read before the jump. B2's view
// walk read five slice headers of its Tree so: 13 of its 39.6 instructions
// per row over the hand Go control, which keeps the columns in registers.
//
// A parameter is immutable: the checker refuses an assignment or a borrow
// rooted at one ("the parameter p cannot be reassigned; copy it into a
// local: let mut p_ = p"), so a body that rebinds it, in a loop or not,
// rebinds a let mut copy, a variable of its own that is never hoisted. Only
// a parameter's own local is hoisted (params), never a let. An owned
// parameter is no exception: it is written in place only through the let
// mut that takes it over, and once taken it is not read again. So each field
// read of it inside a loop reads the field's value at entry: the field is
// read once, into a local, at the function's entry (entry), and the loop
// reads the local. Only a field of a type Go holds in registers is hoisted
// (a basic value, a slice, a map, a pointer); a record or an array field
// would be copied for nothing. Reading the field at entry cannot fail (the
// parameter is a value, not a pointer), so a loop that never runs reads it
// for nothing and nothing else changes.
//
// The reference is hoistLowering "off": every field read stays where it is.
// The IR agreement properties (TestIRViewsMatchSource's records among them)
// run under it against the source path.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

var hoistLowering string

type hoistKey struct {
	local int
	field string
}

// hoistField is the local holding field of the parameter x.X reads, built
// at the function's entry, or nil when the read stays.
func (b *irBuilder) hoistField(x *syntax.SelectorExpr, field string) *irExpr {
	if hoistLowering == "off" || b.loops == 0 || b.fn == nil {
		return nil
	}
	id, ok := unparen(x.X).(*syntax.Ident)
	if !ok {
		return nil
	}
	w, ok := b.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil
	}
	// Only a parameter's own local: the checker refuses assigning a
	// parameter, so a rebinding (std cache's `let mut u = t`, then u = f(u)
	// in a loop) rebinds a let mut, which is never hoisted.
	l, ok := b.locals[w]
	if !ok || !b.params[l] {
		return nil
	}
	if _, ok := b.l.info.Types[x.X].(*check.Named); !ok {
		return nil // a pointer to a record may see a write
	}
	ft := b.l.info.Types[x]
	switch ft.(type) {
	case *check.Basic, *check.Slice, *check.Map, *check.Pointer:
	default:
		return nil
	}
	k := hoistKey{l, field}
	h, ok := b.hoisted[k]
	if !ok {
		p := b.fn.Pos
		h = b.temp(w.Name+"_"+field, b.goType(ft, x), p)
		if b.hoisted == nil {
			b.hoisted = map[hoistKey]int{}
		}
		b.hoisted[k] = h
		b.entry = append(b.entry, &irStmt{K: "def", Pos: p, Local: h, X: &irExpr{K: "field", Pos: p, Name: field, X: &irExpr{K: "local", Pos: p, I: l}}})
	}
	return &irExpr{K: "local", Pos: b.pos(x.Sel, x.Sel.Pos()), I: h}
}

// withEntry is body with the entry's field reads put at its start, after
// the first n statements (the entry poll).
func (b *irBuilder) withEntry(body []*irStmt, n int) []*irStmt {
	if len(b.entry) == 0 {
		return body
	}
	out := append([]*irStmt{}, body[:n]...)
	out = append(out, b.entry...)
	return append(out, body[n:]...)
}
