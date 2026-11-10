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
		return in.rangeStmt(fr, s)
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
		if done, ab := loopBody(in.stmts(fr, s.Body.List)); done {
			return ab
		}
	}
}

// loopBody reads one iteration's completion: done when the loop ends
// (a break: no abrupt; any other abrupt but continue passes out).
func loopBody(ab *abrupt) (bool, *abrupt) {
	if ab == nil {
		return false, nil
	}
	switch ab.kind {
	case ctlBreak:
		return true, nil
	case ctlContinue:
		return false, nil
	}
	return true, ab
}

// rangeStmt runs a for-in over the domain the checker recorded (types.md,
// Loops and len 1, 7): a slice's initial header with live element writes,
// an array's initial value, a string's runes as Go decodes them, or a Range counting
// up from Start, End excluded. A Map's order is unspecified, so a for over
// one has no verdict the Go target is sure to share: a skip (plan section
// 4). A Seq calls a function value, which a pure frame never does.
func (in *interp) rangeStmt(fr *frame, s *syntax.RangeStmt) *abrupt {
	l := fr.info.Loops[s]
	if l == nil {
		ice("a for-in with no Loops entry")
	}
	if l.Domain == check.LoopMap {
		return fr.unsupported(s, "a for over a Map, whose order the Go target leaves unspecified")
	}
	if l.Domain == check.LoopSeq {
		return fr.unsupported(s, "a for over a Seq, which calls a function value")
	}
	var root *slot
	var dom Value
	var ab *abrupt
	if l.Domain == check.LoopSlice {
		dom, root, ab = in.peek(fr, s.X)
		if root != nil {
			root.lend()
		}
	} else {
		dom, ab = in.expr(fr, s.X)
	}
	if ab != nil {
		return ab
	}
	if l.Domain == check.LoopSlice && root != nil {
		if xs, ok := dom.(sliceV); ok && !xs.array {
			if _, direct := unparen(s.X).(*syntax.Ident); !direct {
				return fr.unsupported(s, "a slice range rooted through a field or subscript yet")
			}
		}
	}
	iter := func(pos, elem Value) (bool, *abrupt) {
		if root != nil && !basicValue(elem) {
			root.lend() // the iteration binding holds its own element value
		}
		if ab := in.charge(fr, s, 1); ab != nil {
			return true, ab
		}
		if ab := in.loopBind(fr, s.Key, pos); ab != nil {
			return true, ab
		}
		if ab := in.loopBind(fr, s.Value, elem); ab != nil {
			return true, ab
		}
		return loopBody(in.stmts(fr, s.Body.List))
	}
	switch l.Domain {
	case check.LoopSlice:
		xs := dom.(sliceV)
		if !xs.array {
			// A loop is not a value binding (Bindings 3). Follow writes
			// through the ranged root even when COW relocates its store.
			in.ranges = append(in.ranges, &sliceRange{root: root, view: &xs})
			defer func() { in.ranges = in.ranges[:len(in.ranges)-1] }()
		}
		for i := 0; i < xs.n; i++ {
			if done, ab := iter(int64(i), xs.elems()[i]); done {
				return ab
			}
		}
		return nil
	case check.LoopString:
		for i, r := range dom.(string) {
			if done, ab := iter(int64(i), r); done {
				return ab
			}
		}
		return nil
	case check.LoopRange:
		r := dom.(*record)
		start, end := r.fields[fieldIndex(r.t, "Start")], r.fields[fieldIndex(r.t, "End")]
		one, ok := convertBasic(int64(1), fr.rt(l.Elem).basic().Kind)
		if !ok {
			ice("a Range over %s", l.Elem)
		}
		for i := start; ; {
			less, _, _ := arith(syntax.LSS, i, end)
			if !less.(bool) {
				return nil
			}
			if done, ab := iter(nil, i); done {
				return ab
			}
			i, _, _ = arith(syntax.ADD, i, one)
		}
	}
	ice("a for-in over the domain %d", l.Domain)
	return nil
}

// loopBind binds a for binding: a name, or an irrefutable pattern the
// checker proved (Info.Patterns). No binding, or `_`, binds nothing.
func (in *interp) loopBind(fr *frame, p syntax.Expr, v Value) *abrupt {
	if p == nil {
		return nil
	}
	if id, ok := p.(*syntax.Ident); ok {
		if id.Name == "_" {
			return nil
		}
		if w, ok := fr.info.Defs[id].(*check.Var); ok {
			fr.bind(w, v)
			return nil
		}
	}
	pat := fr.info.Patterns[p]
	if pat == nil {
		ice("a for binding with neither a Defs nor a Patterns entry")
	}
	ok, ab := in.match(fr, p, pat, v)
	if ab != nil {
		return ab
	}
	if !ok {
		return fr.defect(p, "the for pattern does not match")
	}
	return nil
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
		ok, ab := in.match(fr, spec.Pattern, p, v)
		if ab != nil {
			return ab
		}
		if !ok {
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
				fr.bind(w, tup.elems[i])
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
				fr.bind(w, vals[i])
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
		z, ab := in.zero(fr, spec, fr.rt(v.Type))
		if ab != nil {
			return ab
		}
		fr.bind(v, z)
		return nil
	}
	val, ab := in.expr(fr, spec.Values[0])
	if ab != nil {
		return ab
	}
	fr.bind(v, val)
	return nil
}

// assignOps maps an op-assignment to its operator.
var assignOps = map[syntax.Token]syntax.Token{
	syntax.ADD_ASSIGN: syntax.ADD, syntax.SUB_ASSIGN: syntax.SUB, syntax.MUL_ASSIGN: syntax.MUL,
	syntax.QUO_ASSIGN: syntax.QUO, syntax.REM_ASSIGN: syntax.REM, syntax.AND_ASSIGN: syntax.AND,
	syntax.OR_ASSIGN: syntax.OR, syntax.XOR_ASSIGN: syntax.XOR, syntax.SHL_ASSIGN: syntax.SHL,
	syntax.SHR_ASSIGN: syntax.SHR, syntax.AND_NOT_ASSIGN: syntax.AND_NOT,
}

// step is one step of a written place's path: a field, or an index (an
// element of a slice or an array, a key of a Map).
type step struct {
	at    syntax.Expr
	field string
	index Value
}

// assign runs `x = e` and `x op= e` on a place: a local, or a path of
// fields and indexes rooted at one. As Go's two-phase assignment does, the
// place's index operands are evaluated, left to right, before the value.
func (in *interp) assign(fr *frame, s *syntax.AssignStmt) *abrupt {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		ice("an assignment of several values")
	}
	lhs := unparen(s.Lhs[0])
	if id, ok := lhs.(*syntax.Ident); ok && id.Name == "_" {
		_, ab := in.expr(fr, s.Rhs[0])
		return ab
	}
	root, steps, why, ab := in.place(fr, lhs)
	if ab != nil {
		return ab
	}
	if why != "" {
		return fr.unsupported(lhs, "an assignment to %s yet", why)
	}
	for _, r := range in.ranges {
		if r.root == root && len(steps) == 0 {
			return fr.unsupported(lhs, "replacement of an active slice range's root yet")
		}
	}
	var old Value
	if s.Tok != syntax.ASSIGN {
		v, ab := in.read(fr, root.v, steps)
		if ab != nil {
			return ab
		}
		old = v
	}
	var val Value
	if call, own := in.handover(fr, root, steps, s); call != nil {
		val, ab = in.collectionCall(fr, call, fr.info.Calls[call], own)
	} else {
		val, ab = in.expr(fr, s.Rhs[0])
	}
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
	nv, ab := in.storePath(fr, root, root.v, steps, val)
	if ab != nil {
		return ab
	}
	root.v = nv
	return nil
}

// handover is the prelude call of x = x.Append(e), x = x.Put(k, v) or
// x = x.Delete(k) whose receiver is the local x itself, and x's slot: the
// assignment replaces x, so the call may take x's own store.
func (in *interp) handover(fr *frame, root *slot, steps []step, s *syntax.AssignStmt) (*syntax.CallExpr, *slot) {
	if s.Tok != syntax.ASSIGN || len(steps) != 0 {
		return nil, nil
	}
	call, ok := unparen(s.Rhs[0]).(*syntax.CallExpr)
	if !ok {
		return nil, nil
	}
	c := fr.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeCollection(c.Func) || c.Recv == nil || c.Func.Name == "Get" {
		return nil, nil
	}
	id, ok := unparen(c.Recv).(*syntax.Ident)
	if !ok {
		return nil, nil
	}
	v, ok := fr.info.Uses[id].(*check.Var)
	if !ok || fr.vars[v] != root {
		return nil, nil
	}
	if _, conv := fr.info.Implicit[c.Recv]; conv {
		return nil, nil
	}
	return call, root
}

// place resolves an assigned place: its root local's slot and the path
// from it, each index evaluated. why is what the place is when the
// evaluator does not write it.
func (in *interp) place(fr *frame, x syntax.Expr) (*slot, []step, string, *abrupt) {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := fr.info.Uses[x].(*check.Var)
		if !ok {
			ice("an assignment to %s, which is no variable", x.Name)
		}
		s, local := fr.vars[v]
		if !local {
			return nil, nil, "a package let", nil
		}
		return s, nil, "", nil
	case *syntax.SelectorExpr:
		if _, ok := fr.typeOf(x.X).(*check.Pointer); ok {
			return nil, nil, "a field through a pointer", nil
		}
		root, steps, why, ab := in.place(fr, x.X)
		if ab != nil || why != "" {
			return nil, nil, why, ab
		}
		return root, append(steps, step{at: x, field: x.Sel.Name}), "", nil
	case *syntax.IndexExpr:
		if _, ok := fr.typeOf(x.X).(*check.Pointer); ok {
			return nil, nil, "an element through a pointer", nil
		}
		root, steps, why, ab := in.place(fr, x.X)
		if ab != nil || why != "" {
			return nil, nil, why, ab
		}
		iv, ab := in.expr(fr, x.Indices[0])
		if ab != nil {
			return nil, nil, "", ab
		}
		if !basicValue(iv) {
			return nil, nil, "an entry of a Map whose key holds parts", nil
		}
		return root, append(steps, step{at: x, index: iv}), "", nil
	case *syntax.StarExpr:
		return nil, nil, "a pointer's target", nil
	}
	return nil, nil, "this place", nil
}

// read is the value at the path from v, for an op-assignment.
func (in *interp) read(fr *frame, v Value, steps []step) (Value, *abrupt) {
	for _, st := range steps {
		if ab := in.charge(fr, st.at, 1); ab != nil {
			return nil, ab
		}
		switch c := v.(type) {
		case *record:
			v = c.fields[fieldIndex(c.t, st.field)]
		case sliceV:
			i, ok := in.index(fr, st.at, st.index, c.n)
			if !ok {
				return nil, fr.defect(st.at, "index out of range [%s] with length %d", describe(st.index), c.n)
			}
			v = c.b.data[c.off+i]
		case mapV:
			ice("an op-assignment reads a Map entry")
		default:
			ice("a path through %T", v)
		}
	}
	return v, nil
}

// storePath is cur with val written at the path: a new record at each
// field, and each slice, array or Map on the path owned by tok, copied
// once when another value may share it.
func (in *interp) storePath(fr *frame, root *slot, cur Value, steps []step, val Value) (Value, *abrupt) {
	tok := root.own()
	if len(steps) == 0 {
		return val, nil
	}
	st := steps[0]
	switch c := cur.(type) {
	case *record:
		i := fieldIndex(c.t, st.field)
		if ab := in.charge(fr, st.at, int64(len(c.fields))); ab != nil {
			return nil, ab
		}
		inner, ab := in.storePath(fr, root, c.fields[i], steps[1:], val)
		if ab != nil {
			return nil, ab
		}
		next := &record{t: c.t, key: c.key, fields: append([]Value(nil), c.fields...)}
		next.fields[i] = inner
		return next, nil
	case sliceV:
		i, ok := in.index(fr, st.at, st.index, c.n)
		if !ok {
			return nil, fr.defect(st.at, "index out of range [%s] with length %d", describe(st.index), c.n)
		}
		old := c.b
		oldOff := c.off
		c, ab := in.ownSlice(fr, st.at, c, tok)
		if ab != nil {
			return nil, ab
		}
		for _, r := range in.ranges {
			if r.root == root && r.view.b == old {
				r.view.b, r.view.off = c.b, r.view.off-oldOff
			}
		}
		inner, ab := in.storePath(fr, root, c.b.data[c.off+i], steps[1:], val)
		if ab != nil {
			return nil, ab
		}
		c.b.data[c.off+i] = inner
		return c, nil
	case mapV:
		if len(steps) != 1 {
			ice("a write through a Map entry")
		}
		return in.putIn(fr, st.at, c, st.index, val, tok)
	}
	ice("a store into %T", cur)
	return nil, nil
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
				c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
				if !ok {
					return nil, fr.unsupported(item, "a case constant of %s yet", p.Type)
				}
				if ab := in.charge(fr, item, 1); ab != nil {
					return nil, ab
				}
				eq, ab := in.equal(fr, item, tag, c)
				if ab != nil {
					return nil, ab
				}
				if eq {
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
			ok, ab := in.match(fr, item, p, subject)
			if ab != nil {
				return nil, ab
			}
			if ok {
				return in.yield(fr, cl.Body, ret)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return in.yield(fr, def.Body, ret)
}

// match tests v against the checked pattern p, read in the frame's
// substitution, and binds its names.
func (in *interp) match(fr *frame, at syntax.Node, p *check.Pattern, v Value) (bool, *abrupt) {
	switch p.Kind {
	case check.PatWildcard:
		return true, nil
	case check.PatBind:
		fr.bind(p.Var, v)
		return true, nil
	case check.PatTypeTest:
		return hasType(v, fr.rt(p.Type)), nil
	case check.PatRecord:
		if !hasType(v, fr.rt(p.Type)) {
			return false, nil
		}
		r := v.(*record)
		for _, f := range p.Fields {
			ok, ab := in.match(fr, at, f.Pattern, r.fields[fieldIndex(r.t, f.Name)])
			if !ok || ab != nil {
				return false, ab
			}
		}
		return true, nil
	case check.PatConst:
		c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
		if !ok {
			ice("a constant pattern of %s", p.Type)
		}
		return in.equal(fr, at, v, c)
	case check.PatTuple:
		t, ok := v.(tuple)
		if !ok || len(t.elems) != len(p.Elems) {
			ice("a tuple pattern over %T", v)
		}
		for i, e := range p.Elems {
			ok, ab := in.match(fr, at, e, t.elems[i])
			if !ok || ab != nil {
				return false, ab
			}
		}
		return true, nil
	case check.PatAs:
		if len(p.Elems) != 1 {
			return false, nil
		}
		ok, ab := in.match(fr, at, p.Elems[0], v)
		if !ok || ab != nil {
			return false, ab
		}
		fr.bind(p.Var, v)
		return true, nil
	}
	ice("a pattern of kind %d", p.Kind)
	return false, nil
}

// hasType reports whether v's dynamic type is t: a record of t, or of a
// variant of the union t. A value that is no record has its static type.
func hasType(v Value, t rtype) bool {
	r, ok := v.(*record)
	if !ok {
		return true
	}
	t = t.env.resolve(t.t)
	switch tt := t.t.(type) {
	case *check.Union:
		for _, variant := range tt.Variants {
			if hasType(v, rtype{variant, t.env}) {
				return true
			}
		}
		return false
	case *check.Named:
		if u, isUnion := tt.Underlying().(*check.Union); isUnion {
			return hasType(v, rtype{u, t.env})
		}
		return r.t.Obj == tt.Obj && r.key == argsKey(tt, t.env)
	}
	return false
}

// equal is a == b, its work charged before it is done.
func (in *interp) equal(fr *frame, at syntax.Node, a, b Value) (bool, *abrupt) {
	var ab *abrupt
	eq, paid := equal(a, b, func(n int64) bool {
		ab = in.charge(fr, at, n)
		return ab == nil
	})
	if !paid {
		return false, ab
	}
	return eq, nil
}
