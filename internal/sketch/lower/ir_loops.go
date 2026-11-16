package lower

// IR builder: a count loop that is a fill or a move (FLIP F7 GP-LOOPS).
// See ir.go and loops.go.
//
// The loop idioms (DT4) are decided once, by loops.go's loopIdiomOf, for
// the text path and the IR alike; this file writes the decision as the
// emitter's loopIdiomHead writes it, on B-BUILTINS' "builtin" kind (copy,
// clear) and GP-SLICE's "slice" kind:
//
//	if guard { call } else { loop }
//
//   - fill: `fill := P[a:b]; fill[0] = v`, then clear(fill) when v is the
//     zero value of a zeroComparable type, else copy doubling the written
//     part (`for k := 1; k < len(fill); k *= 2 { copy(fill[k:], fill[:k]) }`,
//     as a while loop with its increment);
//   - move: copy(P[da:db], P[sa:sb]) when the forward loop is a memmove.
//
// The guard is the emitter's: lo < hi, every part in bounds, and for a move
// the forward test; the loop itself runs otherwise, and raises the defect
// it raises today. The Range's operands run first, once, as countLoop
// writes them; the guard and the call read the Range's temporary.
//
// A collect (`xs = xs.Append(Q[i+s])`) keeps the element loop: loopIdiomOf
// reads the emitter's own locals (fc.locals, fc.scalarIdent) for it, which
// the IR builder does not keep. An idiom whose operand needs statements
// (none does: loopIdiomOf takes only names, constants, fields and
// operators) keeps the loop too.
//
// Prior art: loops.go (LLVM's LoopIdiomRecognize, GCC's loop distribution,
// Go's memclr range loop).

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// loopIdiomIR is the count loop s, built as loop over the Range in the
// local r (field reads its Start and End), written as its idiom, or nil
// when s is no fill or move.
func (b *irBuilder) loopIdiomIR(s *syntax.RangeStmt, lp *check.Loop, loop *irStmt, field func(string) *irExpr) *irStmt {
	idiom := b.fc.loopIdiomOf(s, lp) // loops.go: the one decision
	if idiom == nil || idiom.kind == "collect" {
		return nil
	}
	elem, ok := sliceElem(b.l.info.Types[idiom.place])
	if !ok {
		return nil
	}
	body := s.Body.List[0]
	p := b.pos(body, body.Pos())
	clean := true
	val := func(x syntax.Expr) *irExpr {
		pre, v := b.value(x)
		if len(pre) > 0 {
			clean = false
		}
		return v
	}
	bin := func(op string, x, y *irExpr) *irExpr { return &irExpr{K: "bin", Pos: p, Op: op, X: x, Y: y} }
	num := func(n int64) *irExpr { return b.constant(lp.Elem, constant.MakeInt64(n), nil, p) }
	place := func() *irExpr { return val(idiom.place) }
	// at is base, base + off or base - off.
	at := func(base string, off syntax.Expr, neg bool) func() *irExpr {
		return func() *irExpr {
			if off == nil {
				return field(base)
			}
			op := "+"
			if neg {
				op = "-"
			}
			return bin(op, field(base), val(off))
		}
	}
	guard := bin("<", field("Start"), field("End"))
	// in tests that [a, b) is a non-empty part of the place; with no
	// offset a < b is the guard itself and is left out (loopIdiomHead).
	in := func(a, z func() *irExpr, off syntax.Expr) *irExpr {
		inside := bin("<=", z(), &irExpr{K: "len", Pos: p, X: place()})
		if off == nil {
			return bin("&&", bin("<=", num(0), a()), inside)
		}
		return bin("&&", bin("&&", bin("<=", num(0), a()), bin("<", a(), z())), inside)
	}
	part := func(a, z func() *irExpr) *irExpr {
		return &irExpr{K: "slice", Pos: p, X: place(), Args: []*irExpr{a(), z()}}
	}
	builtin := func(name string, args ...*irExpr) *irStmt {
		return &irStmt{K: "expr", Pos: p, X: &irExpr{K: "builtin", Pos: p, Name: name, Args: args}}
	}
	var cond *irExpr
	var then []*irStmt
	switch idiom.kind {
	case "fill":
		a, z := at("Start", idiom.dst, idiom.dstNeg), at("End", idiom.dst, idiom.dstNeg)
		cond = bin("&&", guard, in(a, z, idiom.dst))
		f := b.temp("fill", b.goType(b.l.info.Types[idiom.place], idiom.place), p)
		local := func() *irExpr { return &irExpr{K: "local", Pos: p, I: f} }
		first := func() *irExpr { return &irExpr{K: "index", Pos: p, X: local(), Y: num(0)} }
		v := b.assignedTo(idiom.val, val(idiom.val), elem) // ir_union.go
		then = []*irStmt{
			{K: "def", Pos: p, Local: f, X: part(a, z)},
			{K: "store", Pos: p, X: first(), Y: v},
		}
		k := b.temp("k", "int", p)
		kv := func() *irExpr { return &irExpr{K: "local", Pos: p, I: k} }
		double := []*irStmt{
			{K: "def", Pos: p, Local: k, X: num(1)},
			{K: "while", Pos: p, End: p, X: bin("<", kv(), &irExpr{K: "len", Pos: p, X: local()}), Body: []*irStmt{
				builtin("copy", &irExpr{K: "slice", Pos: p, X: local(), Args: []*irExpr{kv(), nil}}, &irExpr{K: "slice", Pos: p, X: local(), Args: []*irExpr{nil, kv()}}),
				{K: "set", Pos: p, Local: k, X: bin("*", kv(), num(2))},
			}},
		}
		vt := b.l.info.Types[idiom.val]
		if fillLowering != "copy" && zeroComparable(vt, map[check.Type]bool{}) {
			// A value whose Go form is all zero bits when it equals the
			// type's zero value is a clear (loopIdiomHead).
			zero := &irExpr{K: "zero", Pos: p, T: b.goType(vt, idiom.val)}
			then = append(then, &irStmt{K: "if", Pos: p, End: p, X: bin("==", first(), zero),
				Then: []*irStmt{builtin("clear", local())}, Else: double, ElsePos: p, ElseEnd: p})
		} else {
			then = append(then, double...)
		}
	case "move":
		da, db := at("Start", idiom.dst, idiom.dstNeg), at("End", idiom.dst, idiom.dstNeg)
		sa, sb := at("Start", idiom.src, idiom.srcNeg), at("End", idiom.src, idiom.srcNeg)
		// A forward loop is memmove when the target starts at or before
		// the source, or after it.
		forward := bin("||", bin("<=", da(), sa()), bin(">=", da(), sb()))
		cond = bin("&&", bin("&&", bin("&&", guard, in(da, db, idiom.dst)), in(sa, sb, idiom.src)), forward)
		then = []*irStmt{builtin("copy", part(da, db), part(sa, sb))}
	default:
		ice(s, "the loop idiom %q", idiom.kind)
	}
	if !clean {
		return nil
	}
	return &irStmt{K: "if", Pos: loop.Pos, End: loop.Pos, X: cond, Then: then, Else: []*irStmt{loop}, ElsePos: loop.Pos, ElseEnd: loop.End}
}
