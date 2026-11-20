package lower

// IR builder: the codec built-ins (FLIP OPC-codec; the emitter's wireCall). encode(v, w)
// and decode[T](r) call the type's codec function, which the lowering
// declares once per type (wireFunc), as the emitter's wireCall writes it:
//
//	encodeT(env, w, v)
//	v, err := decodeT(env, r)
//
// after the interruption point. A type parameter's codec is its witness's
// field, `wT.Enc(env, w, v)` and `v, err := wT.Dec(env, r)`, and a type
// that holds type parameters passes their witnesses after the environment
// (PORT-WITNESS; ir_witness.go). A basic value's encode is its one event,
// the Writer's operation called inline (basicEvent); a direct writer's
// encode writes its cell (ir_codec_direct.go; PORT-ENCODE-DIRECT).

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) codecCall(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr, bool) {
	dir, want := "decode", 1
	if c.Kind == check.CallEncode {
		dir, want = "encode", 2
	}
	switch {
	case len(x.Args) != want || c.Type == nil:
		irNo("codec call form")
	case b.env < 0:
		irNo("codec call from a pure function")
	}
	if dir == "encode" {
		if via, direct := b.l.directWriter(b.l.info, x.Args[1]); direct {
			return b.directEncode(x, c, via, p) // ir_codec_direct.go
		}
	} else if !failsHere(c) {
		irNo("fallible call with an empty row at its site")
	} else if via, direct := b.l.directReaderIndex(b.l.info, x.Args[0]); direct {
		return b.directDecode(x, c, via, p) // ir_codec_direct.go
	}
	if _, cell := b.l.cellOf(referenceEvents); cell {
		irNo("codec on a cell writer")
	}
	// The reader or writer operand goes to the codec function as the
	// prelude's Reader or Writer. A value of another operation type (a
	// module's sealed Cursor lent to decode, as xml's Decode does) is a Go
	// interface the decoder would assign by Go's rule, which its check
	// does not take: it wants identical types (follow-up IFACE-CONV).
	io, ioName := x.Args[0], check.WireReaderName
	if dir == "encode" {
		io, ioName = x.Args[1], check.WireWriterName
	}
	if t, ok := b.l.info.Types[io].(*check.Named); ok && t.Underlying() != nil {
		if _, ops := t.Underlying().(*check.Interface); ops && b.goType(t, io) != b.goType(check.PreludeType(ioName), io) {
			irNo("operation interface to another interface")
		}
	}
	var pre []*irStmt
	if b.fc.frame.polls {
		pre = append(pre, b.poll(x))
	}
	argPre, args := b.operands(x.Args)
	pre = append(pre, argPre...)
	if dir == "encode" {
		args = []*irExpr{args[1], args[0]} // the writer, then the value
	}
	env := &irExpr{K: "local", Pos: p, I: b.env}
	if dir == "encode" && b.l.wireInline(c.Type) {
		// A basic value is one event, the Writer's operation called
		// inline (wireEvents.basic); decode[T] of it calls its decode
		// function, as any type's (reserveWires).
		return pre, b.basicEvent(c.Type, args[0], args[1], p), true // the writer, the value
	}
	if tp, ok := c.Type.(*check.TypeParam); ok {
		// The codec of a type parameter is its witness's (wireOf):
		// Enc or Dec, a function value of the uniform shape, as the
		// emitter's wireEvents.param and wireCall call it.
		field := "Enc"
		if dir == "decode" {
			field = "Dec"
		}
		fn := &irExpr{K: "field", Pos: p, Name: field, X: b.codecWitness(tp, p)}
		return pre, &irExpr{K: "vcall", Pos: p, X: fn, Args: append([]*irExpr{env}, args...)}, true
	}
	lead := []*irExpr{env}
	var targs []string
	for _, tp := range freeTypeParams(c.Type) {
		// A generic codec function takes a witness per type parameter
		// after the environment (wireWits), and its call names its type
		// arguments, the body's own parameters (wireFunc's typeParams),
		// never left to Go's inference (ir_pkg.go).
		lead = append(lead, b.codecWitness(tp, p))
		targs = append(targs, b.goType(tp, x))
	}
	name := b.l.wireFunc(dir, c.Type, x)
	return pre, &irExpr{K: "call", Pos: p, Name: name, T: strings.Join(targs, ", "), Args: append(lead, args...)}, true
}

// basicEvent is the one event of v, a value of the basic (or defined
// basic) type t, on w: the operation of its kind, with the task and v
// converted to its Go type, as wireEvents.basic writes it.
func (b *irBuilder) basicEvent(t check.Type, w, v *irExpr, p irPos) *irExpr {
	op, gt := "Int", "int64"
	switch k := wireUnder(t).(*check.Basic).Kind; {
	case k == check.Bool:
		op, gt = "Bool", "bool"
	case k == check.String:
		op, gt = "String", "string"
	case k == check.Float32 || k == check.Float64:
		op, gt = "Float", "float64"
	case k >= check.Uint && k <= check.Uintptr:
		op, gt = "Uint", "uint64"
	}
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	return &irExpr{K: "mcall", Pos: p, Name: op, X: w, Args: []*irExpr{task, {K: "conv", Pos: p, T: gt, X: v}}}
}

// codecWitness is the codec witness of tp in this body: its witness
// parameter's local (witnessSlots), as the emitter's wireOf reads
// fc.witness.
func (b *irBuilder) codecWitness(tp *check.TypeParam, p irPos) *irExpr {
	l, ok := b.wits[tp]
	if !ok || !isWireParam(tp) {
		irNo("codec of a type parameter with no codec witness here")
	}
	return &irExpr{K: "local", Pos: p, I: l}
}
