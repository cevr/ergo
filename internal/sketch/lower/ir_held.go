package lower

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// heldOperands finds the local handoffs that keep their owner through a
// call with no ownership frame of its own. Match the checker's holdsTaken:
// a runtime binding, or a generic function that only forwards to one.
func (b *irBuilder) heldOperands(x *syntax.CallExpr, c *check.Call) []*syntax.Ident {
	if c == nil {
		return nil
	}
	args := x.Args
	var takes []int
	switch {
	case c.Kind == check.CallOp && c.Op != nil && c.Op.Sig != nil && len(c.Op.Sig.Takes) > 0 && c.Recv != nil:
		if check.OpBodyOf(b.l.info.Types[c.Recv], c.Op.Name).Runtime == "" {
			return nil
		}
		takes = c.Op.Sig.Takes
	case c.Kind == check.CallFunc && c.Func != nil && check.HoldsTaken(c.Func):
		takes = c.Func.Sig.Takes
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
	}
	var ids []*syntax.Ident
	for _, i := range takes {
		if i >= len(args) {
			continue
		}
		if id, ok := unparen(args[i]).(*syntax.Ident); ok {
			if v := b.l.info.Handoffs[id]; v != nil && b.l.info.OwnedVars[v] != nil {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// heldCall evaluates the call before giving up its operands. An
// interruption or a defect leaves them live for the frame's unwind; a
// typed rejection releases them before the enclosing catch or try runs.
// This is holdTaken/settleTaken's rule over either IR owner storage form.
func (b *irBuilder) heldCall(x *syntax.CallExpr, c *check.Call, stmt bool, ids []*syntax.Ident) ([]*irStmt, *irExpr, bool) {
	o := b.own
	if o == nil {
		irNo("held take outside an owner frame")
	}
	first := len(o.held)
	o.held = append(o.held, ids...)
	o.hasHeld = true
	pre, call, _ := b.callPartsRaw(x, stmt)
	o.held = o.held[:first]
	if call == nil {
		irNo("held take without a call")
	}
	t := b.l.info.Types[x]
	hasValue := t != nil && !isNoValue(t)
	fallible := failsHere(c) || b.dropsErr[call]
	p := call.Pos
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	switch {
	case fallible && hasValue:
		// The list reference does not settle a fallible call with a
		// value (settleTaken). Do not guess who owns a partial result.
		irNo("held take with a fallible value result")
		return nil, nil, false
	case fallible:
		e := b.temp("err", "error", p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: e, X: call})
		if c.Kind == check.CallFunc {
			slices.Reverse(ids) // captureCall releases failed admissions in LIFO order
		}
		pre = append(pre, &irStmt{
			K: "if", Pos: p, End: p, ElsePos: p, ElseEnd: p,
			X:    &irExpr{K: "bin", Pos: p, Op: "==", X: local(e), Y: &irExpr{K: "zero", Pos: p, T: "error"}},
			Then: b.settleHeld(ids, p, false), Else: b.settleHeld(ids, p, true),
		})
		if !failsHere(c) {
			return pre, nil, false
		}
		return pre, local(e), false
	case hasValue:
		v := b.temp("v", b.goType(t, x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: call})
		return append(pre, b.settleHeld(ids, p, false)...), local(v), false
	default:
		pre = append(pre, &irStmt{K: "expr", Pos: p, X: call})
		return append(pre, b.settleHeld(ids, p, false)...), nil, false
	}
}

func (b *irBuilder) settleHeld(ids []*syntax.Ident, p irPos, rejected bool) []*irStmt {
	o := b.own
	var mask uint64
	var out []*irStmt
	for _, id := range ids {
		v := b.l.info.Handoffs[id]
		if o.list {
			h, ok := o.handle[v]
			if !ok {
				irNo("held take without an owner entry")
			}
			entry := &irExpr{K: "local", Pos: p, I: h}
			call := &irExpr{K: "mcall", Pos: p, Name: "Release", X: entry}
			if !rejected {
				call = b.ownCall("Hand", "struct{}", p, entry, &irExpr{K: "zero", Pos: p, T: "struct{}"})
			}
			out = append(out, &irStmt{K: "expr", Pos: p, X: call})
			continue
		}
		k, ok := o.bit[v]
		if !ok {
			irNo("held take without an owner slot")
		}
		if rejected {
			out = append(out, b.slotRelease(k, p))
		} else {
			mask |= 1 << uint(k)
		}
	}
	if mask != 0 {
		out = append(out, b.clearSlots(mask, p))
	}
	return out
}
