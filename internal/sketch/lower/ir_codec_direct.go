package lower

// IR builder: encode on a direct writer (PORT-ENCODE-DIRECT; the
// emitter's wireCall with em = wireEvents{via: i}, wiredirect.go and
// wiretext.go). A direct writer whose provider is a cell (cellWriterOf:
// std json's Text over its text record) keeps its state by value: the
// type's direct codec function takes the state and gives it back, so the
// encode is one store of the cell's own value, as the emitter writes it:
//
//	cell := ergoCellOfAdapter(t) // t.(*Adapter)
//	cell.v = encodeDirectN(env, [witnesses,] cell.v, v)
//	cell.v = wT.DirectN(env, cell.v, v) // T a type parameter
//
// The writer is a value of the operation type that holds the cell's
// adapter (an operations conversion), so the cell is the adapter's v
// field: the same storage the adapter's methods write, as the emitter's
// cell window (cellOperand) is.
// A basic value (the emitter's inline event, which cellEvents turns into
// the provider's call) is the adapter's method of its operation on the
// cell: `cell.Int(env.task, int64(v))` (basicEvent).
//
// Prior art: serde's Serializer is a value its derive threads by value
// through each field (`serializer.serialize_struct(...)` consumes it);
// Go's encoding/json appends to the encodeState's buffer in place.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) directEncode(x *syntax.CallExpr, c *check.Call, via int, p irPos) ([]*irStmt, *irExpr, bool) {
	em := wireEvents{via: via}
	// Every direct writer is a cell (directSource admits only a cell
	// writer), and its adapter and cell function have names from the
	// naming pass (naming.go): anything else is the lowering's defect.
	cw, ok := b.l.cellOf(em)
	if !ok {
		ice(x, "the direct writer %d is no cell", via)
	}
	key, _, ok := b.l.adapterName(cw.conv)
	adapter, okA := b.l.pkgNames[pkgKey{"adapter", key}]
	cellOf, okC := b.l.pkgNames[pkgKey{"cell of", key}]
	if !ok || !okA || !okC {
		ice(x, "the cell writer %s has no named adapter or cell function", key)
	}
	var pre []*irStmt
	if b.fc.frame.polls {
		pre = append(pre, b.poll(x))
	}
	argPre, args := b.operands(x.Args)
	pre = append(pre, argPre...)
	w, v := args[1], args[0]
	// The adapter that holds the cell, through cellOf, a Go function the
	// naming pass names beside the adapter (naming.go) and the first use
	// writes: the IR's assertion names a type, never a pointer to one.
	wt := b.fc.goType(b.l.info.Types[x.Args[1]])
	if !b.l.texts[pkgKey{"cell of", key}] && b.st.census == nil {
		b.l.texts[pkgKey{"cell of", key}] = true
		b.l.gen.WriteString("func " + cellOf + "(w " + wt + ") *" + adapter + " { return w.(*" + adapter + ") }\n\n")
	}
	a := b.temp("cell", b.spelling("*"+adapter), p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: a, X: &irExpr{K: "call", Pos: p, Name: cellOf, Args: []*irExpr{w}}})
	cell := func() *irExpr {
		return &irExpr{K: "field", Pos: p, Name: "v", X: &irExpr{K: "local", Pos: p, I: a}}
	}
	env := &irExpr{K: "local", Pos: p, I: b.env}
	if b.l.wireInline(c.Type) {
		// A basic value: its one event, the adapter's method of the
		// Writer's operation on the cell (basicEvent), as the emitter's
		// cellEvents makes wireEvents.basic the provider's call.
		return append(pre, &irStmt{K: "expr", Pos: p, X: b.basicEvent(c.Type, &irExpr{K: "local", Pos: p, I: a}, v, p)}), nil, true
	}
	var write *irExpr
	if tp, isParam := c.Type.(*check.TypeParam); isParam {
		// The witness's Direct field for this writer (wireEvents.param).
		fn := &irExpr{K: "field", Pos: p, Name: directField(via), X: b.codecWitness(tp, p)}
		write = &irExpr{K: "vcall", Pos: p, X: fn, Args: []*irExpr{env, cell(), v}}
	} else {
		lead := []*irExpr{env}
		var targs []string
		for _, tp := range freeTypeParams(c.Type) {
			lead = append(lead, b.codecWitness(tp, p))
			targs = append(targs, b.goType(tp, x))
		}
		name := b.l.wireFunc(em.dir(), c.Type, x)
		write = &irExpr{K: "call", Pos: p, Name: name, T: strings.Join(targs, ", "), Args: append(lead, cell(), v)}
	}
	return append(pre, &irStmt{K: "store", Pos: p, X: cell(), Y: write}), nil, true
}
