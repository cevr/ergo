package lower

// IR builder: a function value a std body's Go parameter takes (FLIP
// OPC-adapt), as the emitter's adaptPlainFunc writes it. The value has the
// uniform shape (typeCtx.funcType: the environment first, then a value and
// an error); the body's parameter is the Go function its shape names
// (v1.Param.Func), with parameters of its own and one result. The adapter
// is a Go function literal of that shape that calls the value, bound once
// at the call, with the caller's environment, and drops its error:
//
//	func(a T, b T) int { r, _ := fv(env, a, b); return r }   (slices.SortFunc, Ref.Update)
//
// A block with no parameters (adaptFunc), a shape that takes the hidden
// task (a task's block, which runs on a copy of the environment that holds
// that task) and a slice of functions keep the source body.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// adapted reports a parameter the call passes through an adapter.
func adapted(q v1.Param) bool { return q.Func != nil || q.Funcs != nil }

// adaptOperand is the adapter of e, the IR of the operand arg, to the Go
// parameter q of a std body, and the statements that bind e first.
func (b *irBuilder) adaptOperand(arg syntax.Expr, e *irExpr, q v1.Param, p irPos) ([]*irStmt, *irExpr) {
	shape := q.Func
	switch {
	case q.Funcs != nil:
		irNo("std call that adapts a slice of functions")
	case len(shape.Hidden) != 0:
		irNo("std call that adapts a function for a task")
	case b.env < 0:
		irNo("std call that adapts a function from a pure function")
	}
	t := b.l.info.Types[arg]
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	sig, ok := t.(*check.Signature)
	if !ok || len(sig.Results) > 1 {
		irNo("std call that adapts a value of another type")
	}
	// fv is bound once, here, where the call is made (bindFunc): a later
	// assignment to the variable it names does not change what runs.
	var pre []*irStmt
	fv := e
	if e.K != "func" {
		v := b.temp("fv", b.sigType(sig, arg), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: e})
		fv = &irExpr{K: "local", Pos: p, I: v}
	}
	env := &irExpr{K: "local", Pos: p, I: b.env}
	call := &irExpr{K: "vcall", Pos: p, X: fv, Args: []*irExpr{env}}
	lit := &irExpr{K: "func", Pos: p, End: &p}
	value := "struct{}"
	if len(sig.Results) == 1 {
		value = b.goType(sig.Results[0], arg)
	}
	// adaptPlainFunc: the Ergo parameter's type has an empty row, so fv
	// never fails; its error is dropped.
	switch {
	case len(shape.Params) == 0 && len(sig.Params) == 0:
		irNo("std call that adapts a block")
	case len(shape.Params) != len(sig.Params):
		irNo("std call that adapts a function of another arity")
	case len(shape.Results) != 1 || shape.Results[0] == "error" || len(sig.Results) != 1:
		irNo("std call that adapts a function of another result")
	case len(rowElems(sig.Errors)) != 0 || len(rowElems(sig.Uses)) != 0:
		irNo("std call that adapts a function with a row")
	}
	var params []string
	for _, sp := range sig.Params {
		gt := b.goType(sp.Type, arg)
		local := b.temp("p", gt, p)
		params = append(params, gt)
		lit.Params = append(lit.Params, local)
		call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: local})
	}
	lit.T = "func(" + strings.Join(params, ", ") + ") " + value
	r, err := b.temp("r", value, p), b.temp("err", "error", p)
	lit.Body = append(lit.Body,
		&irStmt{K: "def2", Pos: p, Local: r, Val: &err, X: call},
		&irStmt{K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}})
	return pre, lit
}

// adaptOperands passes each operand of a std body's call that its Go
// parameter adapts through adaptOperand: args are the IR of x's operands,
// after the receiver when the call has one (skip 1).
func (b *irBuilder) adaptOperands(params []v1.Param, xs []syntax.Expr, args []*irExpr, skip int, p irPos) []*irStmt {
	var pre []*irStmt
	for i, q := range params {
		if adapted(q) {
			bind, e := b.adaptOperand(xs[i], args[skip+i], q, p)
			pre, args[skip+i] = append(pre, bind...), e
		}
	}
	return pre
}
