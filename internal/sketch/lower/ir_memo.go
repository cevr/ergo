package lower

// IR builder: loop-invariant pure calls (LAWFFI LF3a; plan-r1 §2, `pure`:
// "a loop-invariant call moves before the loop"). See ir.go.
//
// A call in a loop's body of a pure callee (an Ergo function the lowering
// emits pure, a pure std body, a reviewed-pure Go function: check.PureStd)
// whose operands are the same on every iteration runs once per entry of
// the loop: the loop's pre-header declares the call's result and a flag,
// and the call's site computes the result the first time it is reached
// and reads it after,
//
//	var t T; var done bool
//	for … { … if !done { t = f(x); done = true }; … t … }
//
// The call keeps its place on the first iteration that reaches it, so a
// defect, an effect before it and a loop that never reaches it are all as
// they were; a later iteration reads the value the same call gave. That is
// the guarded pre-header the plan names, placed at the call: it needs no
// pure loop condition, no "before any effect" rule and no `total`. Only a
// call that is a whole value (irBuilder.value: a let's, an assignment's, an
// argument's) is memoized, where statements may already run before the
// value; an operator's operand keeps its call. The
// flag is declared just before the loop statement, so each entry of the
// loop (an inner loop's, per outer iteration) computes the call again.
//
// The call is the same on every iteration when each operand is a literal,
// a constant, or a name bound outside the loop that no assignment
// reaches: a `let` without mut or a parameter (D-D1: only a `let mut` is
// assigned). Outside the loop is the body's own binding, not the
// source's position: the name's local was declared before the loop's
// entry (irMemo.first). A body built into the loop at its site (a fused
// match's callee, ir_forms_match.go) binds its parameters and lets on
// each iteration, though its source comes before the loop in its file; a
// parameter it binds to the caller's local is that local. Operands and
// the result are of basic types (a number, a bool, a string), so the
// shared result is a value no iteration writes.
// Everything else keeps its call. -X lower.pureHoist=off turns it off:
// the reference, the IR without the change.
//
// When LAWFFI LF2d's stack meets this one, the purity read goes through
// its ledger gate (check.PureActs for a Go package): check.PureStd reads
// the reviewed pure tier, which LF2d gates.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// pureHoist, "off", turns the loop-invariant pure call off: set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.pureHoist=off.
var pureHoist string

// SetPureHoist sets the loop-invariant pure call on or off for the
// lowerings that follow and returns what restores the setting before it:
// the reference of TestPureHoistAgrees (internal/experimental/irspike).
func SetPureHoist(on bool) (restore func()) {
	saved := pureHoist
	pureHoist = "off"
	if on {
		pureHoist = ""
	}
	return func() { pureHoist = saved }
}

// irMemo is one loop of the function being built whose body may hold
// memoized calls: the first local declared at or after the loop's entry,
// and the declarations its pre-header gets. A site that rolls
// the builder back (unmark: a fused match that falls back, a law's
// pattern) drops the declarations its memoized calls added.
type irMemo struct {
	first int
	decls []*irStmt
}

// memoLoop builds the statements of a loop with build, then puts the
// declarations of the calls its body memoized before them. first is the
// builder's next local before the loop bound any name of its own (a
// range's index and element, declared before its body is built): every
// local from first on may change between iterations.
func (b *irBuilder) memoLoop(first int, build func() []*irStmt) []*irStmt {
	m := &irMemo{first: first}
	depth := len(b.memos)
	b.memos = append(b.memos, m)
	// A refusal inside the loop unwinds through here (irNo): the stack
	// still loses this loop.
	defer func() { b.memos = b.memos[:depth] }()
	out := build()
	if len(m.decls) == 0 {
		return out
	}
	return append(m.decls, out...)
}

// memoCall is the memoized form of the call x, built as (pre, e), when x
// is a loop-invariant pure call of the innermost loop: the statements that
// compute it the first time and the expression that reads it. ok is false
// for every other call, which keeps its form.
func (b *irBuilder) memoCall(x *syntax.CallExpr, pre []*irStmt, e *irExpr) ([]*irStmt, *irExpr, bool) {
	if pureHoist == "off" || len(b.memos) == 0 || e == nil {
		return nil, nil, false
	}
	// A Go function's call runs into a temporary first, as an effect's
	// does (hostCall, hoist): the temporary's definition is the call.
	if len(pre) == 1 && pre[0].K == "def" && e.K == "local" && e.I == pre[0].Local {
		e, pre = pre[0].X, nil
	}
	c := b.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil {
		// A conversion, a builtin, an operation: not a call the pass
		// reads.
		return nil, nil, false
	}
	if len(pre) > 0 {
		b.formSite(x, memoStatements) // ir_forms_report.go
		return nil, nil, false
	}
	m := b.memos[len(b.memos)-1]
	if r := b.invariantPureCall(x, m); r != nil {
		b.formSite(x, r)
		return nil, nil, false
	}
	b.formSite(x, memoFired)
	p := e.Pos
	t := b.goType(b.l.info.Types[x], x)
	v, done := b.temp("memo", t, p), b.temp("memoDone", "bool", p)
	m.decls = append(m.decls, &irStmt{K: "var", Pos: p, Local: v}, &irStmt{K: "var", Pos: p, Local: done})
	b.memoSites++
	first := &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: &irExpr{K: "local", Pos: p, I: done}}, Then: []*irStmt{
		{K: "set", Pos: p, Local: v, X: e},
		{K: "set", Pos: p, Local: done, X: &irExpr{K: "const", Pos: p, T: "bool", V: "true"}},
	}}
	return []*irStmt{first}, &irExpr{K: "local", Pos: p, I: v}, true
}

// invariantPureCall is nil for a call x, in the body of m's loop, of a
// pure callee with operands that are the same on every iteration, all of
// basic types, as its result is; otherwise the reason it is not.
func (b *irBuilder) invariantPureCall(x *syntax.CallExpr, m *irMemo) *FormReason {
	info := b.l.info
	c := info.Calls[x]
	if c.Recv != nil || x.Trailing != nil || c.StartsTask || c.Site != nil {
		return memoKind
	}
	if !b.pureCallee(c.Func) {
		return memoImpure
	}
	if !irMemoBasic(info.Types[x]) {
		return memoNotBasic
	}
	for _, a := range x.Args {
		if !irMemoBasic(info.Types[a]) {
			return memoNotBasic
		}
	}
	if b.foldable(c.Func, x) { // ir_fold.go
		return memoFolds
	}
	for _, a := range x.Args {
		if !b.loopInvariant(a, m) {
			return memoVariant
		}
	}
	return nil
}

// pureCallee reports a callee whose call is a function of its operands: a
// pure std body or a reviewed-pure Go function (check.PureStd), or an
// Ergo function the lowering emits pure (pure.go).
func (b *irBuilder) pureCallee(f *check.Func) bool {
	if check.PureStd(f) {
		return true
	}
	return f.Decl != nil && f.Decl.Body != nil && f.Decl.Extern == nil && b.l.isPure(f)
}

// loopInvariant reports an operand that is the same on every iteration of
// m's loop: a literal, a constant, or a name no assignment reaches whose
// local this body declared before the loop's entry.
func (b *irBuilder) loopInvariant(a syntax.Expr, m *irMemo) bool {
	switch a := a.(type) {
	case *syntax.BasicLit:
		return true
	case *syntax.ParenExpr:
		return b.loopInvariant(a.X, m)
	case *syntax.Ident:
		switch o := b.l.info.Uses[a].(type) {
		case *check.Const:
			return true
		case *check.Var:
			l, ok := b.locals[o]
			return ok && !o.Mut() && l < m.first
		}
	}
	return false
}

// irMemoBasic reports a type whose values a memoized call may take and
// give: a number, a bool or a string.
func irMemoBasic(t check.Type) bool {
	bt, ok := t.(*check.Basic)
	return ok && bt.Kind != check.Invalid
}
