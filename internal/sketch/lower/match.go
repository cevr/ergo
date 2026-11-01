package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Patterns (196p, 196t; check-v1 D-B3 and D-B4). A match whose arms a Go
// type switch or expression switch cannot express (record, nested, tuple,
// bool or binding patterns, several subjects) lowers to a Go expressionless
// switch: each arm's condition is its patterns' tests over the subjects,
// joined with || for several patterns, and && inside one pattern, so a
// type assertion runs only after its test passed. The subjects are
// evaluated once, left to right, into temporaries before any test. The
// checker proved the match total (Info contract 3), so the default arm Go
// needs panics with a named defect (noCase).
//
// Prior art: Gleam's JavaScript backend and ReScript compile a decision
// over the subject into if chains of tag tests and field reads; OCaml's
// matching compiler shares tests across rows (Maranget), which this
// lowering leaves to Go's compiler.

// patBind is a binding a pattern makes: Var takes the Go value val.
type patBind struct {
	v   *check.Var
	val string
}

// simpleTypeMatch reports a match a Go type switch lowers: one subject of
// an interface type, and arms of type tests, wildcards and a single `T as
// x`.
func (fc *funcCtx) simpleTypeMatch(s *syntax.TypeSwitchStmt) bool {
	if len(s.Subjects) != 1 || !fc.isInterface(fc.typeOf(s.Subjects[0].X)) {
		return false
	}
	for _, cl := range s.Clauses {
		for _, item := range cl.List {
			p := fc.l.info.Patterns[item]
			if p == nil {
				return false
			}
			switch p.Kind {
			case check.PatWildcard, check.PatTypeTest:
			case check.PatAs:
				if len(cl.List) != 1 || len(p.Elems) != 1 || p.Elems[0].Kind != check.PatTypeTest || p.Var == nil {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// patternMatch lowers any other match, its arms yielding to d.
func (fc *funcCtx) patternMatch(s syntax.Stmt, subjects []syntax.Expr, clauses []*syntax.CaseClause, d dest) bool {
	if fc.l.info.Matches[s] == nil {
		ice(s, "a match with no Matches entry")
	}
	vals := fc.operands(subjects, false)
	types := make([]check.Type, len(subjects))
	for i, x := range subjects {
		types[i] = fc.typeOf(x)
		if !fc.isConst(x) {
			t := fc.temp("s")
			fc.line(t + " := " + vals[i])
			fc.line("_ = " + t)
			vals[i] = t
		}
	}
	fc.line("switch {")
	fc.enterSwitch()
	terminates, hasDefault := true, false
	for _, cl := range clauses {
		var conds []string
		var binds [][]patBind
		always := len(cl.List) == 0
		for _, item := range cl.List {
			p := fc.l.info.Patterns[item]
			if p == nil {
				ice(item, "a case with no Patterns entry")
			}
			var tests []string
			var b []patBind
			if len(subjects) == 1 {
				fc.pattern(p, vals[0], types[0], item, &tests, &b)
			} else {
				if p.Kind != check.PatTuple || len(p.Elems) != len(subjects) {
					ice(item, "a case of a match on %d subjects is not a tuple of %d patterns", len(subjects), len(subjects))
				}
				for j, e := range p.Elems {
					fc.pattern(e, vals[j], types[j], item, &tests, &b)
				}
			}
			cond := strings.Join(tests, " && ")
			if cond == "" {
				always = true
				cond = "true"
			}
			conds = append(conds, cond)
			binds = append(binds, b)
		}
		if always {
			hasDefault = true
			fc.line("default:")
		} else {
			parts := make([]string, len(conds))
			for i, c := range conds {
				parts[i] = c
				if len(conds) > 1 {
					parts[i] = "(" + c + ")"
				}
			}
			fc.line("case " + strings.Join(parts, " || ") + ":")
		}
		fc.depth++
		fc.bindArm(conds, binds)
		terminates = fc.yieldList(cl.Body, d) && terminates
		fc.depth--
	}
	if !hasDefault && d.needsDefault(terminates) {
		hasDefault = true
		fc.line("default:")
		fc.noCase(s)
	}
	fc.leaveSwitch()
	fc.line("}")
	return terminates && hasDefault
}

// bindArm declares an arm's bindings. With several patterns each binds the
// same variables (D-B3 rule 6), from whichever pattern matched.
func (fc *funcCtx) bindArm(conds []string, binds [][]patBind) {
	if len(binds) == 0 || len(binds[0]) == 0 {
		return
	}
	if len(binds) == 1 {
		for _, b := range binds[0] {
			name := fc.declare(b.v)
			fc.line(name + " := " + b.val)
			if !fc.l.reads[b.v] {
				fc.line("_ = " + name)
			}
		}
		return
	}
	for _, b := range binds[0] {
		name := fc.declare(b.v)
		fc.line("var " + name + " " + fc.goType(b.v.Type))
		if !fc.l.reads[b.v] {
			fc.line("_ = " + name)
		}
	}
	for i, list := range binds {
		head := "} else if " + conds[i] + " {"
		switch {
		case i == 0:
			head = "if " + conds[i] + " {"
		case i == len(binds)-1:
			head = "} else {"
		}
		fc.line(head)
		for _, b := range list {
			fc.line("\t" + fc.locals[b.v] + " = " + b.val)
		}
	}
	fc.line("}")
}

// pattern appends the Go tests that val, of the checked type t, matches p,
// and the bindings p makes.
func (fc *funcCtx) pattern(p *check.Pattern, val string, t check.Type, site syntax.Node, tests *[]string, binds *[]patBind) {
	switch p.Kind {
	case check.PatWildcard:
	case check.PatBind:
		if p.Var != nil {
			*binds = append(*binds, patBind{p.Var, val})
		}
	case check.PatConst:
		*tests = append(*tests, val+" == "+fc.constant(p.Type, p.Value, site))
	case check.PatTypeTest:
		fc.typeTest(p.Type, val, t, tests)
	case check.PatRecord:
		inner := fc.typeTest(p.Type, val, t, tests)
		named, ok := p.Type.(*check.Named)
		if !ok {
			ice(site, "a record pattern of the type %s", p.Type)
		}
		st, ok := named.Underlying().(*check.Struct)
		if !ok {
			ice(site, "a record pattern of the non-record %s", p.Type)
		}
		td := fc.l.typeDecl(named.Obj, site)
		for _, f := range p.Fields {
			var ft check.Type
			for _, sf := range st.Fields {
				if sf.Name == f.Name {
					ft = sf.Type
				}
			}
			goName, ok := td.fieldName[f.Name]
			if ft == nil || !ok {
				ice(site, "%s has no field %s", p.Type, f.Name)
			}
			fc.pattern(f.Pattern, inner+"."+goName, ft, site, tests, binds)
		}
	case check.PatTuple:
		tup, ok := t.(*check.Tuple)
		if !ok {
			if n, isNamed := t.(*check.Named); isNamed {
				tup, ok = n.Underlying().(*check.Tuple)
			}
		}
		if !ok || len(tup.Types) != len(p.Elems) {
			ice(site, "a tuple pattern of %d elements against %s", len(p.Elems), t)
		}
		for i, e := range p.Elems {
			fc.pattern(e, val+"."+tupleField(i), tup.Types[i], site, tests, binds)
		}
	case check.PatAs:
		if len(p.Elems) != 1 {
			ice(site, "an as pattern without one inner pattern")
		}
		fc.pattern(p.Elems[0], val, t, site, tests, binds)
		if p.Var != nil {
			*binds = append(*binds, patBind{p.Var, fc.narrow(val, t, p.Var.Type)})
		}
	default:
		ice(site, "an unknown pattern kind")
	}
}

// typeTest appends the test that val, of type t, holds the type want, and
// returns val narrowed to want. A test of t itself always holds.
func (fc *funcCtx) typeTest(want check.Type, val string, t check.Type, tests *[]string) string {
	if !fc.isInterface(t) || fc.goType(want) == fc.goType(t) {
		return val
	}
	*tests = append(*tests, fc.l.isHelper()+"["+fc.goType(want)+"]("+val+")")
	return val + ".(" + fc.goType(want) + ")"
}

// narrow converts val of type t to to: a type assertion out of a union or
// operation type, which the tests before it proved.
func (fc *funcCtx) narrow(val string, t, to check.Type) string {
	if !fc.isInterface(t) || fc.isInterface(to) || fc.goType(to) == fc.goType(t) {
		return val
	}
	return val + ".(" + fc.goType(to) + ")"
}

// tupleField is the Go field name of a tuple's i-th element.
func tupleField(i int) string { return "F" + strconv.Itoa(i) }

// isHelper is the generic Go function that reports whether an interface
// value holds a type, emitted once.
func (l *lowerer) isHelper() string {
	name := l.pkgName(pkgKey{"is", nil}, "ergoIs")
	key := pkgKey{"is", nil}
	if !l.texts[key] {
		l.texts[key] = true
		l.gen.WriteString("func " + name + "[T any](x any) bool {\n\t_, ok := x.(T)\n\treturn ok\n}\n\n")
	}
	return name
}

// letPattern lowers a let that destructures: `let P = e`, whose pattern the
// checker proved irrefutable (D-B5), so it binds with no test; `let q, r =
// e` over a tuple (196t); and `let a, b = x, y`, evaluated left to right.
func (fc *funcCtx) letPattern(spec *syntax.VarSpec) {
	bindAll := func(binds []patBind) {
		for _, b := range binds {
			name := fc.declare(b.v)
			fc.line("var " + name + " " + fc.goType(b.v.Type) + " = " + b.val)
			if !fc.l.reads[b.v] {
				fc.line("_ = " + name)
			}
		}
	}
	switch {
	case spec.Pattern != nil && len(spec.Values) == 1:
		p := fc.l.info.Patterns[spec.Pattern]
		if p == nil {
			ice(spec.Pattern, "a let pattern with no Patterns entry")
		}
		x := spec.Values[0]
		t := fc.typeOf(x)
		if c, ok := fc.l.info.Implicit[x]; ok {
			t = c.To
		}
		v := fc.temp("p")
		fc.line(v + " := " + fc.assigned(x))
		fc.line("_ = " + v)
		var tests []string
		var binds []patBind
		fc.pattern(p, v, t, spec.Pattern, &tests, &binds)
		bindAll(binds)
	case spec.Pattern == nil && len(spec.Values) == 1 && len(spec.Names) > 1:
		x := spec.Values[0]
		tup, ok := fc.typeOf(x).(*check.Tuple)
		if !ok || len(tup.Types) != len(spec.Names) {
			refuse(spec, "this let of several names is lowered after check-v1 stage D (the Map rule)")
		}
		v := fc.temp("p")
		fc.line(v + " := " + fc.value(x))
		fc.line("_ = " + v)
		var binds []patBind
		for i, id := range spec.Names {
			if w, ok := fc.l.info.Defs[id].(*check.Var); ok {
				binds = append(binds, patBind{w, fc.elemAt(x, i, v+"."+tupleField(i))})
			}
		}
		bindAll(binds)
	case spec.Pattern == nil && len(spec.Values) == len(spec.Names):
		vals := fc.operands(spec.Values, true)
		var binds []patBind
		for i, id := range spec.Names {
			if w, ok := fc.l.info.Defs[id].(*check.Var); ok {
				binds = append(binds, patBind{w, vals[i]})
			} else if !fc.isConst(spec.Values[i]) {
				fc.line("_ = " + vals[i])
			}
		}
		bindAll(binds)
	default:
		ice(spec, "a let whose names and values do not pair")
	}
}
