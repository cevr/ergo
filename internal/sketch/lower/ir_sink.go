package lower

// IR builder: an argument evaluated on the one branch that reads it
// (CONSTRAINTS C4, decision 4 of the CONSTRAINTS spec). See ir_call.go.
//
// A call of a function whose body is one guarded return and a final
// value,
//
//	func Then(first int, next int) int {
//		if first != 0 {
//			return first
//		}
//		next
//	}
//
// reads a parameter that the guard does not read on one branch only. When
// the call's argument for that parameter is pure, cannot fail, cannot
// raise a defect and cannot stop or loop (sinkable), the IR evaluates it
// on that branch only: the call is written in place, with each other
// argument bound first, in the order the call evaluates them,
//
//	cmp.Then(cmp.Compare(a.P, b.P), cmp.Compare(a.Name, b.Name))
//	⇒  t := Compare(a.P, b.P); var r int
//	   if t != 0 { r = t } else { r = Compare(a.Name, b.Name) }
//
// so the second compare runs only on a tie, as a hand-written lazy compare
// does. Go's inliner keeps the eager form: it evaluates every argument
// before the inlined body.
//
// The rule reads facts, never a name or a package. The callee: an Ergo
// body that is pure (pure.go: no environment, no failure row, no poll),
// not generic, with no witness, owned or Go-row parameter, and whose
// guard, guarded value and final value are basic operations on its
// parameters and constants with no division or shift (sinkOperand). The
// sunk argument: constants, locals, fields of values (no dereference),
// the same basic operations, and calls of total functions (totalFunc:
// pure, no loop, no index, no division, only such calls). An argument
// that comes after the sunk one in the call must be sinkable too, so
// moving the sunk one after it changes no value. sinkLowering "off" keeps
// every call eager: the reference, as the source path's call is.
// TestSunkArgumentsAgree (internal/experimental/irspike) runs generated
// programs by the IR, by the source path and by the IR with the rule off.
//
// Prior art: Haskell is lazy, so an unread argument is never evaluated,
// and GHC's strictness analysis makes a read one eager; OCaml and Rust
// evaluate every argument first and leave laziness to closures
// (`Ordering::then_with`); Gleam's `order.lazy_break_tie` takes a
// function. MLton and GHC sink a let binding into the one branch that
// uses it (let-floating inwards); this is that floating after inlining.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sinkLowering, "off", evaluates every argument before the call:
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.sinkLowering=off.
var sinkLowering string

// SetSinkLowering turns the rule on or off for the lowerings that follow
// and returns what restores the setting before it: the reference of
// TestSunkArgumentsAgree (internal/experimental/irspike).
func SetSinkLowering(on bool) (restore func()) {
	saved := sinkLowering
	sinkLowering = "off"
	if on {
		sinkLowering = ""
	}
	return func() { sinkLowering = saved }
}

// sinkShape is a callee's guarded return: `if cond { return then }`, then
// the final value els; param is the parameter read on one branch only,
// onThen when that branch is then's.
type sinkShape struct {
	cond, then, els syntax.Expr
	param           int
	onThen          bool
}

// sinkCall is the call x of f (c) written in place with its one-branch
// argument evaluated on its branch, when the rule admits it; ok is false
// otherwise.
func (b *irBuilder) sinkCall(x *syntax.CallExpr, c *check.Call, f *check.Func, fd *funcDecl, p irPos) (pre []*irStmt, e *irExpr, ok bool) {
	if c.Recv != nil || x.Trailing != nil || c.Site != nil || len(c.TypeArgs) > 0 || b.keep > 0 || b.lawKeep > 0 {
		return nil, nil, false
	}
	if !fd.pure || fd.fallible || fd.method != nil || fd.extern != "" || len(witnessParams(f.Sig)) > 0 || len(b.l.ownedParams(f)) > 0 || b.l.hasGoRow(f.Sig) {
		return nil, nil, false
	}
	sh := b.sinkShapeOf(f)
	if sh == nil || len(x.Args) != len(f.Sig.Params) {
		return nil, nil, false
	}
	for i := sh.param; i < len(x.Args); i++ {
		if !b.sinkableArg(x.Args[i], b.l.info) {
			return nil, nil, false
		}
	}
	t := b.l.info.Types[x]
	if t == nil || b.scalarBasic(t) == nil || sinkLowering == "off" {
		return nil, nil, false
	}
	vars := paramVars(f)
	bound := map[*check.Var]*irExpr{}
	// The other arguments, in the call's order, each bound once.
	var others []syntax.Expr
	for i, a := range x.Args {
		if i != sh.param {
			others = append(others, a)
		}
	}
	opre, vals := b.operands(others)
	pre = append(pre, opre...)
	k := 0
	for i := range x.Args {
		if i == sh.param {
			continue
		}
		v := b.temp("a", b.goType(f.Sig.Params[i].Type, x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: vals[k]})
		bound[vars[i]] = &irExpr{K: "local", Pos: p, I: v}
		k++
	}
	r := b.temp("v", b.goType(t, x), p)
	pre = append(pre, &irStmt{K: "var", Pos: p, Local: r})
	info := f.Pkg.Info
	cond := b.sinkOperand(sh.cond, info, bound, p)
	// The branch that reads the argument evaluates it there, first.
	sunk := func(at syntax.Expr) []*irStmt {
		apre, a := b.operands([]syntax.Expr{x.Args[sh.param]})
		bound[vars[sh.param]] = a[0]
		return append(apre, &irStmt{K: "set", Pos: p, Local: r, X: b.sinkOperand(at, info, bound, p)})
	}
	plain := func(at syntax.Expr) []*irStmt {
		return []*irStmt{{K: "set", Pos: p, Local: r, X: b.sinkOperand(at, info, bound, p)}}
	}
	var then, els []*irStmt
	if sh.onThen {
		then, els = sunk(sh.then), plain(sh.els)
	} else {
		then, els = plain(sh.then), sunk(sh.els)
	}
	pre = append(pre, &irStmt{K: "if", Pos: p, End: p, X: cond, Then: then, Else: els, ElsePos: p, ElseEnd: p})
	return pre, &irExpr{K: "local", Pos: p, I: r}, true
}

// sinkShapeOf is f's guarded return with its one-branch parameter, or nil.
func (b *irBuilder) sinkShapeOf(f *check.Func) *sinkShape {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || len(f.Sig.TParams) > 0 || len(f.Sig.Results) != 1 || f.Pkg == nil || f.Pkg.Info == nil {
		return nil
	}
	list := d.Body.List
	if len(list) != 2 {
		return nil
	}
	ifs, ok := list[0].(*syntax.IfStmt)
	if !ok || ifs.Else != nil || len(ifs.Body.List) != 1 {
		return nil
	}
	ret, ok := ifs.Body.List[0].(*syntax.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil
	}
	var els syntax.Expr
	switch s := list[1].(type) {
	case *syntax.ExprStmt:
		els = s.X
	case *syntax.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		els = s.Results[0]
	default:
		return nil
	}
	info := f.Pkg.Info
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		return nil
	}
	for _, x := range []syntax.Expr{ifs.Cond, ret.Results[0], els} {
		if !b.sinkOperandOK(x, info, vars) {
			return nil
		}
	}
	for i, v := range vars {
		if v == nil || readsVar(info, ifs.Cond, v) {
			continue
		}
		inThen, inEls := readsVar(info, ret.Results[0], v), readsVar(info, els, v)
		if inThen != inEls {
			return &sinkShape{cond: ifs.Cond, then: ret.Results[0], els: els, param: i, onThen: inThen}
		}
	}
	return nil
}

// readsVar reports whether x names v.
func readsVar(info *check.Info, x syntax.Expr, v *check.Var) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// sinkOperandOK reports x, an expression of a callee's guarded return, as
// one sinkOperand writes: a constant, a parameter, or a basic operation
// on such operands with no division or shift.
func (b *irBuilder) sinkOperandOK(x syntax.Expr, info *check.Info, vars []*check.Var) bool {
	if _, ok := info.Values[x]; ok {
		return b.scalarBasic(info.Types[x]) != nil
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.sinkOperandOK(x.X, info, vars)
	case *syntax.Ident:
		v, ok := info.Uses[x].(*check.Var)
		if !ok || b.scalarBasic(v.Type) == nil {
			return false
		}
		for _, p := range vars {
			if p == v {
				return true
			}
		}
		return false
	case *syntax.UnaryExpr:
		switch x.Op.String() {
		case "!", "-", "+", "^":
			return b.sinkOperandOK(x.X, info, vars)
		}
	case *syntax.BinaryExpr:
		if !sinkOp(x.Op.String()) || b.scalarBasic(info.Types[x.X]) == nil {
			return false
		}
		return b.sinkOperandOK(x.X, info, vars) && b.sinkOperandOK(x.Y, info, vars)
	}
	return false
}

// sinkOp reports an operator that cannot raise a defect on basic operands.
func sinkOp(op string) bool {
	switch op {
	case "/", "%", "<<", ">>":
		return false
	}
	return irOps()[op]
}

// sinkOperand is x (sinkOperandOK), read with the callee's info, each
// parameter its bound value.
func (b *irBuilder) sinkOperand(x syntax.Expr, info *check.Info, bound map[*check.Var]*irExpr, p irPos) *irExpr {
	if v, ok := info.Values[x]; ok {
		return b.constant(info.Types[x], v, x, p)
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.sinkOperand(x.X, info, bound, p)
	case *syntax.Ident:
		v, _ := info.Uses[x].(*check.Var)
		if e := bound[v]; e != nil {
			return e
		}
	case *syntax.UnaryExpr:
		return &irExpr{K: "un", Pos: p, Op: x.Op.String(), X: b.sinkOperand(x.X, info, bound, p)}
	case *syntax.BinaryExpr:
		return &irExpr{K: "bin", Pos: p, Op: x.Op.String(), X: b.sinkOperand(x.X, info, bound, p), Y: b.sinkOperand(x.Y, info, bound, p)}
	}
	ice(x, "a guarded return's operand the sink rule did not admit")
	return nil
}

// sinkableArg reports x, an argument, as one that may be evaluated later
// or not at all with no change a program can see: a constant, a local, a
// field of a value, a basic operation with no division or shift on such
// operands, or a call of a total function (totalFunc) with such
// arguments.
func (b *irBuilder) sinkableArg(x syntax.Expr, info *check.Info) bool {
	if _, ok := info.Values[x]; ok {
		return true
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.sinkableArg(x.X, info)
	case *syntax.Ident:
		_, ok := info.Uses[x].(*check.Var)
		return ok && info.OwnedTemps[x] == nil && info.ContextValues[x] == nil
	case *syntax.SelectorExpr:
		// A field of a record value: no dereference, no accessor.
		n, ok := info.Types[x.X].(*check.Named)
		if !ok {
			return false
		}
		st, ok := n.Underlying().(*check.Struct)
		if !ok {
			return false
		}
		field := false
		for _, f := range st.Fields {
			field = field || f.Name == x.Sel.Name
		}
		return field && info.Types[x] != nil && info.ContextValues[x] == nil && b.sinkableArg(x.X, info)
	case *syntax.UnaryExpr:
		switch x.Op.String() {
		case "!", "-", "+", "^":
			return b.sinkBasic(info.Types[x.X]) && b.sinkableArg(x.X, info)
		}
	case *syntax.BinaryExpr:
		return sinkOp(x.Op.String()) && b.sinkBasic(info.Types[x.X]) && b.sinkableArg(x.X, info) && b.sinkableArg(x.Y, info)
	case *syntax.CallExpr:
		c := info.Calls[x]
		if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil || c.StartsTask || c.Site != nil || x.Trailing != nil || info.OwnedTemps[x] != nil {
			return false
		}
		for _, a := range x.Args {
			if !b.sinkableArg(a, info) {
				return false
			}
		}
		return b.totalFunc(c.Func)
	}
	return false
}

// sinkBasic reports t as an operand type whose operators (sinkOp) raise
// no defect: a basic type, or a type parameter whose constraint is a type
// set of basic types (C1: ordered, number, a user's), as in the body of a
// generic total function.
func (b *irBuilder) sinkBasic(t check.Type) bool {
	if b.scalarBasic(t) != nil {
		return true
	}
	tp, ok := t.(*check.TypeParam)
	if !ok {
		return false
	}
	it := constraintOf(tp)
	if it == nil || len(it.Terms) == 0 {
		return false
	}
	for _, term := range it.Terms {
		if b.scalarBasic(term.Type) == nil {
			return false
		}
	}
	return true
}

// totalFunc reports f as a total function: pure (pure.go), with an Ergo
// body of if statements, returns, lets and values whose expressions are
// sinkable in f's own package, so no loop, index, division or call of
// another kind. A function on a cycle of calls is not total (it might not
// end).
func (b *irBuilder) totalFunc(f *check.Func) bool {
	if b.st.totals == nil {
		b.st.totals = map[*check.Func]bool{}
	}
	if t, seen := b.st.totals[f]; seen {
		return t
	}
	b.st.totals[f] = false // on the path: a cycle is not total
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil || !b.pureCallee(f) || len(witnessParams(f.Sig)) > 0 || len(rowElems(f.Sig.Errors)) > 0 {
		return false
	}
	info := f.Pkg.Info
	var stmts func([]syntax.Stmt) bool
	stmts = func(list []syntax.Stmt) bool {
		for _, s := range list {
			switch s := s.(type) {
			case *syntax.ExprStmt:
				if !b.sinkableArg(s.X, info) {
					return false
				}
			case *syntax.ReturnStmt:
				for _, r := range s.Results {
					if !b.sinkableArg(r, info) {
						return false
					}
				}
			case *syntax.BlockStmt:
				if !stmts(s.List) {
					return false
				}
			case *syntax.IfStmt:
				if !b.sinkableArg(s.Cond, info) || !stmts(s.Body.List) {
					return false
				}
				if s.Else != nil && !stmts([]syntax.Stmt{s.Else}) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	t := stmts(d.Body.List)
	b.st.totals[f] = t
	return t
}
