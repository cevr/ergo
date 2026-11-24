package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Compiled yields (ORACLE O35, TESTSPEED FT4). yieldStmt runs a statement
// that may give a value: a body's or an arm's last statement, an if, a
// block, a match. Its form charges what yieldStmt charges, in its order,
// and reads once what yieldStmt reads at every run: a match's patterns,
// their types and constants, its default clause, each arm's body form.

// fastYield is yieldStmt of one statement.
type fastYield func(in *interp, fr *frame) (Value, *abrupt)

// patTest is match of one checked pattern: whether v matches, its
// bindings bound.
type patTest func(in *interp, fr *frame, v Value) (bool, *abrupt)

// yieldForm is the form of yieldStmt(s, ret), or a call of the walk.
func (in *interp) yieldForm(fr *frame, s syntax.Stmt, ret bool) fastYield {
	if g := in.compileYield(fr, s, ret); g != nil {
		return g
	}
	return func(in *interp, fr *frame) (Value, *abrupt) { return in.yieldStmt(fr, s, ret) }
}

// compileYield is the form of yieldStmt(s, ret), or nil when s has none.
func (in *interp) compileYield(fr *frame, s syntax.Stmt, ret bool) fastYield {
	if !fr.forms() {
		return nil
	}
	var at syntax.Node = s
	switch s := s.(type) {
	case *syntax.ExprStmt:
		x := s.X
		if ret {
			r := in.compileRet(fr, x)
			return func(in *interp, fr *frame) (Value, *abrupt) {
				if ab := in.charge(fr, at, 1); ab != nil {
					return nil, ab
				}
				return r(in, fr)
			}
		}
		if _, tail := fr.info.Tails[s]; tail {
			r := in.compileResult(fr, x)
			return func(in *interp, fr *frame) (Value, *abrupt) {
				if ab := in.charge(fr, at, 1); ab != nil {
					return nil, ab
				}
				return r(in, fr)
			}
		}
		e := in.operand(fr, x, in.fx(fr, x))
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1); ab != nil {
				return nil, ab
			}
			if _, ab := e(in, fr); ab != nil {
				return nil, escape(ab)
			}
			return nil, nil
		}
	case *syntax.IfStmt:
		return in.compileIfYield(fr, s, ret)
	case *syntax.BlockStmt:
		body := in.compileScope(fr, s, s.List, ret)
		madeForm(s)
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1); ab != nil {
				return nil, ab
			}
			return body(in, fr)
		}
	case *syntax.SwitchStmt:
		g := in.compileValueMatch(fr, s, ret)
		if g != nil {
			madeForm(s)
		}
		return g
	case *syntax.TypeSwitchStmt:
		g := in.compileTypeMatch(fr, s, ret)
		if g != nil {
			madeForm(s)
		}
		return g
	}
	core := in.compileStmtCore(fr, s)
	if core == nil {
		return nil
	}
	return func(in *interp, fr *frame) (Value, *abrupt) { return nil, core(in, fr) }
}

// compileResult is the form of resultExpr(x): x's position read once, its
// operand form bound.
func (in *interp) compileResult(fr *frame, x syntax.Expr) fastExpr {
	origin := unparen(x)
	for {
		t, ok := origin.(*syntax.TryExpr)
		if !ok {
			break
		}
		origin = unparen(t.X)
	}
	pos := origin.Pos()
	reset := true
	switch origin.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.CatchExpr:
		reset = false
	}
	e := in.operand(fr, x, in.fx(fr, x))
	return func(in *interp, fr *frame) (Value, *abrupt) {
		fr.resultPos = pos
		v, ab := e(in, fr)
		if reset {
			fr.resultPos = pos
		}
		return v, ab
	}
}

// compileRet is the form of retValue(x): the returned local named once,
// the result's form, and whether its value is a failure read from x's
// facts with no lookup.
func (in *interp) compileRet(fr *frame, x syntax.Expr) fastExpr {
	id, _ := unparen(x).(*syntax.Ident)
	res := in.compileResult(fr, x)
	xf := in.fx(fr, x)
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if id != nil {
			fr.returned = id // a returned owned local stays the frame's (own.go, keep)
		}
		v, ab := res(in, fr)
		fr.returned = nil
		if ab != nil {
			return nil, escape(ab)
		}
		if !xf.errSeen {
			xf.errIs = fr.returnIsFailure(x)
			xf.errSeen = true
		}
		if xf.errIs {
			if _, isRecord := v.(*record); isRecord {
				return nil, fr.abrupt(ctlEscape, x, v, "")
			}
		}
		return v, nil
	}
}

// compileIfYield is yieldStmt's if: its branches yield.
func (in *interp) compileIfYield(fr *frame, s *syntax.IfStmt, ret bool) fastYield {
	var at syntax.Node = s
	cond := in.operand(fr, s.Cond, in.fx(fr, s.Cond))
	condRel := in.releasesOf(fr, s.Cond)
	body := in.compileScope(fr, s.Body, s.Body.List, ret)
	var els fastYield
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		els = in.compileScope(fr, e, e.List, ret)
	case *syntax.IfStmt:
		els = in.compileIfYield(fr, e, ret)
	default:
		return nil
	}
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		c, ab := cond(in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		if condRel != nil {
			if ab := condRel(in, fr); ab != nil {
				return nil, ab
			}
		}
		if truth(c) {
			return body(in, fr)
		}
		if els != nil {
			return els(in, fr)
		}
		return nil, nil
	}
}

// compileScope is the form of scope(key, list, ret).
func (in *interp) compileScope(fr *frame, key syntax.Node, list []syntax.Stmt, ret bool) fastYield {
	y := in.compileList(fr, list, ret)
	rel := in.releasesOf(fr, key)
	if rel == nil {
		return y
	}
	return func(in *interp, fr *frame) (Value, *abrupt) {
		v, ab := y(in, fr)
		if ab == nil {
			ab = rel(in, fr)
		}
		return v, ab
	}
}

// compileList is the form of yield(list, ret): the statements but the
// last as statement forms, then the last's yield and its releases.
func (in *interp) compileList(fr *frame, list []syntax.Stmt, ret bool) fastYield {
	if len(list) == 0 {
		return func(in *interp, fr *frame) (Value, *abrupt) { return nil, nil }
	}
	prefix := in.compileBlock(fr, list[:len(list)-1])
	lastStmt := list[len(list)-1]
	last := in.yieldForm(fr, lastStmt, ret)
	rel := in.releasesOf(fr, lastStmt)
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := prefix(in, fr); ab != nil {
			return nil, ab
		}
		v, ab := last(in, fr)
		if ab == nil && rel != nil {
			ab = rel(in, fr)
		}
		return v, ab
	}
}

// armForm is one clause of a compiled match: its patterns' tests, in
// written order, and its body's form.
type armForm struct {
	cl    *syntax.CaseClause
	items []syntax.Node
	tests []patTest
	body  fastYield
}

// run is arm: the clause's ownership step, then its body.
func (a *armForm) run(in *interp, fr *frame) (Value, *abrupt) {
	in.patternOwners(fr, a.cl)
	return a.body(in, fr)
}

// compileTypeMatch is the form of typeMatch, or nil when a pattern has
// no form.
func (in *interp) compileTypeMatch(fr *frame, s *syntax.TypeSwitchStmt, ret bool) fastYield {
	if in.sx(fr, s).match == nil {
		return nil
	}
	var arms []*armForm
	var def *armForm
	for _, cl := range s.Clauses {
		a := &armForm{cl: cl}
		if len(cl.List) == 0 {
			def = a
		} else {
			arms = append(arms, a)
		}
		for _, item := range cl.List {
			p := in.fx(fr, item).pattern
			if p == nil {
				return nil
			}
			t := in.compilePattern(fr, item, p)
			if t == nil {
				return nil
			}
			a.items, a.tests = append(a.items, item), append(a.tests, t)
		}
	}
	for _, a := range append(arms, def) {
		if a != nil {
			a.body = in.compileScope(fr, a.cl, a.cl.Body, ret)
		}
	}
	n := len(s.Subjects)
	subj := make([]fastExpr, n)
	subjRel := make([]fastStmt, 2*n)
	for i, g := range s.Subjects {
		subj[i] = in.operand(fr, g.X, in.fx(fr, g.X))
		subjRel[2*i], subjRel[2*i+1] = in.releasesOf(fr, g.X), in.releasesOf(fr, g)
	}
	var at syntax.Node = s
	subject := func(in *interp, fr *frame, i int) (Value, *abrupt) {
		v, ab := subj[i](in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		for _, r := range subjRel[2*i : 2*i+2] {
			if r != nil {
				if ab := r(in, fr); ab != nil {
					return nil, ab
				}
			}
		}
		return v, nil
	}
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		var sv Value
		if n == 1 {
			v, ab := subject(in, fr, 0)
			if ab != nil {
				return nil, ab
			}
			sv = v
		} else {
			subjects := make([]Value, n)
			for i := range subjects {
				v, ab := subject(in, fr, i)
				if ab != nil {
					return nil, ab
				}
				subjects[i] = v
			}
			sv = tuple{elems: subjects}
		}
		// typeMatch then releases at s. A compiled match is always a
		// statement of a list (compileBlock, compileList), and the list
		// releases the statement's key, s, right after it, with nothing in
		// between, so the form leaves that release to the list.
		return in.runArms(fr, s, arms, def, sv)
	}
}

// runArms is typeArms over compiled arms.
func (in *interp) runArms(fr *frame, s syntax.Node, arms []*armForm, def *armForm, v Value) (Value, *abrupt) {
	for _, a := range arms {
		for i, t := range a.tests {
			if ab := in.charge(fr, a.items[i], 1); ab != nil {
				return nil, ab
			}
			ok, ab := t(in, fr, v)
			if ab != nil {
				return nil, ab
			}
			if ok {
				return a.run(in, fr)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return def.run(in, fr)
}

// compilePattern is the form of match for p, read in a frame with no
// substitution, or nil when p has none.
func (in *interp) compilePattern(fr *frame, at syntax.Node, p *check.Pattern) patTest {
	switch p.Kind {
	case check.PatWildcard:
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) { return true, nil }
	case check.PatBind:
		w := p.Var
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) {
			fr.bind(w, v)
			return true, nil
		}
	case check.PatTypeTest:
		has := typeTest(fr.rt(p.Type))
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) { return has(v), nil }
	case check.PatRecord:
		has := typeTest(fr.rt(p.Type))
		subs := make([]patTest, len(p.Fields))
		names := make([]string, len(p.Fields))
		for i, f := range p.Fields {
			if subs[i] = in.compilePattern(fr, at, f.Pattern); subs[i] == nil {
				return nil
			}
			names[i] = f.Name
		}
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) {
			if !has(v) {
				return false, nil
			}
			r := v.(*record)
			for i, sub := range subs {
				ok, ab := sub(in, fr, r.fields[fieldIndex(r.t, names[i])])
				if !ok || ab != nil {
					return false, ab
				}
			}
			return true, nil
		}
	case check.PatConst:
		c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
		if !ok {
			return nil // the walk names it
		}
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) { return in.equal(fr, at, v, c) }
	case check.PatTuple:
		subs := make([]patTest, len(p.Elems))
		for i, e := range p.Elems {
			if subs[i] = in.compilePattern(fr, at, e); subs[i] == nil {
				return nil
			}
		}
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) {
			t, ok := v.(tuple)
			if !ok || len(t.elems) != len(subs) {
				ice("a tuple pattern over %T", v)
			}
			for i, sub := range subs {
				ok, ab := sub(in, fr, t.elems[i])
				if !ok || ab != nil {
					return false, ab
				}
			}
			return true, nil
		}
	case check.PatAs:
		if len(p.Elems) != 1 {
			return func(in *interp, fr *frame, v Value) (bool, *abrupt) { return false, nil }
		}
		sub := in.compilePattern(fr, at, p.Elems[0])
		if sub == nil {
			return nil
		}
		w := p.Var
		return func(in *interp, fr *frame, v Value) (bool, *abrupt) {
			ok, ab := sub(in, fr, v)
			if !ok || ab != nil {
				return false, ab
			}
			fr.bind(w, v)
			return true, nil
		}
	}
	return nil
}

// recordKind is one record type a type test takes: its declaration and
// its type arguments' key (hasType's last case).
type recordKind struct {
	obj *check.TypeName
	key string
}

// typeTest is hasType(·, t) with t's variants read once.
func typeTest(t rtype) func(Value) bool {
	var kinds []recordKind
	collectKinds(t, &kinds)
	if len(kinds) == 1 {
		k := kinds[0]
		return func(v Value) bool {
			r, ok := v.(*record)
			return !ok || r.t.Obj == k.obj && r.key == k.key
		}
	}
	return func(v Value) bool {
		r, ok := v.(*record)
		if !ok {
			return true
		}
		for _, k := range kinds {
			if r.t.Obj == k.obj && r.key == k.key {
				return true
			}
		}
		return false
	}
}

// collectKinds lists the record types hasType takes for t, in its order.
func collectKinds(t rtype, out *[]recordKind) {
	t = t.env.resolve(t.t)
	switch tt := t.t.(type) {
	case *check.Union:
		for _, variant := range tt.Variants {
			collectKinds(rtype{variant, t.env}, out)
		}
	case *check.Named:
		if u, isUnion := tt.Underlying().(*check.Union); isUnion {
			collectKinds(rtype{u, t.env}, out)
			return
		}
		*out = append(*out, recordKind{tt.Obj, argsKey(tt, t.env)})
	}
}

// valueCase is one constant case of a compiled value match.
type valueCase struct {
	at  syntax.Node
	c   Value
	arm *armForm
}

// compileValueMatch is the form of valueMatch, or nil when a case has
// none.
func (in *interp) compileValueMatch(fr *frame, s *syntax.SwitchStmt, ret bool) fastYield {
	if in.sx(fr, s).match == nil {
		return nil
	}
	var cases []valueCase
	var def *armForm
	for _, cl := range s.Clauses {
		a := &armForm{cl: cl}
		used := false
		if len(cl.List) == 0 {
			def, used = a, true
		}
		for _, item := range cl.List {
			p := in.fx(fr, item).pattern
			if p == nil {
				return nil
			}
			switch p.Kind {
			case check.PatWildcard:
				def, used = a, true
			case check.PatConst:
				c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
				if !ok {
					return nil // the walk names it
				}
				cases, used = append(cases, valueCase{item, c, a}), true
			default:
				return nil
			}
		}
		if used {
			a.body = in.compileScope(fr, cl, cl.Body, ret)
		}
	}
	tag := in.operand(fr, s.Tag, in.fx(fr, s.Tag))
	tagRel := in.releasesOf(fr, s.Tag)
	var at syntax.Node = s
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		v, ab := tag(in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		if tagRel != nil {
			if ab := tagRel(in, fr); ab != nil {
				return nil, ab
			}
		}
		for i := range cases {
			c := &cases[i]
			if ab := in.charge(fr, c.at, 1); ab != nil {
				return nil, ab
			}
			eq, ab := in.equal(fr, c.at, v, c.c)
			if ab != nil {
				return nil, ab
			}
			if eq {
				return c.arm.run(in, fr)
			}
		}
		if def == nil {
			return nil, fr.defect(s, "no case of the match took the value")
		}
		return def.run(in, fr)
	}
}
