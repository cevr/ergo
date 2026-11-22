package lower

// IR builder: std results the emitter adapts (stdResult, std.go;
// ADAPTED-RESULTS). The first shape: an operation whose runtime method
// returns Go's comma-ok (T, bool), the Go form of the prelude's Option[T]
// (stdlib.md, v1 packages, item 5 of its Types; queue.Queue's Poll). The emitter calls it inside a
// function literal called where it is made, returning Some of the value
// when ok, else None (optionReturn). The IR binds both results and builds
// the Option in a local, as go.As does (ir_golib.go): None, then Some of
// the value when ok.
//
// The second shape: an operation whose runtime method returns Go's
// iter.Seq[T] (queue.Queue's Values, actor.Actor's LatestChanges), the Go
// form of the prelude's Seq[T]. Its call is deferred (defersCall): the
// receiver and the arguments are evaluated once where the Seq is made, and
// the method is called inside the Seq with the environment it is iterated
// on, so a wait takes the iterating task, never the making one (Rule 3;
// the emitter's stdResult). The IR binds the operands to locals, then
// makes the Seq a function literal of the uniform shape that ranges over
// the method's iter.Seq and passes each value to yield, returning at a
// yield's failure or false.
//
// Prior art: hand Go writes `v, ok := q.Poll()` and branches on ok; Rust's
// Option<T> from a channel's try_recv and OCaml's Queue.take_opt are the
// same value, built once by the callee. Gleam and ReScript, which lower
// Option to a tagged value of the host, build it at the call as this does.

import (
	"strings"

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
	if !isOptionType(t) || failsHere(c) { // gocall.go
		irNo("std call with an adapted result")
	}
	some, _ := optionVariants(t, x)
	// Some's one field, the payload, by its declaration (ADT-NOPRIV).
	var st *check.Struct
	if someN, named := some.(*check.Named); named {
		st, _ = someN.Underlying().(*check.Struct)
	}
	if st == nil || len(st.Fields) != 1 {
		ice(x, "the prelude's Some is not a record of one field")
	}
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
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(some, x), Keys: []string{memberName(st.Fields[0].Name)}, Elems: []*irExpr{{K: "local", Pos: p, I: v}}}
	pre = append(pre,
		&irStmt{K: "def", Pos: p, Local: res, X: b.machineNone(t, p, x)}, // ir_machine.go
		&irStmt{K: "tdef", Pos: p, X: call, Ls: []int{v, ok}},
		&irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "local", Pos: p, I: ok}, Then: []*irStmt{
			{K: "set", Pos: p, Local: res, X: b.variantInto(some, t, lit, x)}, // ir_union.go
		}})
	return pre, &irExpr{K: "local", Pos: p, I: res}, !body.Pure
}

// seqOpCall is the call x of the operation c.Op on a value of n, whose
// runtime method returns Go's iter.Seq[T] (boundOpCall): its operands
// bound to locals where the Seq is made (the emitter's bindDeferred), and
// the Seq[T] a function literal of the uniform shape,
//
//	func(e *Env, yield func(*Env, T) (bool, error)) (struct{}, error) {
//		for v := range recv.Op(e's task, args...) {
//			ok, err := yield(e, v)
//			if err != nil { panic(UnreachableAt(…, the call's site)) }
//			if !ok { return struct{}{}, nil }
//		}
//		return struct{}{}, nil
//	}
//
// whose hidden arguments read e, the environment the Seq is iterated on.
// A Seq's yield is func(v T) bool, with no failure (types.md, Loops and
// len 4), so its error is always nil: the uniform shape carries it, and
// the literal makes a non-nil one the unreachable defect at the call
// rather than a failure no checked program has. The emitter (stdResult)
// returns it; a change that lets a yield fail fails here, loudly.
func (b *irBuilder) seqOpCall(x *syntax.CallExpr, c *check.Call, n *check.Named, body v1.Body, p irPos) ([]*irStmt, *irExpr, bool) {
	elem := b.seqElem(x, c, body)
	goElem := b.recvTypeArgs(n, strings.TrimSuffix(strings.TrimPrefix(body.Results[0], "iter.Seq["), "]"))
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
	ops := append([]syntax.Expr{c.Recv}, x.Args...)
	pre, vals := b.operands(ops)
	convOperands(vals, convs) // ir_operand.go
	pre = append(pre, b.bindDeferred(x, ops, vals, convs, p)...)
	lit := b.seqLit(x, c, elem, body, p, func(hidden []*irExpr) *irExpr {
		return &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: append(hidden, vals[1:]...)}
	})
	return pre, lit, !body.Pure
}

// stdSeqCall is the call x of a std function's body named name (in pkg,
// at the type arguments targs) whose Go result is iter.Seq[T] (stdCall):
// its operands args, the function operands among them adapted (on the
// making environment, as the emitter's adaptFunc runs before its
// bindDeferred), bound to locals where the Seq is made, and the Seq[T]
// the literal seqOpCall's is, calling the function on the iterating
// environment (actor.Changes, io's hostLines).
func (b *irBuilder) stdSeqCall(x *syntax.CallExpr, c *check.Call, body v1.Body, subst map[string]string, pkg, name, targs string, ops []syntax.Expr, args []*irExpr, convs map[int]string, p irPos) ([]*irStmt, *irExpr, bool) {
	elem := b.seqElem(x, c, body)
	goElem := strings.TrimSuffix(strings.TrimPrefix(body.Results[0], "iter.Seq["), "]")
	if !b.sameGo(elem, goElem) && !b.sameRT(elem, goElem, subst, body.Package) {
		irNo("std call with a converted result")
	}
	pre := b.bindDeferred(x, ops, args, convs, p)
	lit := b.seqLit(x, c, elem, body, p, func(hidden []*irExpr) *irExpr {
		return &irExpr{K: "pcall", Pos: p, T: pkg, Name: name, V: targs, Args: append(hidden, args...)}
	})
	return pre, lit, !body.Pure
}

// seqElem is T of the Seq[T] the call x of body returns, which returns
// Go's iter.Seq: a call that fails at its site, or a body that takes the
// frame scope (a Seq outlives its frame), keeps the source body.
func (b *irBuilder) seqElem(x *syntax.CallExpr, c *check.Call, body v1.Body) check.Type {
	t := b.l.info.Types[x]
	seqT, ok := t.(*check.Named)
	if !ok || !isPreludeType(t, "Seq") || len(seqT.TArgs) != 1 || failsHere(c) || hasHidden(body.Hidden, v1.Frame) {
		irNo("std call with an adapted result")
	}
	if b.env < 0 {
		irNo("call of a function value from a pure function") // the Seq is called with an environment
	}
	return seqT.TArgs[0]
}

// bindDeferred binds each of a deferred call's operands vals (the IR of
// ops, then any adapted block after them) to a local, here, in source
// order, as the emitter's bindDeferred spills them: the Seq evaluates
// none again. A local's type is the operand's converted type (convs, by
// index), an adapter's own, or the operand's.
func (b *irBuilder) bindDeferred(x *syntax.CallExpr, ops []syntax.Expr, vals []*irExpr, convs map[int]string, p irPos) []*irStmt {
	var pre []*irStmt
	for i, v := range vals {
		typ := convs[i]
		switch {
		case typ != "":
		case v.K == "func" && v.T != "":
			typ = v.T
		case i < len(ops):
			typ = b.goType(b.l.info.Types[ops[i]], x)
		default:
			irNo("std call with an adapted result") // a trailing block's adapter has its type
		}
		l := b.temp("op", typ, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: v})
		vals[i] = &irExpr{K: "local", Pos: p, I: l}
	}
	return pre
}

// seqLit is the Seq[elem] of the call x: the function literal of the
// uniform shape (seqOpCall) ranging over call's iter.Seq, call given the
// hidden arguments read from the iterating environment.
func (b *irBuilder) seqLit(x *syntax.CallExpr, c *check.Call, elem check.Type, body v1.Body, p irPos, call func(hidden []*irExpr) *irExpr) *irExpr {
	t := b.l.info.Types[x]
	sig, ok := t.(*check.Named).Underlying().(*check.Signature)
	if !ok || len(sig.Params) != 1 {
		irNo("std call with an adapted result")
	}
	// The literal's type is the Seq's (sigType); its first parameter's type
	// is the environment's, read from it, so this body names no Env type
	// of its own (TestRuntime199EnvTypeReaders), as seqRange does.
	typ := b.sigType(sig, x)
	envT, _, _ := strings.Cut(strings.TrimPrefix(typ, "func("), ", ")
	e := b.temp("e", envT, p)
	yield := b.temp("yield", b.goType(sig.Params[0].Type, x), p)
	outer := b.env
	b.env = e // the hidden arguments take the iterating environment
	hidden := b.hidden(body, x, c, p)
	b.env = outer
	v := b.temp("v", b.goType(elem, x), p)
	yok, yerr := b.temp("ok", "bool", p), b.temp("err", "error", p)
	b.keepImport("var _ = " + b.l.rt() + ".UnreachableAt")
	unreachable := &irStmt{K: "panic", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "UnreachableAt", Args: []*irExpr{
		{K: "const", Pos: p, T: "string", V: "a Seq's yield failed"},
		{K: "const", Pos: p, T: "string", V: b.l.siteText(x)},
	}}}
	unit := func() *irStmt { return &irStmt{K: "ret", Pos: p, X: &irExpr{K: "zero", Pos: p, T: "struct{}"}} }
	loop := &irStmt{K: "range", Pos: p, End: p, X: call(hidden), Key: &v, Body: []*irStmt{
		{K: "def2", Pos: p, Local: yok, Val: &yerr, X: &irExpr{K: "vcall", Pos: p, X: &irExpr{K: "local", Pos: p, I: yield}, Args: []*irExpr{{K: "local", Pos: p, I: e}, {K: "local", Pos: p, I: v}}}},
		{K: "if", Pos: p, End: p, X: b.notNil(yerr, p), Then: []*irStmt{unreachable}},
		{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: &irExpr{K: "local", Pos: p, I: yok}}, Then: []*irStmt{unit()}},
	}}
	end := p
	lit := &irExpr{K: "func", Pos: p, T: typ, Params: []int{e, yield}, End: &end, Body: []*irStmt{loop, unit()}}
	return &irExpr{K: "conv", Pos: p, T: b.goType(t, x), X: lit} // the literal is the named Seq's underlying func
}
