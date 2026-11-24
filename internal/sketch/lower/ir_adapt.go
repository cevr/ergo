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

// givenFunc is a parameter of an adapter (task is set: adaptTaskFunc's
// shape) that takes a plain Go function the std body gives the callback,
// of the Ergo function type t with no result and an empty row: w is the value
// the callback receives, a literal of t's uniform shape that calls it and
// returns no failure; local and gt are the adapter's parameter (its
// local and Go type).
func (b *irBuilder) givenFunc(t check.Type, task bool, site syntax.Node, p irPos) (w *irExpr, local int, gt string) {
	ps, ok := t.(*check.Signature)
	if !task || !ok || len(ps.Results) != 0 || len(rowElems(ps.Errors)) != 0 || len(rowElems(ps.Uses)) != 0 {
		irNo("std call that adapts a function the body gives a function")
	}
	var goParams []string
	for _, q := range ps.Params {
		goParams = append(goParams, b.goType(q.Type, site))
	}
	gt = "func(" + strings.Join(goParams, ", ") + ")"
	local = b.temp("p", gt, p)
	env := b.temp("env", b.envPointer(), p)
	w = &irExpr{K: "func", Pos: p, T: b.goType(t, site), Params: []int{env}}
	call := &irExpr{K: "vcall", Pos: p, X: &irExpr{K: "local", Pos: p, I: local}}
	for _, gp := range goParams {
		a := b.temp("a", gp, p)
		w.Params = append(w.Params, a)
		call.Args = append(call.Args, &irExpr{K: "local", Pos: p, I: a})
	}
	end := p
	w.Body = b.unitReturns([]*irStmt{{K: "expr", Pos: p, X: call}}, end)
	w.End = &end
	return w, local, gt
}

// adaptSlice is the adapter of e, the IR of a slice of Ergo function
// values (the operand at site), to a std body's slice of Go functions of
// shape (tasks.Race's blocks), as the emitter's adaptFuncs writes it: a
// new slice of e's length whose element i adapts e's element i
// (adaptOperand), each bound where the call is made.
func (b *irBuilder) adaptSlice(site syntax.Node, e *irExpr, q v1.Param, p irPos) ([]*irStmt, *irExpr) {
	x, ok := site.(syntax.Expr)
	if !ok {
		irNo("std call that adapts a slice of functions") // a trailing block is one function
	}
	t := b.l.info.Types[x]
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	s, ok := t.(*check.Slice)
	if !ok {
		irNo("std call that adapts a slice of functions")
	}
	sig, ok := s.Elem.(*check.Signature)
	if !ok {
		irNo("std call that adapts a slice of functions")
	}
	fs := b.temp("fs", b.goType(t, site), p)
	f := b.temp("f", b.goType(s.Elem, site), p)
	i := b.temp("i", "int", p)
	q.Func, q.Funcs = q.Funcs, nil // each element takes the slice's shape
	bind, lit, _ := b.adaptOperand(sig, site, &irExpr{K: "local", Pos: p, I: f}, q, p, false)
	out := b.temp("blocks", "[]"+lit.T, p)
	mk := &irExpr{K: "builtin", Pos: p, Name: "make", T: "[]" + lit.T, Args: []*irExpr{{K: "len", Pos: p, X: &irExpr{K: "local", Pos: p, I: fs}}}}
	body := append(bind, &irStmt{K: "store", Pos: p, X: &irExpr{K: "index", Pos: p, X: &irExpr{K: "local", Pos: p, I: out}, Y: &irExpr{K: "local", Pos: p, I: i}}, Y: lit})
	return []*irStmt{
		{K: "def", Pos: p, Local: fs, X: e},
		{K: "def", Pos: p, Local: out, X: mk},
		{K: "range", Pos: p, End: p, X: &irExpr{K: "local", Pos: p, I: fs}, Key: &i, Val: &f, Body: body},
	}, &irExpr{K: "local", Pos: p, I: out}
}

// adaptOperand is the adapter of e, the IR of a function value of the
// signature sig (the operand at site), to the Go parameter q of a std
// body. It returns the binding statements, the adapted value, and the
// child environment definition for a single task callback. Each adapter
// in a slice keeps its own child environment. syncHost marks a reviewed
// non-escaping Go callback that reads the environment on the calling task.
func (b *irBuilder) adaptOperand(sig *check.Signature, site syntax.Node, e *irExpr, q v1.Param, p irPos, syncHost bool) ([]*irStmt, *irExpr, *irStmt) {
	shape := q.Func
	if q.Funcs != nil {
		pre, slice := b.adaptSlice(site, e, q, p)
		return pre, slice, nil
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
		// A plain Go result cannot carry a typed failure. A reviewed
		// synchronous host callback can forward the caller's needs.
		ok = ok && values == 1 && !fallible && len(sig.Results) == 1 && len(rowElems(sig.Errors)) == 0 && (syncHost || len(rowElems(sig.Uses)) == 0)
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
	var frame *irStmt
	var env *irExpr = &irExpr{K: "local", Pos: p, I: b.env}
	if task {
		// childEnv: e := *env; e.task = t; the value runs with &e.
		tt := b.spelling("*" + b.l.taskPkg() + ".Task")
		t := b.temp("t", tt, p)
		params = append(params, tt)
		lit.Params = append(lit.Params, t)
		cp := b.temp("e", b.envValue(), p)
		frame = &irStmt{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: env}}
		lit.Body = append(lit.Body, frame,
			&irStmt{K: "store", Pos: p, X: &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: cp}}, Y: &irExpr{K: "local", Pos: p, I: t}})
		env = &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: cp}}
	}
	call := &irExpr{K: "vcall", Pos: b.litCallPos(site, e, p), X: fv, Args: []*irExpr{env}}
	for i, sp := range sig.Params {
		gt := b.goType(sp.Type, site)
		if shape.Params[i].Func != nil {
			// A function the Go body gives the callback (the send of
			// actor.StartMachine's work, MU4): a plain Go function,
			// passed to the value in the uniform shape (the emitter's
			// adaptTaskFunc wraps it the same way).
			w, gl, gfn := b.givenFunc(sp.Type, task, site, p)
			params = append(params, gfn)
			lit.Params = append(lit.Params, gl)
			call.Args = append(call.Args, w)
			continue
		}
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
	return pre, lit, frame
}

// litCallPos is the position of the adapter's call of e, the operand at
// site: for a block or a function literal the adapter calls in place, the
// position of its last statement, as the emitter's call follows the
// literal's text, whose closing line repeats that statement's directive
// (adaptFunc). Go reads the adapter's frame line there, so the report
// writes the closure around an exit action's block as part of the
// action's frame, not a call of scope.OnExit (runtime.md, Fork-tree defects 3;
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
// not take (skip 1). The trailing block's child environment definition
// is returned separately for spawn lowering.
func (b *irBuilder) adaptOperands(params []v1.Param, x *syntax.CallExpr, ops []syntax.Expr, args []*irExpr, skip int, p irPos) (pre []*irStmt, frame *irStmt) {
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
		bind, e, child := b.adaptOperand(sig, site, args[skip+i], q, p, false)
		pre, args[skip+i] = append(pre, bind...), e
		if i >= len(ops) {
			frame = child
		}
	}
	return pre, frame
}
