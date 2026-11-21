package lower

// IR builder: the bounds facts (bounds.go, BCE1) on the IR (SLICE-BOUNDS).
// The emitter proves a string's or a slice's index, and a substring, in
// range from the comparisons that dominate it, and the run build then
// reads it through a reviewed unchecked body of runtime/v1 (ByteAt,
// AddrAt, StringAt, StringN; unchecked.go). The IR builder keeps the same
// facts in the same funcCtx (fc.bounds), at the same places the emitter
// does, so a proved access is the same unchecked call on both paths (the
// Go floor rule: the IR is no slower than the text it replaces):
//   - an if's condition gives its then branch its facts (ifWith), a
//     conditional loop's its body (whileStmt), each reset after;
//   - an int let takes what the facts say of its value (assumeLet);
//   - a write ends the facts over its root: each statement of a list kills,
//     once built, the facts over every local it writes (tailTo), which
//     covers the emitter's kill at each assignment, and a loop kills at
//     its head the facts over what its body writes (killWritten), before
//     its condition is built.
//
// A fact names only a stable local of this body (stable): the IR's locals
// stand in for the emitter's (factLocal). A function literal is a funcCtx
// of its own, with no facts and no IR locals, so nothing proves inside it,
// as the emitter's literal starts with none. Every other body the IR
// builds (a law's, a machine's, a release) has no factLocal: it proves
// nothing, which keeps Go's checks.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// factLocals makes the builder's locals the facts' locals (stable).
func (b *irBuilder) factLocals() {
	b.fc.factLocal = func(v *check.Var) bool {
		_, ok := b.locals[v]
		return ok
	}
}

// uncheckedIndex is the read x, S[i] of a string or a slice, through the
// runtime's unchecked body when the facts prove it in range (the
// emitter's index): ByteAt(s, i), or *AddrAt[T](xs, i). ok is false for
// any other read, which is Go's checked index.
func (b *irBuilder) uncheckedIndex(x *syntax.IndexExpr, p irPos) (*irExpr, bool) {
	if len(x.Indices) != 1 || !b.fc.provedIndex(x) { // bounds.go
		return nil, false
	}
	b.fc.indexFact(x) // facts.go
	if !b.fc.uncheckedOK() {
		return nil, false
	}
	var name, targs string
	switch t := b.l.info.Types[x.X].(type) {
	case *check.Slice:
		name, targs = "AddrAt", b.goType(t.Elem, x)
	default:
		if !isGoString(t) { // bounds.go: a string or a mapped text, Go's string
			return nil, false
		}
		name = "ByteAt"
	}
	b.l.took("bounds-unchecked")
	b.keepImport("var _ = " + b.l.rt() + "." + name + map[bool]string{true: "[" + targs + "]", false: ""}[targs != ""])
	xs := b.inOrder([]syntax.Expr{x.X, x.Indices[0]}) // ir_order.go
	call := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, V: targs, Args: xs}
	if name == "AddrAt" {
		return &irExpr{K: "deref", Pos: p, X: call}, true
	}
	return call, true
}

// uncheckedCut is the substring x, s[a:b] of a string, through the
// runtime's unchecked body when the facts prove it in range (the
// emitter's sliceExpr): StringN(s, a, n) for a cut of a constant length
// n, else StringAt(s, a, b), a missing bound 0 or len(s).
func (b *irBuilder) uncheckedCut(x *syntax.SliceExpr, p irPos) (*irExpr, bool) {
	if !b.fc.provedSlice(x) { // bounds.go
		return nil, false
	}
	b.fc.cutFact(x) // facts.go
	if !b.fc.uncheckedOK() {
		return nil, false
	}
	if !isGoString(b.l.info.Types[x.X]) { // bounds.go: a string or a mapped text
		return nil, false
	}
	b.l.took("bounds-unchecked")
	if n := b.fc.sliceLen(x); n > 0 {
		b.keepImport("var _ = " + b.l.rt() + ".StringN")
		xs := b.inOrder([]syntax.Expr{x.X, x.Low}) // ir_order.go
		return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "StringN", Args: append(xs, &irExpr{K: "const", Pos: p, T: "int", V: strconv.FormatInt(n, 10)})}, true
	}
	list := []syntax.Expr{x.X}
	for _, e := range []syntax.Expr{x.Low, x.High} {
		if e != nil {
			list = append(list, e)
		}
	}
	xs := b.inOrder(list) // ir_order.go
	// The subject is a local (bounds.go, subject): read again for a
	// missing high bound, with no effect.
	s, k := xs[0], 1
	low := &irExpr{K: "const", Pos: p, T: "int", V: "0"}
	if x.Low != nil {
		low, k = xs[k], k+1
	}
	sc := *s // its own node: an IR node is never shared
	high := &irExpr{K: "len", Pos: p, X: &sc}
	if x.High != nil {
		high = xs[k]
	}
	b.keepImport("var _ = " + b.l.rt() + ".StringAt")
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "StringAt", Args: []*irExpr{s, low, high}}, true
}
