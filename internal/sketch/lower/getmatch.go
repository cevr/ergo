package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A match on m.Get(k) (lw-get-commaok, G-ex-f-6). The prelude's Get builds
// an Option, an interface value, which a match then takes apart with a
// type switch. When the match's subject is the call itself, so the Option
// is used nowhere else, and every arm reads only which variant it is and
// Some's value, the lowering reads the Map with Go's comma-ok form instead
// and branches on ok:
//
//	v, ok := m[k]
//	if ok { Some's arm } else { None's arm }
//
// Some's arm binds `Some[V] as s` to the concrete Some[V]{Value: v} (a
// struct, never boxed) and `Some{x}` to v itself. A match of any other
// shape (a constant or nested pattern inside Some, a binding of the whole
// Option, several patterns that bind) keeps the type switch. The arms
// are the same statements in the same order of evaluation: the receiver
// and the key are evaluated once, left to right, as the call's operands
// were, and the Map read cannot fail.
//
// The reference lowering is the type switch on the Option for every
// match: getLowering "switch", set by a test or by a whole build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.getLowering=switch,
// so the examples' tests and the conformance corpus run under both
// (TestGetMatchAgrees).
//
// Prior art: OCaml's flambda and Gleam do not unbox an Option across a
// match; Rust's niche layout makes Option<&T> a nullable pointer and LLVM
// folds the match into a test; hand Go writes `v, ok := m[k]`.
var getLowering string

// getArm is the arm that takes one variant of the Option and the binding
// it makes: as, a binding of the variant (Some[V] or None); value, a
// binding of Some's value.
type getArm struct {
	clause *syntax.CaseClause
	as     *check.Var
	value  *check.Var
}

// preludeGet reports the prelude's Get[K, V](m Map[K, V], k K) Option[V],
// the declaration whose body intrinsic writes.
func preludeGet(f *check.Func) bool {
	if f == nil || f.Pkg == nil || f.Pkg.Path != "prelude" || f.Decl == nil || f.Decl.Extern == nil || f.Name != "Get" {
		return false
	}
	s := f.Sig
	return len(rowElems(s.Errors)) == 0 && len(rowElems(s.Uses)) == 0 &&
		len(s.TParams) == 2 && len(s.Params) == 2 && len(s.Results) == 1 &&
		isMapOf(s.Params[0].Type, s.TParams[0], s.TParams[1]) && s.Params[1].Type == s.TParams[0] &&
		isPreludeType(s.Results[0], "Option")
}

// getMatch lowers the match s to a comma-ok read when its subject is a
// call of the prelude's Get and its arms take the shapes above, and
// reports whether it did; the second result is whether the lowering
// terminates.
func (fc *funcCtx) getMatch(s *syntax.TypeSwitchStmt, d dest) (done, terminates bool) {
	if getLowering == "switch" || len(s.Subjects) != 1 {
		return false, false
	}
	if o := fc.okSubject; o != nil && fc.isOkSubject(s) {
		// The match of a callback's comma-ok variant (update.go): its
		// subject is the Option parameter, which the variant takes as
		// the value and ok of its Map read.
		some, none, ok := fc.getArms(s.Clauses)
		if !ok {
			ice(s, "the comma-ok variant of a callback whose match getMatch does not lower")
		}
		if valueMutant == "update-ok-swap" {
			some, none = none, some
		}
		return true, fc.getArmsLower(some, none, o.val, o.ok, d)
	}
	call, ok := unparen(s.Subjects[0].X).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return false, false
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeGet(c.Func) {
		return false, false
	}
	some, none, ok := fc.getArms(s.Clauses)
	if !ok {
		return false, false
	}
	list := call.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if len(list) != 2 {
		ice(call, "a call of the prelude's Get with %d operands", len(list))
	}
	vals := fc.callArgs(list, call)
	v, okName := fc.temp("v"), fc.temp("ok")
	fc.line(v + ", " + okName + " := " + vals[0] + "[" + vals[1] + "]")
	if some.as == nil && some.value == nil {
		fc.line("_ = " + v)
	}
	return true, fc.getArmsLower(some, none, v, okName, d)
}

// getArmsLower writes the arms of a match that getMatch lowers, on the Go
// value v and the comma-ok flag okName, and reports whether they
// terminate.
func (fc *funcCtx) getArmsLower(some, none getArm, v, okName string, d dest) bool {
	arm := func(a getArm) bool {
		fc.depth++
		defer func() { fc.depth-- }()
		if a.as != nil {
			name := fc.declare(a.as)
			fields := ""
			if isPreludeType(a.as.Type, "Some") {
				fields = memberName("Value") + ": " + v
			}
			fc.line(name + " := " + fc.goType(a.as.Type) + "{" + fields + "}")
			if !fc.l.reads[a.as] {
				fc.line("_ = " + name)
			}
		}
		if a.value != nil {
			name := fc.declare(a.value)
			fc.line(name + " := " + v)
			if !fc.l.reads[a.value] {
				fc.line("_ = " + name)
			}
		}
		return fc.scoped(a.clause, func() bool { return fc.yieldList(a.clause.Body, d) })
	}
	if some.clause == none.clause {
		// One arm takes both variants (a wildcard or a type test of the
		// Option), so it binds nothing and needs no test.
		fc.line("_ = " + okName)
		fc.line("{")
		t := arm(some)
		fc.line("}")
		return t
	}
	fc.line("if " + okName + " {")
	ts := arm(some)
	fc.line("} else {")
	tn := arm(none)
	fc.line("}")
	return ts && tn
}

// getArms is the arm that takes Some and the arm that takes None, the
// first clause in order whose pattern covers each, when every pattern of
// the match takes a shape getMatch lowers.
func (fc *funcCtx) getArms(clauses []*syntax.CaseClause) (some, none getArm, ok bool) {
	return getArmsOf(fc.l.info, clauses)
}

// getArmsOf is getArms with the facts of info, the package of the match.
func getArmsOf(info *check.Info, clauses []*syntax.CaseClause) (some, none getArm, ok bool) {
	for _, cl := range clauses {
		if len(cl.List) == 0 {
			takeArm(&some, getArm{clause: cl})
			takeArm(&none, getArm{clause: cl})
			continue
		}
		for _, item := range cl.List {
			p := info.Patterns[item]
			if p == nil {
				ice(item, "a case with no Patterns entry")
			}
			a, variants, fits := getPattern(p)
			if !fits || (len(cl.List) > 1 && (a.as != nil || a.value != nil)) {
				return some, none, false
			}
			a.clause = cl
			if variants&1 != 0 {
				takeArm(&some, a)
			}
			if variants&2 != 0 {
				takeArm(&none, a)
			}
		}
	}
	return some, none, some.clause != nil && none.clause != nil
}

// takeArm makes a the arm of its variant unless an earlier clause took it.
func takeArm(slot *getArm, a getArm) {
	if slot.clause == nil {
		*slot = a
	}
}

// getPattern is the binding a pattern on an Option makes and the variants
// it takes (1 Some, 2 None), and whether getMatch lowers it: a wildcard, a
// type test of Some, None or the Option, `Some[V] as s`, `None as n`, and
// a record pattern of Some whose field is a binding or a wildcard.
func getPattern(p *check.Pattern) (a getArm, variants int, fits bool) {
	variant := func(t check.Type) int {
		switch {
		case isPreludeType(t, "Some"):
			return 1
		case isPreludeType(t, "None"):
			return 2
		case isPreludeType(t, "Option"):
			return 3
		}
		return 0
	}
	switch p.Kind {
	case check.PatWildcard:
		return a, 3, true
	case check.PatBind:
		return a, 3, p.Var == nil
	case check.PatTypeTest:
		v := variant(p.Type)
		return a, v, v != 0
	case check.PatAs:
		if len(p.Elems) != 1 || p.Elems[0].Kind != check.PatTypeTest || p.Var == nil {
			return a, 0, false
		}
		v := variant(p.Elems[0].Type)
		if v != 1 && v != 2 {
			return a, 0, false
		}
		a.as = p.Var
		return a, v, true
	case check.PatRecord:
		v := variant(p.Type)
		if v == 2 && len(p.Fields) == 0 {
			return a, 2, true
		}
		if v != 1 || len(p.Fields) != 1 || p.Fields[0].Name != "Value" {
			return a, 0, false
		}
		switch f := p.Fields[0].Pattern; f.Kind {
		case check.PatWildcard:
		case check.PatBind:
			a.value = f.Var
		default:
			return a, 0, false
		}
		return a, 1, true
	}
	return a, 0, false
}
