package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (in *interp) stmts(fr *frame, list []syntax.Stmt) *abrupt {
	for _, s := range list {
		if ab := in.stmt(fr, s); ab != nil {
			return ab
		}
	}
	return nil
}

// stmt runs one statement for its effect on the frame.
func (in *interp) stmt(fr *frame, s syntax.Stmt) *abrupt {
	switch s.(type) {
	case *syntax.ExprStmt, *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.BlockStmt:
		_, ab := in.yieldStmt(fr, s, false)
		return ab
	}
	if ab := in.charge(fr, s, 1); ab != nil {
		return ab
	}
	return in.stmtOnly(fr, s)
}

// stmtOnly runs a statement that yields no value.
func (in *interp) stmtOnly(fr *frame, s syntax.Stmt) *abrupt {
	switch s := s.(type) {
	case *syntax.DeclStmt:
		return in.let(fr, s.Decl)
	case *syntax.AssignStmt:
		return in.assign(fr, s)
	case *syntax.ReturnStmt:
		if len(s.Results) == 0 {
			return fr.abrupt(ctlReturn, s, nil, "")
		}
		if len(s.Results) > 1 {
			ice("a return of %d values", len(s.Results))
		}
		v, ab := in.retValue(fr, s.Results[0])
		if ab != nil {
			return ab
		}
		return fr.abrupt(ctlReturn, s, v, "")
	case *syntax.BranchStmt:
		if s.Tok == syntax.CONTINUE {
			return fr.abrupt(ctlContinue, s, nil, "")
		}
		return fr.abrupt(ctlBreak, s, nil, "")
	case *syntax.ForStmt:
		return in.forStmt(fr, s)
	case *syntax.RangeStmt:
		return fr.unsupported(s, "a for-in loop yet")
	case *syntax.AssertStmt:
		v, ab := in.expr(fr, s.X)
		if ab != nil {
			return ab
		}
		if !truth(v) {
			return fr.abrupt(ctlAssert, s, nil, "")
		}
		return nil
	}
	ice("a statement of the kind %T", s)
	return nil
}

// forStmt runs `for { }` and `for cond { }`. Every iteration costs steps
// through its body and condition, so a loop that never ends exhausts the
// budget.
func (in *interp) forStmt(fr *frame, s *syntax.ForStmt) *abrupt {
	for {
		if ab := in.charge(fr, s, 1); ab != nil {
			return ab
		}
		if s.Cond != nil {
			c, ab := in.expr(fr, s.Cond)
			if ab != nil {
				return ab
			}
			if !truth(c) {
				return nil
			}
		}
		ab := in.stmts(fr, s.Body.List)
		if ab == nil {
			continue
		}
		switch ab.kind {
		case ctlBreak:
			return nil
		case ctlContinue:
			continue
		}
		return ab
	}
}

// let binds a local let.
func (in *interp) let(fr *frame, d *syntax.VarDecl) *abrupt {
	for _, spec := range d.Specs {
		if ab := in.letSpec(fr, spec); ab != nil {
			return ab
		}
	}
	return nil
}

func (in *interp) letSpec(fr *frame, spec *syntax.VarSpec) *abrupt {
	switch {
	case spec.Pattern != nil:
		if len(spec.Values) != 1 {
			ice("a let pattern with %d values", len(spec.Values))
		}
		p := fr.info.Patterns[spec.Pattern]
		if p == nil {
			ice("a let pattern with no Patterns entry")
		}
		v, ab := in.expr(fr, spec.Values[0])
		if ab != nil {
			return ab
		}
		if !in.match(fr, p, v) {
			return fr.defect(spec.Pattern, "the let pattern does not match")
		}
		return nil
	case len(spec.Names) > 1 && len(spec.Values) == 1:
		v, ab := in.expr(fr, spec.Values[0])
		if ab != nil {
			return ab
		}
		tup, ok := v.(tuple)
		if !ok || len(tup.elems) != len(spec.Names) {
			ice("a let of %d names over %T", len(spec.Names), v)
		}
		for i, id := range spec.Names {
			if w, ok := fr.info.Defs[id].(*check.Var); ok {
				fr.vars[w] = tup.elems[i]
			}
		}
		return nil
	case len(spec.Names) != 1 || len(spec.Values) > 1:
		if len(spec.Values) != len(spec.Names) {
			ice("a let of %d names and %d values", len(spec.Names), len(spec.Values))
		}
		vals := make([]Value, len(spec.Values))
		for i, x := range spec.Values {
			v, ab := in.expr(fr, x)
			if ab != nil {
				return ab
			}
			vals[i] = v
		}
		for i, id := range spec.Names {
			if w, ok := fr.info.Defs[id].(*check.Var); ok {
				fr.vars[w] = vals[i]
			}
		}
		return nil
	}
	id := spec.Names[0]
	if p := fr.info.Patterns[id]; p != nil && p.Kind != check.PatBind {
		// A known single name tests a type or constant the checker proved
		// irrefutable; the value runs for its effect.
		_, ab := in.expr(fr, spec.Values[0])
		return ab
	}
	if c, ok := fr.info.Defs[id].(*check.Context); ok && c.Local {
		return fr.unsupported(spec, "a local context, which binds capabilities")
	}
	if id.Name == "_" {
		if len(spec.Values) == 1 {
			_, ab := in.expr(fr, spec.Values[0])
			return ab
		}
		return nil
	}
	v, ok := fr.info.Defs[id].(*check.Var)
	if !ok {
		ice("the let %s has no Defs entry", id.Name)
	}
	if len(spec.Values) == 0 {
		z, ok := zero(v.Type)
		if !ok {
			return fr.unsupported(spec, "the default value of %s yet", v.Type)
		}
		fr.vars[v] = z
		return nil
	}
	val, ab := in.expr(fr, spec.Values[0])
	if ab != nil {
		return ab
	}
	fr.vars[v] = val
	return nil
}

// assignOps maps an op-assignment to its operator.
var assignOps = map[syntax.Token]syntax.Token{
	syntax.ADD_ASSIGN: syntax.ADD, syntax.SUB_ASSIGN: syntax.SUB, syntax.MUL_ASSIGN: syntax.MUL,
	syntax.QUO_ASSIGN: syntax.QUO, syntax.REM_ASSIGN: syntax.REM, syntax.AND_ASSIGN: syntax.AND,
	syntax.OR_ASSIGN: syntax.OR, syntax.XOR_ASSIGN: syntax.XOR, syntax.SHL_ASSIGN: syntax.SHL,
	syntax.SHR_ASSIGN: syntax.SHR, syntax.AND_NOT_ASSIGN: syntax.AND_NOT,
}

// assign runs `x = e` and `x op= e` on a place: a local, or a field path
// of one. The place holds no operand that runs, so the order of Go's
// two-phase assignment has nothing to observe.
func (in *interp) assign(fr *frame, s *syntax.AssignStmt) *abrupt {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		ice("an assignment of several values")
	}
	lhs := unparen(s.Lhs[0])
	if id, ok := lhs.(*syntax.Ident); ok && id.Name == "_" {
		_, ab := in.expr(fr, s.Rhs[0])
		return ab
	}
	if why := in.placeForm(fr, lhs); why != "" {
		return fr.unsupported(lhs, "an assignment to %s yet", why)
	}
	var old Value
	if s.Tok != syntax.ASSIGN {
		v, ab := in.expr(fr, lhs)
		if ab != nil {
			return ab
		}
		old = v
	}
	val, ab := in.expr(fr, s.Rhs[0])
	if ab != nil {
		return ab
	}
	if s.Tok != syntax.ASSIGN {
		op, ok := assignOps[s.Tok]
		if !ok {
			ice("the assignment %s", s.Tok)
		}
		val, ab = in.binop(fr, s, op, old, val)
		if ab != nil {
			return ab
		}
	}
	return in.store(fr, lhs, val)
}

// placeForm is "" for a place the evaluator writes, else what it is.
func (in *interp) placeForm(fr *frame, x syntax.Expr) string {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := fr.info.Uses[x].(*check.Var)
		if !ok {
			ice("an assignment to %s, which is no variable", x.Name)
		}
		if _, local := fr.vars[v]; !local {
			return "a package let"
		}
		return ""
	case *syntax.SelectorExpr:
		if _, ok := fr.typeOf(x.X).(*check.Pointer); ok {
			return "a field through a pointer"
		}
		return in.placeForm(fr, x.X)
	case *syntax.IndexExpr:
		return "an element"
	case *syntax.StarExpr:
		return "a pointer's target"
	}
	return "this place"
}

// store writes v into the place x: a new record at each field of the path.
func (in *interp) store(fr *frame, x syntax.Expr, v Value) *abrupt {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		fr.vars[fr.info.Uses[x].(*check.Var)] = v
		return nil
	case *syntax.SelectorExpr:
		base, ab := in.expr(fr, x.X)
		if ab != nil {
			return ab
		}
		r, ok := base.(*record)
		if !ok {
			ice("a field store into %T", base)
		}
		i := fieldIndex(r.t, x.Sel.Name)
		if ab := in.charge(fr, x, int64(len(r.fields))); ab != nil {
			return ab
		}
		next := &record{t: r.t, fields: append([]Value(nil), r.fields...)}
		next.fields[i] = v
		return in.store(fr, x.X, next)
	}
	ice("a store into %T", x)
	return nil
}

// fieldIndex is the index of the field name of the record type t.
func fieldIndex(t *check.Named, name string) int {
	s := structOf(t)
	if s == nil {
		ice("%s is no record", t)
	}
	for i, f := range s.Fields {
		if f.Name == name {
			return i
		}
	}
	ice("%s has no field %s", t, name)
	return -1
}

// valueMatch runs a match on a literal subject: the first case whose
// constant equals the subject, else its `_` or default case (Go's switch;
// lower/body.go valueMatch).
func (in *interp) valueMatch(fr *frame, s *syntax.SwitchStmt, ret bool) (Value, *abrupt) {
	if fr.info.Matches[s] == nil {
		ice("a value match with no Matches entry")
	}
	tag, ab := in.expr(fr, s.Tag)
	if ab != nil {
		return nil, ab
	}
	var def *syntax.CaseClause
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			def = cl
		}
		for _, item := range cl.List {
			p := fr.info.Patterns[item]
			if p == nil {
				ice("a value case with no Patterns entry")
			}
			switch p.Kind {
			case check.PatWildcard:
				def = cl
			case check.PatConst:
				c, ok := fromConstant(p.Type, p.Value)
				if !ok {
					return nil, fr.unsupported(item, "a case constant of %s yet", p.Type)
				}
				if ab := in.charge(fr, item, 1); ab != nil {
					return nil, ab
				}
				if in.equal(tag, c) {
					return in.yield(fr, cl.Body, ret)
				}
			default:
				ice("a value case of pattern kind %d", p.Kind)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return in.yield(fr, def.Body, ret)
}

// typeMatch runs any other match: the first clause one of whose patterns
// matches the subjects, its bindings bound; a default clause last.
func (in *interp) typeMatch(fr *frame, s *syntax.TypeSwitchStmt, ret bool) (Value, *abrupt) {
	if fr.info.Matches[s] == nil {
		ice("a match with no Matches entry")
	}
	subjects := make([]Value, len(s.Subjects))
	for i, g := range s.Subjects {
		v, ab := in.expr(fr, g.X)
		if ab != nil {
			return nil, ab
		}
		subjects[i] = v
	}
	var subject Value = tuple{elems: subjects}
	if len(subjects) == 1 {
		subject = subjects[0]
	}
	var def *syntax.CaseClause
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			def = cl
			continue
		}
		for _, item := range cl.List {
			p := fr.info.Patterns[item]
			if p == nil {
				ice("a case with no Patterns entry")
			}
			if ab := in.charge(fr, item, 1); ab != nil {
				return nil, ab
			}
			if in.match(fr, p, subject) {
				return in.yield(fr, cl.Body, ret)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return in.yield(fr, def.Body, ret)
}

// match tests v against the checked pattern p and binds its names.
func (in *interp) match(fr *frame, p *check.Pattern, v Value) bool {
	switch p.Kind {
	case check.PatWildcard:
		return true
	case check.PatBind:
		if p.Var != nil {
			fr.vars[p.Var] = v
		}
		return true
	case check.PatTypeTest:
		return hasType(v, p.Type)
	case check.PatRecord:
		if !hasType(v, p.Type) {
			return false
		}
		r := v.(*record)
		for _, f := range p.Fields {
			if !in.match(fr, f.Pattern, r.fields[fieldIndex(r.t, f.Name)]) {
				return false
			}
		}
		return true
	case check.PatConst:
		c, ok := fromConstant(p.Type, p.Value)
		if !ok {
			ice("a constant pattern of %s", p.Type)
		}
		return in.equal(v, c)
	case check.PatTuple:
		t, ok := v.(tuple)
		if !ok || len(t.elems) != len(p.Elems) {
			ice("a tuple pattern over %T", v)
		}
		for i, e := range p.Elems {
			if !in.match(fr, e, t.elems[i]) {
				return false
			}
		}
		return true
	case check.PatAs:
		if len(p.Elems) != 1 || !in.match(fr, p.Elems[0], v) {
			return false
		}
		if p.Var != nil {
			fr.vars[p.Var] = v
		}
		return true
	}
	ice("a pattern of kind %d", p.Kind)
	return false
}

// hasType reports whether v's dynamic type is t: a record of t, or of a
// variant of the union t. A value that is no record has its static type.
func hasType(v Value, t check.Type) bool {
	r, ok := v.(*record)
	if !ok {
		return true
	}
	switch tt := t.(type) {
	case *check.Union:
		for _, variant := range tt.Variants {
			if hasType(v, variant) {
				return true
			}
		}
		return false
	case *check.Named:
		if u, isUnion := tt.Underlying().(*check.Union); isUnion {
			return hasType(v, u)
		}
		return sameNamed(r.t, tt)
	}
	return false
}

func (in *interp) equal(a, b Value) bool {
	return equal(a, b, func(n int64) { in.steps += n })
}
