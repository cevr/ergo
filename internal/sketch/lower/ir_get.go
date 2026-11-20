package lower

// IR builder: a match on m.Get(k) (F7 lane B). See ir_match.go.
//
// The emitter's getMatch (getmatch.go) reads the Map with Go's comma-ok
// form when the match's subject is the call of the prelude's Get and its
// arms take getArmsOf's shapes; the IR builds the same form:
//
//	v, ok := m[k]          ("mapget": Local v, Val ok, X m, Y k)
//	if ok { Some's arm } else { None's arm }
//
// The receiver and the key are evaluated once, left to right (operands).
// When None's clause comes first in the source the test is !ok, so the
// arms keep their source order and the blocks' positions increase (DWARF
// scopes). One clause that takes both variants binds nothing: ok is
// discarded and the arm's statements follow. An arm is an if's branch, not
// a switch case, so a break in it leaves the loop (no b.switches).
//
// A match on Get of another shape, or any match under getLowering
// "switch" (the type switch reference), keeps the gate "match on Get with
// patterns": the emitter then switches on the Option, which the IR does
// not build.
//
// Prior art: hand Go writes `v, ok := m[k]`; Rust's Option<&V> from
// HashMap::get is a nullable pointer the match tests; OCaml's
// Hashtbl.find_opt allocates the Some, which flambda does not remove.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// getMatch builds the match s when its subject is a call of the prelude's
// Get, and reports whether s is such a match (built or refused).
func (b *irBuilder) getMatch(s *syntax.TypeSwitchStmt, ret bool, to *yieldTo) ([]*irStmt, bool) {
	if len(s.Subjects) != 1 {
		return nil, false
	}
	call, ok := unparen(s.Subjects[0].X).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeGet(c.Func) {
		return nil, false
	}
	if getLowering == "switch" || call.Trailing != nil {
		irNo("match on Get with patterns")
	}
	some, none, fits := getArmsOf(b.l.info, s.Clauses)
	if !fits {
		irNo("match on Get with patterns")
	}
	if len(b.l.info.Releases[s.Subjects[0]]) > 0 || len(b.l.info.Releases[s.Subjects[0].X]) > 0 {
		irNo("match subject that releases owned values")
	}
	list := call.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if len(list) != 2 {
		irNo("call of Get with %d operands", len(list))
	}
	mt, isMap := b.l.info.Types[list[0]].(*check.Map)
	if !isMap {
		irNo("Get of a value that is no Map")
	}
	p := b.pos(s, s.Pos())
	pre, ops := b.operands(list)
	p = past(p, pre)
	v := b.temp("v", b.goType(mt.Elem, call), p)
	okv := b.temp("ok", "bool", p)
	if b.l.intMap(mt) {
		pre = append(pre, intMapGet(p, v, okv, ops[0], ops[1])) // ir_intmap.go
	} else {
		pre = append(pre, &irStmt{K: "mapget", Pos: p, Local: v, Val: &okv, X: ops[0], Y: ops[1]})
	}
	okx := &irExpr{K: "local", Pos: p, I: okv}
	if some.clause == none.clause {
		return append(pre, b.getArm(some, v, ret, to)...), true
	}
	first, second, cond := some, none, okx
	if none.clause.Case < some.clause.Case {
		first, second, cond = none, some, &irExpr{K: "un", Pos: p, Op: "!", X: okx}
	}
	st := &irStmt{K: "if", Pos: p, X: cond, End: b.pos(second.clause, second.clause.Case)}
	st.Then = b.getArm(first, v, ret, to)
	st.ElsePos, st.ElseEnd = st.End, b.pos(s, s.Rbrace)
	st.Else = b.getArm(second, v, ret, to)
	return append(pre, st), true
}

// getArm is the arm a, its bindings first: `Some[V] as s` is
// Some[V]{Value: v}, `None as n` None's zero record, `Some{x}` is v.
func (b *irBuilder) getArm(a getArm, v int, ret bool, to *yieldTo) []*irStmt {
	cp := b.pos(a.clause, a.clause.Case)
	var out []*irStmt
	bind := func(x *check.Var, val *irExpr) {
		if _, dup := b.locals[x]; dup {
			irNo("local declared twice")
		}
		name := b.temp(x.Name, b.goType(x.Type, a.clause), cp)
		b.locals[x] = name
		out = append(out, &irStmt{K: "def", Pos: cp, Local: name, X: val})
	}
	if a.as != nil {
		lit := &irExpr{K: "lit", Pos: cp, T: b.goType(a.as.Type, a.clause)}
		if isPreludeType(a.as.Type, "Some") {
			lit.Keys = []string{memberName("Value")}
			lit.Elems = []*irExpr{{K: "local", Pos: cp, I: v}}
		}
		bind(a.as, lit)
	}
	if a.value != nil {
		bind(a.value, &irExpr{K: "local", Pos: cp, I: v})
	}
	if len(a.clause.Body) == 0 && (ret || to != nil) {
		irNo("empty case with a result")
	}
	return append(out, b.tailTo(a.clause.Body, ret && to == nil, to)...)
}
