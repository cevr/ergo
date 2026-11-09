package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A plain catch is a catch of one arm around a direct call of Go that
// fails go.Error: `strconv.ParseInt(s, 10, 64) catch { case go.Error: -1 }`.
// Its row is go.Error alone, and the call's error is Go's error made
// go.Error (v1.GoErrorOf), never an interruption or a defect, which leave
// as panics: so a non-nil error is always the arm's, and the type switch
// with a default panic that catch writes for a row (rows.md, Catch 6) has
// one reachable case. The plain lowering writes what hand Go writes,
//
//	r, err := strconv.ParseInt(s, int(10), int(64))
//	if err != nil {
//		r = int64(-1)
//	}
//
// with no switch, no conversion of an error the arm does not read
// (goErrorOf), and the call's own result as the catch's value when their
// Go types agree, so the function stays inside Go's inlining budget.
// An arm `go.Error as e` converts the error and asserts its Go type.
//
// SetCatchSwitchForTest selects the reference, the switch catch writes
// for every simple catch (TestPlainCatchAgrees). Prior art: Gleam's
// `case` on a Result and ReScript's `switch` on an exception lower a
// single-case match to a plain conditional for the same reason.

// catchSwitchAlways selects the reference lowering: every simple catch is
// a type switch (catch).
var catchSwitchAlways bool

// SetCatchSwitchForTest selects the type-switch lowering of every simple
// catch (true) or the plain lowering of a one-arm catch of a Go call
// (false), and returns a function that restores the previous choice.
func SetCatchSwitchForTest(always bool) func() {
	old := catchSwitchAlways
	catchSwitchAlways = always
	return func() { catchSwitchAlways = old }
}

// plainCatch reports a catch the plain lowering takes: one arm of one
// type test (or `T as e`), one subject, around a call of Go.
func (fc *funcCtx) plainCatch(x *syntax.CatchExpr, m *check.Match) bool {
	if catchSwitchAlways || len(x.Arms) != 1 || len(x.Arms[0].Types) != 1 || len(m.Subjects) != 1 || !fc.simpleCatch(x) {
		return false
	}
	return fc.goFailing(x.X)
}

// goFailing reports x a direct call of Go (import go, or a user extern),
// whose failure is Go's error as go.Error. A catch handles its call's
// failures, never its operands' (rows.md, Catch 5), so the call's error is
// the only one the catch sees.
func (fc *funcCtx) goFailing(x syntax.Expr) bool {
	call, ok := x.(*syntax.CallExpr)
	if !ok {
		return false
	}
	c := fc.l.info.Calls[call]
	return c != nil && !check.IsUnreachable(c) && (goImportCall(c, fc.typeOf) || hostCalled(c))
}

// catchBinding is the pattern `T as e` of a one-arm catch, nil when its arm
// binds nothing.
func catchBinding(info *check.Info, x *syntax.CatchExpr) *check.Pattern {
	if len(x.Arms) != 1 || len(x.Arms[0].Types) != 1 {
		return nil
	}
	p := info.Patterns[x.Arms[0].Types[0]]
	if p == nil || p.Kind != check.PatAs {
		return nil
	}
	return p
}

// plainCatchBody writes the plain catch x of the call's operand o.
func (fc *funcCtx) plainCatchBody(x *syntax.CatchExpr, m *check.Match, o operand) operand {
	result, reuse := "", false
	if m.Result != nil {
		if t, ok := fc.typed(x.X); ok && o.val != "" && goIdent(o.val) && fc.goType(t) == fc.goType(m.Result) {
			// The call's result is a temporary of the catch's Go type:
			// the arm writes over it.
			result, reuse = o.val, true
		} else {
			result = fc.temp("v")
			fc.line("var " + result + " " + fc.goType(m.Result))
		}
	} else if o.val != "" {
		fc.line("_ = " + o.val)
	}
	fc.line("if " + o.err + " != nil {")
	fc.depth++
	if p := catchBinding(fc.l.info, x); p != nil {
		name := fc.declare(p.Var)
		fc.line(name + " := " + o.err + ".(" + fc.goType(p.Elems[0].Type) + ")")
		if !fc.l.reads[p.Var] {
			fc.line("_ = " + name)
		}
	}
	fc.arm(x.Arms[0], result)
	fc.depth--
	if result != "" && !reuse && o.val != "" {
		fc.line("} else {")
		fc.line("\t" + result + " = " + o.val)
	}
	fc.line("}")
	return operand{val: result}
}

// goIdent reports s a Go identifier.
func goIdent(s string) bool {
	for i, r := range s {
		if r != '_' && !('a' <= r && r <= 'z') && !('A' <= r && r <= 'Z') && (i == 0 || !('0' <= r && r <= '9')) {
			return false
		}
	}
	return s != ""
}
