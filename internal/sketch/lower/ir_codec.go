package lower

// IR builder: the codec built-ins (FLIP OPC-codec; the emitter's wireCall). encode(v, w)
// and decode[T](r) call the type's codec function, which the lowering
// declares once per type (wireFunc), as the emitter's wireCall writes it:
//
//	encodeT(env, w, v)
//	v, err := decodeT(env, r)
//
// after the interruption point. A direct or cell writer (wiredirect.go,
// wiretext.go), a type the emitter writes inline (a basic value's one
// event) and a type that holds type parameters (codec witnesses) keep the
// source body.

import (
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
	case len(freeTypeParams(c.Type)) > 0:
		irNo("codec of a type that holds type parameters")
	case b.l.wireInline(c.Type):
		irNo("codec of a type written inline")
	}
	if dir == "encode" {
		if _, direct := b.l.directWriter(b.l.info, x.Args[1]); direct {
			irNo("encode on a direct writer")
		}
	} else if !failsHere(c) {
		irNo("fallible call with an empty row at its site")
	} else if _, direct := b.l.directReaderIndex(b.l.info, x.Args[0]); direct {
		irNo("decode on a direct reader")
	}
	if _, cell := b.l.cellOf(referenceEvents); cell {
		irNo("codec on a cell writer")
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
	name := b.l.wireFunc(dir, c.Type, x)
	return pre, &irExpr{K: "call", Pos: p, Name: name, Args: append([]*irExpr{env}, args...)}, true
}
