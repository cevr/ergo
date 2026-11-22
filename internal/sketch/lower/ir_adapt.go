package lower

// IR builder: a function value a std body's Go parameter takes (FLIP
// OPC-adapt), as the emitter's adaptFunc, adaptPlainFunc and
// adaptTaskFunc write it. The value has the uniform shape
// (typeCtx.funcType: the environment first, then a value and an error);
// the body's parameter is the Go function its shape names (v1.Param.Func).
// The adapter is a Go function literal of that shape that calls the
// value, bound once at the call:
//
//	func(a T, b T) int { r, _ := fv(env, a, b); return r }   (slices.SortFunc)
//	func() error { _, err := fv(env); return err }            (a block)
//	func(t *Task) (S, error) { e := *env; e.task = t; return fv(&e) }
//
// A shape that takes the hidden task runs the value on a copy of the
// caller's environment that holds that task (Rule 1: the providers are
// captured, the task is the runner's), as a task.Go block does; any other
// shape runs it with the caller's environment. A Go result list with no
// error drops the value's error, which a checked value with no error row
// never returns. A slice of functions (tasks.Race) and a function the Go
// body gives the callback keep the source body.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// adapted reports a parameter the call passes through an adapter.
func adapted(q v1.Param) bool { return q.Func != nil || q.Funcs != nil }

// trailing is 1 for a call with a trailing block, its last operand.
func trailing(x *syntax.CallExpr) int {
	if x.Trailing != nil {
		return 1
	}
	return 0
}

// adaptOperand is the adapter of e, the IR of a function value of the
// signature sig (the operand at site), to the Go parameter q of a std
// body, and the statements that bind e first.
func (b *irBuilder) adaptOperand(sig *check.Signature, site syntax.Node, e *irExpr, q v1.Param, p irPos) ([]*irStmt, *irExpr) {
	shape := q.Func
	if q.Funcs != nil {
		irNo("std call that adapts a slice of functions")
	}
	if b.env < 0 {
		irNo("std call that adapts a function from a pure function")
	}
	if sig == nil || len(sig.Results) > 1 {
		irNo("std call that adapts a value of another type")
	}
	task := false
	for _, h := range shape.Hidden {
		if h != v1.CallerTask || task {
			irNo("std call that adapts a function with hidden argument %s", h)
		}
		task = true
	}
	fallible := len(shape.Results) > 0 && shape.Results[len(shape.Results)-1] == "error"
	values := len(shape.Results)
	if fallible {
		values--
	}
	value := "struct{}" // a block with no value binds T to struct{}
	if len(sig.Results) == 1 {
		value = b.goType(sig.Results[0], site)
	}
	ok := len(shape.Params) == len(sig.Params)
	switch {
	case len(shape.Params) == 0:
		// adaptFunc: a block; a Go result of error alone takes a block
		// with no value.
		ok = ok && values <= 1 && !(len(shape.Results) == 1 && fallible && len(sig.Results) != 0) && !(values == 1 && !fallible)
	case task:
		// adaptTaskFunc: the Go values are the Ergo function's.
		ok = ok && values == len(sig.Results)
	default:
		// adaptPlainFunc: one Go result that is not error; the Ergo
		// parameter's type has an empty row, so the value never fails.
		ok = ok && values == 1 && !fallible && len(sig.Results) == 1 && len(rowElems(sig.Errors)) == 0 && len(rowElems(sig.Uses)) == 0
	}
	if !ok {
		irNo("std call that adapts a function of another shape")
	}
	// fv is bound once, here, where the call is made (bindFunc): a later
	// assignment to the variable it names does not change what runs.
	var pre []*irStmt
	fv := e
	if e.K != "func" {
		v := b.temp("fv", b.sigType(sig, site), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: e})
		fv = &irExpr{K: "local", Pos: p, I: v}
	}
	lit := &irExpr{K: "func", Pos: p, End: &p}
	var params []string
	var env *irExpr = &irExpr{K: "local", Pos: p, I: b.env}
	if task {
		// childEnv: e := *env; e.task = t; the value runs with &e.
		tt := b.spelling("*" + b.l.taskPkg() + ".Task")
		t := b.temp("t", tt, p)
		params = append(params, tt)
		lit.Params = append(lit.Params, t)
		cp := b.temp("e", b.l.env(), p)
		lit.Body = append(lit.Body,
			&irStmt{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: env}},
			&irStmt{K: "store", Pos: p, X: &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: cp}}, Y: &irExpr{K: "local", Pos: p, I: t}})
		env = &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: cp}}
	}
	call := &irExpr{K: "vcall", Pos: b.litCallPos(site, e, p), X: fv, Args: []*irExpr{env}}
	for i, sp := range sig.Params {
		if shape.Params[i].Func != nil {
			irNo("std call that adapts a function the body gives a function")
		}
		gt := b.goType(sp.Type, site)
		local := b.temp("p", gt, p)
		params = append(params, gt)
		lit.Params = append(lit.Params, local)
		call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: local})
	}
	results := ""
	r, err := b.temp("r", value, p), b.temp("err", "error", p)
	bind := &irStmt{K: "def2", Pos: call.Pos, Local: r, Val: &err, X: call}
	switch {
	case values == 1 && fallible:
		results = " (" + value + ", error)"
		lit.Body = append(lit.Body, &irStmt{K: "ret", Pos: p, X: call})
	case values == 1:
		results = " " + value
		lit.Body = append(lit.Body, bind, &irStmt{K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}})
	case fallible:
		results = " error"
		lit.Body = append(lit.Body, bind, &irStmt{K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: err}})
	default:
		lit.Body = append(lit.Body, bind)
	}
	lit.T = "func(" + strings.Join(params, ", ") + ")" + results
	return pre, lit
}

// litCallPos is the position of the adapter's call of e, the operand at
// site: for a block or a function literal the adapter calls in place, the
// position of its last statement, as the emitter's call follows the
// literal's text, whose closing line repeats that statement's directive
// (adaptFunc). Go reads the adapter's frame line there, so the report
// writes the closure around an exit action's block as part of the
// action's frame, not a call of scope.OnExit (runtime.md, Defects 3;
// runtime/v1 ergoStack). Any other operand is called at p.
func (b *irBuilder) litCallPos(site syntax.Node, e *irExpr, p irPos) irPos {
	if e.K != "func" {
		return p
	}
	var body *syntax.BlockStmt
	switch n := site.(type) {
	case *syntax.BlockStmt:
		body = n
	case *syntax.FuncLit:
		body = n.Body
	}
	if body == nil || len(body.List) == 0 {
		return p
	}
	last := body.List[len(body.List)-1]
	return b.pos(last, last.Pos())
}

// adaptOperands passes each operand of a std body's call that its Go
// parameter adapts through adaptOperand: ops are the source operands
// params take, in order (x's arguments, after a slice operation's
// receiver; PORT-CALLS), x's trailing block after them; args are their IR
// (the block last), after the receiver of an operation, which params do
// not take (skip 1).
func (b *irBuilder) adaptOperands(params []v1.Param, x *syntax.CallExpr, ops []syntax.Expr, args []*irExpr, skip int, p irPos) []*irStmt {
	var pre []*irStmt
	for i, q := range params {
		if !adapted(q) {
			continue
		}
		var sig *check.Signature
		var site syntax.Node
		if i < len(ops) {
			site = ops[i]
			t := b.l.info.Types[ops[i]]
			if n, ok := t.(*check.Named); ok {
				t = n.Underlying()
			}
			sig, _ = t.(*check.Signature)
		} else {
			site, sig = x.Trailing, b.l.info.Literals[x.Trailing]
		}
		bind, e := b.adaptOperand(sig, site, args[skip+i], q, p)
		pre, args[skip+i] = append(pre, bind...), e
	}
	return pre
}
