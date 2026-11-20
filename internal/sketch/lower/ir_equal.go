package lower

// IR builder: == and != on the types that are not basic (FLIP F7,
// equality on IR). See ir.go and equal.go.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// binaryOther is the IR of x when x is == or != on operands of a type that
// is not basic, else nil (operator takes the basic ones). Both paths share
// the emitter's form (binaryOf): Go's == when Go compares the type, else a
// call of the type's equality function (equal.go equalFunc), written once
// per Go type as the text path writes it, and negated for !=. The
// operands keep their order: a, then b.
//
// The operands are compared as the emitter's equalType: the union when the
// other operand is one of its variants, else the left one. The checker
// records the variant's conversion into the union (Implicit), which expr
// applies; an operand of any other Go type keeps the source body.
//
// Prior art: Gleam on Erlang keeps the host's structural =:=, and on
// JavaScript calls its isEqual walk; ReScript calls caml_equal unless the
// type is known to be a primitive. Here the type is always known, so the
// call is the per-type function, as Rust's derive(PartialEq) is.
func (b *irBuilder) binaryOther(x *syntax.BinaryExpr, p irPos) *irExpr {
	// Both operands in order, each with its implicit conversion: a later
	// one that runs statements (a call, an if) runs after the earlier
	// (ir_order.go inOrder).
	return b.equalOn(x, func() []*irExpr { return b.inOrder([]syntax.Expr{x.X, x.Y}) })
}

// equalOn is binaryOther's comparison of the operands that build gives,
// built once x is known to be one: an assert's operands (ir_assert.go) are
// its spilled locals, which its failure text reads too (the emitter's
// assertCompare, binaryOf on the spilled parts).
func (b *irBuilder) equalOn(x *syntax.BinaryExpr, build func() []*irExpr) *irExpr {
	op := x.Op.String()
	if op != "==" && op != "!=" {
		return nil
	}
	lt, rt := b.l.info.Types[x.X], b.l.info.Types[x.Y]
	if lt == nil || rt == nil || b.scalarBasic(lt) != nil && b.scalarBasic(rt) != nil {
		return nil
	}
	t := b.fc.equalType(x)
	gt := b.goType(t, x)
	vals := build()
	operand := func(k int, e syntax.Expr, et check.Type) *irExpr {
		v := vals[k]
		if conv, ok := b.l.info.Implicit[e]; ok && conv.Kind == check.ConvVariant {
			et = conv.To
		}
		if b.goType(et, x) != gt {
			irNo("operator %s on operands of two types", op)
		}
		return v
	}
	pos := b.pos(x, x.OpPos)
	if !needsEqual(t) {
		if !check.Comparable(t) {
			irNo("operator %s on a type with no equality", op)
		}
		return &irExpr{K: "bin", Pos: pos, Op: op, X: operand(0, x.X, lt), Y: operand(1, x.Y, rt)}
	}
	name, free := b.l.equalFunc(t, x)
	args := make([]string, len(free))
	for i, tp := range free {
		args[i] = b.goType(tp, x)
	}
	call := &irExpr{K: "call", Pos: pos, Name: name, T: strings.Join(args, ", "), Args: []*irExpr{operand(0, x.X, lt), operand(1, x.Y, rt)}}
	if op == "!=" {
		return &irExpr{K: "un", Pos: pos, Op: "!", X: call}
	}
	return call
}
