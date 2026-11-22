package lower

// IR builder: std results the emitter adapts (stdResult, std.go;
// ADAPTED-RESULTS). The first shape: an operation whose runtime method
// returns Go's comma-ok (T, bool), the Go form of the prelude's Option[T]
// (stdlib.md, Types 5; queue.Queue's Poll). The emitter calls it inside a
// function literal called where it is made, returning Some of the value
// when ok, else None (optionReturn). The IR binds both results and builds
// the Option in a local, as go.As does (ir_golib.go): None, then Some of
// the value when ok.
//
// Prior art: hand Go writes `v, ok := q.Poll()` and branches on ok; Rust's
// Option<T> from a channel's try_recv and OCaml's Queue.take_opt are the
// same value, built once by the callee. Gleam and ReScript, which lower
// Option to a tagged value of the host, build it at the call as this does.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// commaOkOpCall is the call x of the operation c.Op on a value of n,
// whose runtime method returns (T, bool) and no error (boundOpCall): the
// method's two results bound to locals, and the Option[T] the call's
// value is, None unless ok.
func (b *irBuilder) commaOkOpCall(x *syntax.CallExpr, c *check.Call, n *check.Named, body v1.Body, p irPos) ([]*irStmt, *irExpr, bool) {
	t := b.l.info.Types[x]
	if !isPreludeType(t, "Option") || failsHere(c) {
		irNo("std call with an adapted result")
	}
	some, _ := optionVariants(t, x)
	elem := t.(*check.Named).TArgs[0]
	goElem := b.recvTypeArgs(n, body.Results[0])
	if !b.sameGo(elem, goElem) && !b.sameRT(elem, goElem, nil, body.Package) {
		irNo("std call with a converted result")
	}
	convs := map[int]string{} // by index of the receiver-first vals
	for i, q := range body.Params {
		if adapted(q) {
			irNo("std call with an adapted result") // a function operand: the plain form's adapter (adaptOperands)
		}
		conv, ok := b.operandConv(x.Args[i], b.recvTypeArgs(n, q.Type), nil, body.Package) // ir_operand.go
		if !ok {
			irNo("std call with a converted operand")
		}
		if conv != "" {
			convs[i+1] = conv
		}
	}
	hidden := b.hidden(body, x, c, p)
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	convOperands(vals, convs) // ir_operand.go
	call := &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: append(hidden, vals[1:]...)}
	res := b.temp("opt", b.goType(t, x), p)
	v, ok := b.temp("v", b.goType(elem, x), p), b.temp("ok", "bool", p)
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(some, x), Keys: []string{memberName("Value")}, Elems: []*irExpr{{K: "local", Pos: p, I: v}}}
	pre = append(pre,
		&irStmt{K: "def", Pos: p, Local: res, X: b.machineNone(t, p, x)}, // ir_machine.go
		&irStmt{K: "tdef", Pos: p, X: call, Ls: []int{v, ok}},
		&irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "local", Pos: p, I: ok}, Then: []*irStmt{
			{K: "set", Pos: p, Local: res, X: b.variantInto(some, t, lit, x)}, // ir_union.go
		}})
	return pre, &irExpr{K: "local", Pos: p, I: res}, !body.Pure
}
