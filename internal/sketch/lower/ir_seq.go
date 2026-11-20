package lower

// IR builder: a for-in over a Seq[T] (PORT-FORIN). The IR writes the
// emitter's loop (body.go, rangeStmt): the Seq, bound once before the
// loop, adapted at the loop to Go's func(yield func(T) bool),
//
//	for v := range func(yield func(T) bool) {
//		_, _ = seq(env, func(_ *Env, v T) (bool, error) { return yield(v), nil })
//	} { body }
//
// and the patched compiler hands the loop to Go's own range-over-func
// rewrite (toolchain/ir: its "range" takes a func, and rangefunc.Rewrite
// runs again over the IR bodies). break, continue, return, a failure and
// the back-edge poll then mean what they mean in the emitter's loop, and
// a Seq that calls yield after it returned false panics as Go's does
// there. The adapter is a literal of the body, at the Seq's position, as
// the emitter's is: a defect's report reads its frame as the body's call
// of the Seq.
//
// A Seq has no position: its one binding is the element, the range's
// first variable (Key).
//
// The reference is irSeqRangeLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irSeqRangeLowering=off):
// a Seq loop keeps its source body ("for-in over a non-slice").

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irSeqRangeLowering is "off" for the reference: a Seq loop falls back.
var irSeqRangeLowering string

// IRSeqRangeOn reports that a Seq loop lowers on the IR (the property's
// reach check).
func IRSeqRangeOn() bool { return irSeqRangeLowering != "off" }

// seqLoop reports a for-in over a Seq that lowers on the IR.
func (b *irBuilder) seqLoop(s *syntax.RangeStmt) bool {
	lp := b.l.info.Loops[s]
	return lp != nil && lp.Domain == check.LoopSeq && irSeqRangeLowering != "off"
}

// seqRange is the range domain of the Seq loop s over the Seq value x:
// the statement that binds x once, and the adapter literal.
func (b *irBuilder) seqRange(s *syntax.RangeStmt, lp *check.Loop, x *irExpr) ([]*irStmt, *irExpr) {
	if b.env < 0 {
		irNo("call of a function value from a pure function") // as a call of the Seq (ir_closure.go)
	}
	if bound(s.Key) && bound(s.Value) {
		irNo("for-in pattern") // the checker refuses a Seq's position (loops.go)
	}
	p := b.pos(s.X, s.X.Pos())
	end := p
	env := "*" + b.l.env()
	elem := b.goType(lp.Elem, s)
	yieldT := "func(" + elem + ") bool"
	seq := b.temp("seq", b.goType(b.l.info.Types[s.X], s.X), p)
	pre := []*irStmt{{K: "def", Pos: p, Local: seq, X: x}}
	// The callback: func(_ *Env, v T) (bool, error) { return yield(v), nil }.
	// Its call of yield is at the loop body's brace, not the Seq's
	// position: gc refuses to inline a call whose position is already on
	// its inlining stack, as a recursion (inline/inl.go, canInlineCallExpr's
	// parsePos loop), and the call of the Seq above it is at the Seq's
	// position. At one position the loop body stayed a closure that
	// captures its locals, where the emitter's loop inlines it. The brace
	// comes after the Seq, so the literals' scopes still nest in order.
	q := b.pos(s.Body, s.Body.Pos())
	end = q
	yield, ce, v := b.temp("yield", yieldT, p), b.temp("e", env, p), b.temp("v", elem, p)
	cb := &irExpr{K: "func", Pos: p, T: "func(" + env + ", " + elem + ") (bool, error)", Params: []int{ce, v}, End: &end,
		Body: []*irStmt{{K: "ret", Pos: q, X: &irExpr{K: "vcall", Pos: q, X: &irExpr{K: "local", Pos: q, I: yield}, Args: []*irExpr{{K: "local", Pos: q, I: v}}}}}}
	// The adapter: func(yield func(T) bool) { _, _ = seq(env, callback) }.
	r, err := b.temp("r", "struct{}", p), b.temp("err", "error", p)
	call := &irExpr{K: "vcall", Pos: p, X: &irExpr{K: "local", Pos: p, I: seq}, Args: []*irExpr{{K: "local", Pos: p, I: b.env}, cb}}
	adapter := &irExpr{K: "func", Pos: p, T: "func(" + yieldT + ")", Params: []int{yield}, End: &end,
		Body: []*irStmt{{K: "def2", Pos: p, Local: r, Val: &err, X: call}}}
	b.st.report.SeqRanges++
	return pre, adapter
}
