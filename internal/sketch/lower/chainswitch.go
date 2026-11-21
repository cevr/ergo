package lower

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Comparison chains (wf10 L2, S2b's opKind). A run of consecutive
// statements of one list, each `if c == K { … }` on the same value c,
// lowers to one Go `switch c { case K: … }`, and the list goes on after
// it. gc compiles a switch over constants to a binary search or a jump
// table; the chain compares them one at a time (S2b's opKind: 55.6
// instructions per call as a chain, 41.2 as a switch; L2 red-check.md).
//
// An arm joins the run when:
//   - it is an if with no else, whose condition is c == K or K == c;
//   - c is the same immutable local or parameter in every arm (a let, not
//     a let mut; no field, no call, no package let), of an integer type;
//   - K is a constant (Info.Values, or a let lowered as a Go constant)
//     that no earlier arm of the run takes: a duplicate ends the run, as
//     Go refuses duplicate cases and the later arm never runs;
//   - its body leaves (return, continue, a defect), so no arm falls on to
//     the next test;
//   - its body holds no break outside a loop of its own (refused as the
//     simpler rule: the emitter would have to label it past the switch).
//
// With c immutable and each arm leaving, at most one arm runs and the run
// does what the chain does. A run needs chainMinArms arms.
//
// The reference is chainLowering "if": every chain lowers as written. It
// is set by -ldflags=-X=ergo.local/prototype/internal/sketch/lower.chainLowering=if
// and by referenceLowerings in the tests.
//
// Prior art: GHC's case-of-known-constructor and Gleam's and ReScript's
// pattern compilers turn a sequence of literal tests on one scrutinee into
// one case; Go's own compiler does it only for a switch the source wrote.
var chainLowering string

// chainMinArms is the shortest run that becomes a switch.
const chainMinArms = 2

// chainArm is one arm of a run: its if and its constant.
type chainArm struct {
	s   *syntax.IfStmt
	key constant.Value
}

// compareChain is the run of arms that starts at list[i], with its
// subject (c's name at the first arm), or nil when fewer than chainMinArms
// arms start there.
func (fc *funcCtx) compareChain(list []syntax.Stmt, i int) (*syntax.Ident, []chainArm) {
	if chainLowering == "if" {
		return nil, nil
	}
	var subject *syntax.Ident
	var v *check.Var
	var arms []chainArm
	seen := map[string]bool{}
	for ; i < len(list); i++ {
		s, ok := list[i].(*syntax.IfStmt)
		if !ok || s.Else != nil {
			break
		}
		id, key, ok := fc.chainTest(s.Cond)
		if !ok {
			break
		}
		u := fc.l.info.Uses[id].(*check.Var)
		if v != nil && u != v {
			break
		}
		k := caseKey(u.Type, key)
		if seen[k] || !chainLeaves(fc, s.Body.List) || chainBreaks(s.Body) {
			break
		}
		seen[k] = true
		if v == nil {
			subject, v = id, u
		}
		arms = append(arms, chainArm{s: s, key: key})
	}
	if len(arms) < chainMinArms {
		return nil, nil
	}
	return subject, arms
}

// chainTest is cond as c == K: c's name and K's value.
func (fc *funcCtx) chainTest(cond syntax.Expr) (*syntax.Ident, constant.Value, bool) {
	b, ok := unparen(cond).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.EQL {
		return nil, nil, false
	}
	for _, side := range [][2]syntax.Expr{{b.X, b.Y}, {b.Y, b.X}} {
		id, ok := unparen(side[0]).(*syntax.Ident)
		if !ok || !fc.chainSubject(id) {
			continue
		}
		if key := fc.chainConst(side[1]); key != nil {
			return id, key, true
		}
	}
	return nil, nil, false
}

// chainSubject reports whether id names an immutable local or parameter of
// an integer type.
func (fc *funcCtx) chainSubject(id *syntax.Ident) bool {
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok || v.Mut() || !isInteger(v.Type) {
		return false
	}
	_, pkgLet := fc.l.vars[v]
	return !pkgLet
}

// chainConst is x's constant integer value, or nil.
func (fc *funcCtx) chainConst(x syntax.Expr) constant.Value {
	x = unparen(x)
	if v, ok := fc.l.info.Values[x]; ok {
		if v.Kind() == constant.Int {
			return v
		}
		return nil
	}
	if id, ok := x.(*syntax.Ident); ok {
		if u, ok := fc.l.info.Uses[id].(*check.Var); ok && fc.l.consts[u] {
			if v := fc.l.constVals[u]; v != nil && v.Kind() == constant.Int {
				return v
			}
		}
	}
	return nil
}

// chainLeaves reports whether a statement list always leaves: its last
// statement returns, continues, raises a defect, or is an if with an else
// or a block whose lists leave.
func chainLeaves(fc *funcCtx, list []syntax.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	switch s := list[len(list)-1].(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.BranchStmt:
		return s.Tok == syntax.CONTINUE
	case *syntax.ExprStmt:
		return fc.unreachableCall(s.X) != nil
	case *syntax.BlockStmt:
		return chainLeaves(fc, s.List)
	case *syntax.IfStmt:
		switch e := s.Else.(type) {
		case *syntax.BlockStmt:
			return chainLeaves(fc, s.Body.List) && chainLeaves(fc, e.List)
		case *syntax.IfStmt:
			return chainLeaves(fc, s.Body.List) && chainLeaves(fc, []syntax.Stmt{e})
		}
	}
	return false
}

// chainBreaks reports a break in body that is not inside a loop of body's
// own (nor inside a function literal, which no break leaves).
func chainBreaks(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ForStmt, *syntax.RangeStmt, *syntax.FuncLit:
			return false
		case *syntax.BranchStmt:
			if n.Tok == syntax.BREAK {
				found = true
			}
		}
		return !found
	})
	return found
}

// chainSwitch (IR) appends to out the run of arms at list[i] as one IR
// "switch" (ir_match.go's form: a constant per case, no default) and
// returns how many statements it took, or 0. yields says the list's last
// statement is its result, which no run takes.
func (b *irBuilder) chainSwitch(out *[]*irStmt, list []syntax.Stmt, i int, yields bool) int {
	if yields {
		list = list[:len(list)-1]
	}
	if i >= len(list) {
		return 0
	}
	subject, arms := b.fc.compareChain(list, i)
	if arms == nil {
		return 0
	}
	n := 0
	b.part(func() {
		t := b.l.info.Uses[subject].(*check.Var).Type
		first, end := arms[0].s, arms[len(arms)-1].s.Body
		pre, x := b.value(subject)
		if len(pre) > 0 {
			irNo("chain subject with statements")
		}
		sw := &irStmt{K: "switch", Pos: b.pos(first, first.Pos()), X: x, End: b.pos(end, end.Rbrace)}
		sw.ElsePos = sw.End
		b.switches++
		defer func() { b.switches-- }()
		for _, a := range arms {
			if len(b.l.info.Releases[a.s]) > 0 {
				irNo("statement that releases owned values")
			}
			key := b.constant(t, a.key, a.s.Cond, b.pos(a.s.Cond, a.s.Cond.Pos()))
			c := &irCase{Pos: b.pos(a.s, a.s.Pos()), Vals: []*irExpr{key}}
			c.Body = b.tailTo(a.s.Body.List, false, nil)
			sw.Cases = append(sw.Cases, c)
		}
		*out = append(*out, sw)
		n = len(arms)
	})
	return n
}

// chainSwitch writes the run of arms at list[i] as one Go switch and
// returns how many statements it took, or 0 when no run starts there.
func (fc *funcCtx) chainSwitch(list []syntax.Stmt, i int) int {
	subject, arms := fc.compareChain(list, i)
	if arms == nil {
		return 0
	}
	t := fc.l.info.Uses[subject].(*check.Var).Type
	fc.at(arms[0].s)
	fc.line("switch " + fc.headerValue(subject) + " {")
	fc.enterSwitch()
	for _, a := range arms {
		fc.at(a.s)
		fc.line("case " + fc.constant(t, a.key, a.s.Cond) + ":")
		fc.depth++
		mark := fc.factMark()
		fc.assume(a.s.Cond) // bounds.go: the facts the test gives its arm
		fc.scoped(a.s.Body, func() bool { return fc.yieldList(a.s.Body.List, dest{}) })
		fc.factReset(mark)
		fc.depth--
	}
	fc.leaveSwitch()
	fc.line("}")
	for _, a := range arms {
		fc.releaseAt(a.s) // each if's temporaries, as stmts releases them (R43)
	}
	return len(arms)
}
