package check

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The pattern language and match (196p rules 1 to 4; #196 fourth, sixth,
// seventh and ninth rounds; spec D-B3 and D-B4).
//
//  1. A bare name that resolves to a type is a type test; `_` is the
//     wildcard; true and false are constants; any other bare name binds a
//     new local. A bare name that resolves to a package-level let, a
//     function, a built-in or a package is S108.
//  2. A record pattern T{F: p, …} is keyed; a one-field record may omit the
//     key (Some{v}); omitted fields are ignored; an unknown field is S104;
//     a record pattern of a union variant tests the variant and matches its
//     fields; T{} with no field is S108 "write case T:".
//  3. A constant pattern is an integer, string, rune or bool literal (a
//     number may take `-`), typed by its subject's type; a float or
//     duration pattern is S108; a constant of another type is S108.
//  4. A tuple pattern has its subject tuple's arity (S108).
//  5. `P as x` binds the matched value with P's type, in its arm.
//  6. A case with several patterns binds only names that every pattern
//     binds, each with one type (S108).
//  7. Every binding is read (S110), `_` excepted.
//
// A match names every constructor or has a covering arm (S103 at the
// match, naming one missing pattern); a case that earlier cases cover is
// S108; a match on an operation-type value admits only `_` and bindings.
//
// Prior art: Rust resolves a path pattern when the name resolves and binds
// otherwise, and requires or-pattern bindings on every side (E0408);
// Elixir's pin `^x` exists because a bare name always binds; Gleam's
// patterns are keyed by label and `as` binds; OCaml's or-patterns bind
// one set; MoonBit matches records by field name; Go has no patterns.

// domain is what a column's values are, for its constructors.
type domain int

const (
	domOpaque domain = iota // only wildcards and bindings
	domUnion                // a union's variants
	domBool                 // false and true
	domConst                // integer, rune or string constants: never complete
	domTuple                // a tuple's one shape
	domRecord               // a record's one shape
)

// colInfo is what the resolver knows of a column: its type and domain.
type colInfo struct {
	t   Type
	dom domain
	u   *switchUnion
}

// patBinding is one name a pattern binds, or the placeholder of a refused
// type test (declarePlaceholder).
type patBinding struct {
	id   *syntax.Ident
	t    Type
	info *Pattern // its PatBind or PatAs node; Var is set when it is declared
	as   bool
	// placeholder: id is a refused type test, not a binder. It binds no
	// name in Info and meets no binder rule.
	placeholder bool
}

// resolved is one pattern resolved against its subject type.
type resolved struct {
	pat  int      // the solver pattern, when has
	has  bool     // false: a wildcard at its column
	t    Type     // the matched value's type, narrowed by a type test
	info *Pattern // the Info.Patterns node
}

// patResolver resolves the patterns of one match, let or for against their
// subject types, into the solver's columns and patterns. Resolving a type
// test is charged to resolve (variantindex.go), against the solvers'
// budget.
type patResolver struct {
	c        *checker
	sp       *patSpace
	info     []colInfo
	resolve  *lookupSteps
	binds    []patBinding    // of the current item
	bound    map[string]bool // the names binds holds
	bad      bool            // the current item has a reported error
	bindings bool            // let/for only: D-D2 permits binding over package objects; never a match's or a catch's
	// letFor: a let or for pattern, where a bare prelude name is a binder
	// D-D3 refuses, never a type test or a constant.
	letFor bool
	// own holds the binding names of a package-level let's pattern, which
	// the package scope already declares: they bind, not refer.
	own map[*syntax.Ident]bool
}

func (c *checker) newPatResolver() *patResolver {
	return &patResolver{c: c, sp: newPatSpace(), resolve: &lookupSteps{ops: &c.variantOps}}
}

// begin starts a pattern item: no binding, no error.
func (r *patResolver) begin() {
	r.binds = nil
	r.bound = nil
	r.bad = false
}

// subject adds a subject column of type t.
func (r *patResolver) subject(t Type) int {
	col, info := r.columnOf(t)
	r.info = append(r.info, info)
	return r.sp.addColumn(col)
}

// child returns the column of field (or element) field of constructor ctor
// at parent, whose type is t.
func (r *patResolver) child(parent, ctor, field int, t Type) int {
	return r.sp.childColumn(parent, ctor, field, func() column {
		col, info := r.columnOf(t)
		r.info = append(r.info, info)
		return col
	})
}

// columnOf is the column of a value of type t, with its witness printer.
func (r *patResolver) columnOf(t Type) (column, colInfo) {
	if u := r.c.subjectUnion(t); u != nil {
		col := column{size: u.len()}
		col.label = func(w witness, j int, out *strings.Builder) {
			ctor, ok := w[j]
			if !ok {
				if r.sp.cols[j].parent >= 0 || u.len() == 0 {
					out.WriteByte('_')
					return
				}
				ctor = 0 // a subject: name its first variant
			}
			r.recordText(w, j, ctor, u.at(ctor), out)
		}
		return col, colInfo{t: t, dom: domUnion, u: u}
	}
	switch ut := under(t).(type) {
	case *Basic:
		switch {
		case isBoolean(ut):
			col := column{size: 2}
			col.label = func(w witness, j int, out *strings.Builder) {
				ctor, ok := w[j]
				switch {
				case !ok:
					out.WriteByte('_')
				case ctor == 1:
					out.WriteString("true")
				default:
					out.WriteString("false")
				}
			}
			return col, colInfo{t: t, dom: domBool}
		case isInteger(ut), isString(ut):
			return column{size: -1}, colInfo{t: t, dom: domConst}
		}
	case *Tuple:
		n := len(ut.Types)
		col := column{size: 1}
		col.label = func(w witness, j int, out *strings.Builder) {
			if _, ok := w[j]; !ok {
				out.WriteByte('_')
				return
			}
			// A tuple pattern names every element in order, so its child
			// columns already have field order. Write into one builder;
			// nested witnesses never copy their full text at each parent.
			kids := r.sp.cols[j].children[0]
			out.WriteByte('(')
			for i := range n {
				if i > 0 {
					out.WriteString(", ")
				}
				if i < len(kids) {
					r.sp.writeWitness(w, kids[i], out)
				} else {
					out.WriteByte('_')
				}
			}
			out.WriteByte(')')
		}
		return col, colInfo{t: t, dom: domTuple}
	case *Struct:
		col := column{size: 1}
		col.label = func(w witness, j int, out *strings.Builder) {
			if _, ok := w[j]; !ok {
				out.WriteByte('_')
				return
			}
			r.recordText(w, j, 0, t, out)
		}
		return col, colInfo{t: t, dom: domRecord}
	}
	return column{size: -1}, colInfo{t: t, dom: domOpaque}
}

// recordText prints a record constructor of a witness: its name, with the
// fields the witness constrains.
func (r *patResolver) recordText(w witness, j, ctor int, t Type, out *strings.Builder) {
	s, _ := under(t).(*Struct)
	var shown []int
	for _, k := range r.sp.cols[j].children[ctor] {
		if _, ok := w[k]; ok && s != nil {
			shown = append(shown, k)
		}
	}
	sort.Slice(shown, func(a, b int) bool { return r.sp.cols[shown[a]].field < r.sp.cols[shown[b]].field })
	out.WriteString(t.String())
	if len(shown) == 0 {
		return
	}
	out.WriteByte('{')
	for i, k := range shown {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(s.Fields[r.sp.cols[k].field].Name)
		out.WriteString(": ")
		r.sp.writeWitness(w, k, out)
	}
	out.WriteByte('}')
}

// fail notes a reported error in the current item.
func (r *patResolver) fail(e syntax.Expr, t Type) resolved {
	r.bad = true
	r.bindNames(e)
	return resolved{t: invalid}
}

// bindNames binds, with the invalid type, the names an unresolved pattern
// would bind, so its arm reads them silently (D3).
func (r *patResolver) bindNames(e syntax.Expr) {
	if e == nil {
		return
	}
	tests := func(name string) bool { return r.c.patternTests(name, r.letFor) }
	for _, id := range patternNames(e, tests) {
		r.bind(id, invalid, &Pattern{Kind: PatBind}, false)
	}
}

// placeholder records a refused type test's name for declarePlaceholder.
func (r *patResolver) placeholder(id *syntax.Ident) {
	r.binds = append(r.binds, patBinding{id: id, t: invalid, placeholder: true})
}

func (r *patResolver) bind(id *syntax.Ident, t Type, info *Pattern, as bool) {
	if r.bound[id.Name] {
		r.c.errorf(id, CodeType, "%s is bound twice in this pattern (rows.md, Totality 1)", id.Name)
		r.bad = true
		return
	}
	if r.bound == nil {
		r.bound = map[string]bool{}
	}
	r.bound[id.Name] = true
	r.binds = append(r.binds, patBinding{id: id, t: t, info: info, as: as})
}

// pattern resolves e against a value of type t at column col and records
// its Info.Patterns node.
func (r *patResolver) pattern(e syntax.Expr, t Type, col int) resolved {
	res := r.pattern0(e, t, col)
	if res.info != nil {
		r.c.pattern(e, res.info)
	}
	return res
}

func (r *patResolver) pattern0(e syntax.Expr, t Type, col int) resolved {
	c := r.c
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return r.pattern(x.X, t, col)
	case *syntax.Ident:
		if x.Name == "_" {
			return resolved{t: t, info: &Pattern{Kind: PatWildcard}}
		}
		if r.own[x] {
			info := &Pattern{Kind: PatBind}
			r.bind(x, t, info, false)
			return resolved{t: t, info: info}
		}
		o := c.lookup(x.Name)
		prelude := c.isPreludeName(x.Name)
		if patternNameTests(o, prelude, r.letFor) {
			if _, isConst := o.(*Const); isConst {
				return r.constant(x, t, col)
			}
			return r.typeTest(x, t, col)
		}
		if r.letFor && prelude {
			// A let or for pattern has no type test on a bare name of
			// the prelude: the name is a binder, D-D3's S108 is its one
			// diagnostic, and it binds with the value's type.
			c.preludeName(x)
			info := &Pattern{Kind: PatBind}
			r.bind(x, t, info, false)
			return resolved{t: t, info: info}
		}
		switch o := o.(type) {
		case *Func, *Builtin, *PkgName:
			if r.bindings && reusableBinderName(o) {
				break
			}
			if r.bindings || c.isPreludeName(x.Name) {
				// A prelude function or built-in: D-D3, in every
				// binder kind (match and catch patterns included).
				c.preludeName(x)
			} else {
				c.errorf(x, CodeType, "a pattern name binds; %s already names %s: compare with if, or choose another name (rows.md, Totality 1)", x.Name, objectNoun(o))
				c.refuseBinder(x.Pos())
			}
			if p, ok := o.(*PkgName); ok {
				p.used = true // The refused occurrence must not add an unused import.
			}
			r.bind(x, invalid, &Pattern{Kind: PatBind}, false)
			return r.fail(nil, t)
		case *Var:
			if o.pkg && !r.bindings {
				c.errorf(x, CodeType, "a pattern name binds; %s already names a package value: compare with if, or choose another name (rows.md, Totality 1)", x.Name)
				c.refuseBinder(x.Pos())
				r.bind(x, invalid, &Pattern{Kind: PatBind}, false)
				return r.fail(nil, t)
			}
		}
		info := &Pattern{Kind: PatBind}
		r.bind(x, t, info, false)
		return resolved{t: t, info: info}
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		return r.typeTest(x, t, col)
	case *syntax.BasicLit, *syntax.UnaryExpr:
		return r.constant(x, t, col)
	case *syntax.CompositeLit:
		return r.record(x, t, col)
	case *syntax.TupleExpr:
		return r.tuple(x, t, col)
	case *syntax.AsPattern:
		inner := r.pattern(x.X, t, col)
		info := &Pattern{Kind: PatAs, Elems: []*Pattern{inner.info}}
		if inner.info == nil {
			info.Elems = []*Pattern{{Kind: PatWildcard}}
		}
		if x.Name.Name != "_" {
			r.bind(x.Name, inner.t, info, true)
		}
		inner.info = info
		return inner
	}
	c.undecided(e, "this pattern form")
	return r.fail(nil, t)
}

// reusableBinderName is D-D2's package-name exception. Prelude names are
// protected; match and catch never use this exception.
func reusableBinderName(o Object) bool {
	switch o := o.(type) {
	case *Func:
		return o.Pkg != nil && o.Pkg.Path != std.PreludePath
	case *PkgName:
		return true
	}
	return false
}

// objectNoun names a package object a pattern name may not bind over.
func objectNoun(o Object) string {
	switch o.(type) {
	case *Func:
		return "a function"
	case *Builtin:
		return "a built-in function"
	case *PkgName:
		return "an imported package"
	}
	return "a package value"
}

// typeTest resolves a type in a pattern: a variant of the subject's union,
// or the subject's own type. A match on an operation-type value admits
// only `_` and bindings (no type assertions).
func (r *patResolver) typeTest(e syntax.Expr, t Type, col int) resolved {
	ctor, vt, ok := r.head(e, t, col)
	if !ok {
		// A refused type test stays a type test (patternNameTests): the
		// name names the type, and binds nothing. Later reads in the
		// pattern's scope find a silent placeholder, not the type (D3).
		if ix, indexed := e.(*syntax.IndexExpr); indexed {
			e = ix.X
		}
		if id, ok := e.(*syntax.Ident); ok {
			r.placeholder(id)
		}
		return r.fail(nil, t)
	}
	info := &Pattern{Kind: PatTypeTest, Type: vt}
	if ctor < 0 {
		return resolved{t: vt, info: info} // the subject's own type: a wildcard
	}
	return resolved{pat: r.sp.node(ctor, nil), has: true, t: vt, info: info}
}

// head resolves the type a type test or a record pattern names against a
// value of type t at col: its constructor (-1 when it is t's own type)
// and the narrowed type; ok false when reported.
func (r *patResolver) head(e syntax.Expr, t Type, col int) (int, Type, bool) {
	c := r.c
	ci := r.info[col]
	switch {
	case ci.dom == domUnion:
		idx := c.caseVariant(e, ci.u, t, r.resolve)
		if idx == variantOver {
			r.resolve.over = true // reported once by the match (S103)
			return 0, nil, false
		}
		if idx == -1 {
			return -1, t, true
		}
		if idx < 0 {
			return 0, nil, false
		}
		return idx, ci.u.at(idx), true
	}
	pt := c.typExpr0(e, c.localTypeCtx())
	if isInvalid(pt) {
		return 0, nil, false
	}
	if asInterface(t) != nil {
		c.errorf(e, CodeType, "a match on %s, an operation-type value, admits only _ and bindings: Ergo has no type assertions (rows.md, Totality 1)", t)
		return 0, nil, false
	}
	if n, ok := pt.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		if tn, ok := t.(*Named); ok && tn.origin() == n {
			pt = t // a generic type written without its arguments
		}
	}
	if !c.identical(pt, t) {
		c.errorf(e, CodeType, "%s does not match a value of %s: a type in a pattern names a variant of the subject's union or the subject's own type (rows.md, Totality 1)", pt, t)
		return 0, nil, false
	}
	return -1, t, true
}

// constant resolves a constant pattern, typed by its subject (D-A2).
func (r *patResolver) constant(e syntax.Expr, t Type, col int) resolved {
	c := r.c
	if lit, _, ok := signedLit(e); ok && (lit.Kind == syntax.FLOAT || lit.Kind == syntax.DURATION) {
		what := "float"
		if lit.Kind == syntax.DURATION {
			what = "duration"
		}
		c.errorf(e, CodeType, "a %s pattern compares for equality, a trap for floats and durations; test with if (rows.md, Totality 1)", what)
		return r.fail(nil, t)
	}
	if ut, ok := under(t).(*Basic); ok && (isFloat(ut) || isComplex(ut) || ut.Kind == Duration) {
		c.errorf(e, CodeType, "a constant pattern on %s compares for equality, a trap for floats and durations; test with if (rows.md, Totality 1)", t)
		return r.fail(nil, t)
	}
	op := c.valueExpr(e, t)
	if op.invalid() {
		return r.fail(nil, t)
	}
	if op.mode != modeConst || op.val == nil {
		c.errorf(e, CodeType, "a pattern is a literal, a type, a record, a tuple or a name, not a value (rows.md, Totality 1)")
		return r.fail(nil, t)
	}
	if !c.identical(op.typ, t) {
		c.errorf(e, CodeType, "the constant %s is %s; the subject is %s (rows.md, Totality 1)", op.val.ExactString(), op.typ, t)
		return r.fail(nil, t)
	}
	info := &Pattern{Kind: PatConst, Value: op.val, Type: t}
	var ctor int
	switch r.info[col].dom {
	case domBool:
		if op.val.ExactString() == "true" {
			ctor = 1
		}
	case domConst:
		ctor = r.sp.constCtor(col, op.val.ExactString())
	default:
		c.errorf(e, CodeType, "a constant pattern matches an integer, rune, string or bool subject, not %s (rows.md, Totality 1)", t)
		return r.fail(nil, t)
	}
	return resolved{pat: r.sp.node(ctor, nil), has: true, t: t, info: info}
}

// record resolves a record pattern T{F: p, …}.
func (r *patResolver) record(x *syntax.CompositeLit, t Type, col int) resolved {
	c := r.c
	if x.Type == nil {
		c.errorf(x, CodeType, "a record pattern names its type: T{F: p} (rows.md, Totality 1)")
		return r.fail(x, t)
	}
	ctor, rt, ok := r.head(x.Type, t, col)
	if !ok {
		return r.fail(x, t)
	}
	s, isStruct := under(rt).(*Struct)
	if !isStruct {
		c.errorf(x, CodeType, "%s is not a record, so it has no fields to match; write case %s: (rows.md, Totality 1)", rt, exprText(x.Type))
		return r.fail(x, t)
	}
	if len(x.Elts) == 0 {
		// One form per job (spec D-B2): a type test is the bare name.
		c.errorf(x, CodeType, "a record pattern with no fields tests the type: write case %s: (rows.md, Totality 1)", exprText(x.Type))
		r.bad = true
		return resolved{t: rt}
	}
	recordCol := col
	if ctor < 0 {
		ctor = 0 // the subject's own record: its one shape
	}
	type fieldPat struct {
		idx int
		p   syntax.Expr
	}
	var fps []fieldPat
	if _, keyed := x.Elts[0].(*syntax.KeyValueExpr); !keyed {
		if len(s.Fields) != 1 || len(x.Elts) != 1 {
			c.errorf(x, CodeType, "a record pattern names its fields, %s{F: p}; only a record of one field may omit the key (rows.md, Totality 1)", exprText(x.Type))
			return r.fail(x, t)
		}
		fps = append(fps, fieldPat{0, x.Elts[0]})
	} else {
		seen := map[string]bool{}
		for _, e := range x.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			id, isID := kv.Key.(*syntax.Ident)
			if !ok || !isID {
				c.errorf(e, CodeType, "a record pattern names its fields, %s{F: p} (rows.md, Totality 1)", exprText(x.Type))
				return r.fail(x, t)
			}
			f, i := s.field(id.Name)
			if f == nil {
				c.errorf(id, CodeUnknown, "%s has no field %s (rows.md, Totality 1)", rt, id.Name)
				return r.fail(x, t)
			}
			if seen[id.Name] {
				c.errorf(id, CodeType, "field %s is matched twice (rows.md, Totality 1)", id.Name)
				return r.fail(x, t)
			}
			seen[id.Name] = true
			fps = append(fps, fieldPat{i, kv.Value})
		}
	}
	info := &Pattern{Kind: PatRecord, Type: rt}
	var kids solverRow
	for _, fp := range fps {
		f := s.Fields[fp.idx]
		child := r.child(recordCol, ctor, fp.idx, f.Type)
		sub := r.pattern(fp.p, f.Type, child)
		if sub.info != nil {
			info.Fields = append(info.Fields, FieldPattern{Name: f.Name, Pattern: sub.info})
		}
		if sub.has {
			kids = append(kids, cell{child, sub.pat})
		}
	}
	r.sp.sortRow(kids)
	return resolved{pat: r.sp.node(ctor, kids), has: true, t: rt, info: info}
}

// tuple resolves a tuple pattern against a tuple, or a type defined over
// one (D-B6).
func (r *patResolver) tuple(x *syntax.TupleExpr, t Type, col int) resolved {
	c := r.c
	tt, ok := under(t).(*Tuple)
	if !ok {
		c.errorf(x, CodeType, "a tuple pattern matches a tuple, not %s (rows.md, Totality 1)", t)
		return r.fail(x, t)
	}
	if len(x.Elts) != len(tt.Types) {
		c.errorf(x, CodeType, "a tuple pattern of %d elements does not match %s, a tuple of %d (rows.md, Totality 1)", len(x.Elts), t, len(tt.Types))
		return r.fail(x, t)
	}
	info := &Pattern{Kind: PatTuple}
	var kids solverRow
	for i, e := range x.Elts {
		child := r.child(col, 0, i, tt.Types[i])
		sub := r.pattern(e, tt.Types[i], child)
		el := sub.info
		if el == nil {
			el = &Pattern{Kind: PatWildcard}
		}
		info.Elems = append(info.Elems, el)
		if sub.has {
			kids = append(kids, cell{child, sub.pat})
		}
	}
	r.sp.sortRow(kids)
	return resolved{pat: r.sp.node(0, kids), has: true, t: t, info: info}
}

// isPatternType reports whether a bare name in a pattern tests the value
// (a type test or a constant) where c checks, outside a let or a for rule
// (D-B3 rule 1; patternNameTests).
func (c *checker) isPatternType(name string) bool {
	return c.patternTests(name, false)
}

// ---------------------------------------------------------------- match

// matchStmt checks a match: a SwitchStmt (every pattern a literal) or a
// TypeSwitchStmt, with one subject per tuple element of the header. Each
// case's patterns are resolved against the subjects; a case that the cases
// before it cover is S108 (unreachable); the match is total, by the
// usefulness solver over the case matrix, or has `case _` (S103; rows.md,
// Totality 2 and 5); each binding is read in its arm (S110).
func (c *checker) matchStmt(s syntax.Stmt, kw syntax.Pos, subjects []syntax.Expr, clauses []*syntax.CaseClause, body func([]syntax.Stmt)) {
	done := c.openScope()
	defer done()
	n := len(subjects)
	subj := make([]Type, n)
	valid := true
	for i, x := range subjects {
		op := c.valueExpr(x, nil)
		if op.invalid() {
			valid = false
			continue
		}
		t := c.typeOf(&op)
		if isInvalid(t) {
			valid = false
			continue
		}
		subj[i] = t
	}
	pop := c.push(brSwitch)
	defer pop()
	if valid {
		c.match(s, &Match{Subjects: subj, Arms: len(clauses)})
	}
	r := c.newPatResolver()
	r.sp.written = len(subjects)
	for _, cl := range clauses {
		if cl.List == nil {
			r.sp.written++
		}
		for _, item := range cl.List {
			r.sp.written += patternSize(item)
		}
	}
	cols := make([]int, n)
	if valid {
		for i, t := range subj {
			cols[i] = r.subject(t)
		}
	}
	// The case rows and bindings, resolved once, in source order: rowOf
	// [clause][item] is nil for a pattern that did not resolve (reported).
	hasDefault := false
	patternsOK := valid
	rowOf := make([][]solverRow, len(clauses))
	bindsOf := make([][][]patBinding, len(clauses))
	var all []solverRow
	for ci, cl := range clauses {
		if cl.List == nil {
			if hasDefault {
				c.errorf(cl, CodeType, "a match has at most one case _")
			}
			hasDefault = true
		}
		rowOf[ci] = make([]solverRow, len(cl.List))
		bindsOf[ci] = make([][]patBinding, len(cl.List))
		for ii, item := range cl.List {
			r.begin()
			row, ok := r.caseItem(item, n, subj, cols, valid)
			if r.resolve.over {
				patternsOK = false
			}
			if !ok || r.bad {
				patternsOK = false
				for k := range r.binds {
					r.binds[k].t = invalid
				}
				row = nil
			}
			bindsOf[ci][ii] = r.binds
			if row != nil {
				rowOf[ci][ii] = row
				all = append(all, row)
			}
		}
	}
	// The solvers see the columns in source order: the solver is
	// deterministic for a given source, and reordering the subjects may
	// change whether a match near the budget is refused, never a proved
	// verdict (D16 as amended in round 5). Reachability follows the cases'
	// order: a case is unreachable when the cases before it cover it.
	useful := r.sp.solverFor()
	matrix := newRowSet(useful)
	var origin []syntax.Node // the case of each row of matrix, in order
	judge := func(at syntax.Node, row solverRow) {
		u, ok := useful.caseUseful(matrix, row)
		if ok && !u {
			c.errorf(at, CodeType, "%s (unreachable)", c.coveredBy(matrix, origin))
			return
		}
		before := len(matrix.rows)
		matrix.addRow(row)
		if len(matrix.rows) > before {
			origin = append(origin, at)
		}
	}
	for ci, cl := range clauses {
		for ii, item := range cl.List {
			row := rowOf[ci][ii]
			if row == nil || r.resolve.over {
				continue
			}
			judge(item, row)
		}
		if cl.List == nil && valid && !r.resolve.over {
			// `case _` is judged like any case (D-B4): it is unreachable
			// when the cases before it are total, so a dead default never
			// hides a variant added later. Its question is the totality
			// question, under the same budget (rows.md, Totality 5). After
			// a reported pattern the cases before it are incomplete, so it
			// only covers.
			if patternsOK {
				judge(cl, solverRow{})
			} else {
				matrix.addRow(solverRow{})
				origin = append(origin, cl)
			}
		}
		inner := c.openScope()
		arm := c.declareArm(bindsOf[ci])
		body(cl.Body)
		c.unusedBindings(arm)
		inner()
	}
	// Totality is decided over the maximal case rows by a solver of its
	// own, so it does not depend on which cases reachability judged first.
	// A case _ covers totality; its reachability was proved above.
	total := r.sp.solverFor()
	set := newRowSet(total)
	if valid && patternsOK && !hasDefault {
		for _, row := range all {
			set.addMaximal(row)
		}
	}
	var (
		w       witness
		missing bool
		ok      = true
	)
	if valid && patternsOK && !hasDefault {
		w, missing, ok = total.total(set.all())
	}
	if c.silent == 0 {
		c.totalitySteps = append(c.totalitySteps, total.steps)
		c.totalityBudgets = append(c.totalityBudgets, total.budget)
		c.usefulSteps = append(c.usefulSteps, useful.steps)
		c.patternCosts = append(c.patternCosts, patternCost{r.sp.written, total.budget, total.meter.cellBudget, total.steps, useful.steps, total.cells, useful.cells})
	}
	c.totalSwitch[s] = true
	switch {
	case useful.over:
		// A case whose reachability is unproven is never accepted.
		c.errorIn(s, kw, kw+matchLen, CodeNotTotal, "too many cases to prove which cases of this match are reachable; split it (rows.md, Totality 5)")
	case r.resolve.over, !ok:
		c.errorIn(s, kw, kw+matchLen, CodeNotTotal, "too many cases to prove this match total; split it or add case _ (rows.md, Totality 5)")
	case !valid || !patternsOK || hasDefault:
		// Already reported, or total by `case _`: the match counts as
		// total, so no missing return or totality diagnostic follows (D3).
	case missing:
		parts := make([]string, n)
		allConst := true
		for j, col := range cols {
			parts[j] = r.sp.witnessText(w, col)
			if r.info[col].dom != domConst {
				allConst = false
			}
		}
		if n == 1 && allConst {
			c.errorIn(s, kw, kw+matchLen, CodeNotTotal, "this match is not total: constant patterns never cover %s; add case _ (rows.md, Totality 2)", subj[0])
			break
		}
		what := parts[0]
		if n > 1 {
			what = "(" + strings.Join(parts, ", ") + ")"
		}
		c.errorIn(s, kw, kw+matchLen, CodeNotTotal, "this match is not total: no case handles %s; name every variant or add case _ (rows.md, Totality 2)", what)
	}
}

// coveredBy says what covers an unreachable case row: the earlier case
// that alone covers it, by its text ("the earlier case `B`"; no line, so the
// message does not move with the declarations), or the cases before it together.
func (c *checker) coveredBy(m *rowSet, origin []syntax.Node) string {
	i := m.cover
	if i < 0 || i >= len(origin) {
		return "this case is already covered by the cases before it"
	}
	f := c.cur.syntax
	text := "_"
	if x, ok := origin[i].(syntax.Expr); ok {
		text = strings.Join(strings.Fields(f.Src[x.Pos():x.End()]), " ")
	}
	if rs := []rune(text); len(rs) > 40 {
		text = string(rs[:37]) + "…"
	}
	return fmt.Sprintf("this case is already covered by the earlier case `%s`", text)
}

// caseItem resolves one pattern of a case: a pattern of the subject, or of
// a match on several subjects a tuple of one pattern per subject.
func (r *patResolver) caseItem(item syntax.Expr, n int, subj []Type, cols []int, valid bool) (solverRow, bool) {
	c := r.c
	if !valid {
		r.bindNames(item)
		return nil, false
	}
	if n == 1 {
		res := r.pattern(item, subj[0], cols[0])
		if !res.has {
			return solverRow{}, true
		}
		return r.sp.flat(solverRow{{cols[0], res.pat}}), true
	}
	tup, ok := unparen(item).(*syntax.TupleExpr)
	if !ok || len(tup.Elts) != n {
		c.errorf(item, CodeType, "a case of a match on %d subjects is a tuple of %d patterns", n, n)
		r.bindNames(item)
		return nil, false
	}
	row := solverRow{}
	info := &Pattern{Kind: PatTuple}
	for j, e := range tup.Elts {
		res := r.pattern(e, subj[j], cols[j])
		el := res.info
		if el == nil {
			el = &Pattern{Kind: PatWildcard}
		}
		info.Elems = append(info.Elems, el)
		if res.has {
			row = append(row, cell{cols[j], res.pat})
		}
	}
	c.pattern(item, info)
	if tup != item {
		c.pattern(tup, info)
	}
	return r.sp.flat(row), true
}

// armVar is one binding of an arm with the group that records a read.
type armVar struct {
	name  *syntax.Ident
	group *switchGroup
	as    bool
}

// declareArm declares the names a case's patterns bind, in the current
// scope. With several patterns, each binds the same names with one type
// (rule 6, S108 otherwise); every pattern's identifier is a declaring
// identifier of the one variable.
func (c *checker) declareArm(all [][]patBinding) []armVar {
	if len(all) == 0 {
		return nil
	}
	// A refused type test's placeholder declares nothing: it is out of the
	// same-names check, and is declared after the names the case binds.
	items := make([][]patBinding, len(all))
	var placeholders []*syntax.Ident
	for k, item := range all {
		for _, b := range item {
			if b.placeholder {
				placeholders = append(placeholders, b.id)
				continue
			}
			items[k] = append(items[k], b)
		}
	}
	first := items[0]
	vars := map[string]*Var{}
	types := map[string]Type{}
	groups := map[string]*switchGroup{}
	var out []armVar
	for _, b := range first {
		g := &switchGroup{}
		groups[b.id.Name] = g
		c.localBinder(b.id)
		v := c.bindLocal(&Var{Name: b.id.Name, Type: b.t, Pos: b.id.Pos(), id: b.id, group: g, used: true})
		if isInvalid(b.t) {
			g.used = true // an invalid pattern adds no S110 (D3)
		}
		c.def(b.id, v)
		c.fn.scope.insert(b.id.Name, v)
		b.info.Var = v
		vars[b.id.Name] = v
		types[b.id.Name] = b.t
		out = append(out, armVar{name: b.id, group: g, as: b.as})
	}
	for k, item := range items[1:] {
		bound := map[string]bool{}
		for _, b := range item {
			bound[b.id.Name] = true
			v, ok := vars[b.id.Name]
			if !ok {
				if !isInvalid(b.t) {
					// An invalid binding is a reported pattern's
					// placeholder: no second diagnostic (D3).
					c.errorf(b.id, CodeType, "%s is bound by this pattern and not by the first pattern of its case; every pattern of a case binds the same names (rows.md, Totality 1)", b.id.Name)
				}
				// The name still binds, with the invalid type, so its reads
				// in the body add nothing (D3).
				g := &switchGroup{used: true}
				v := c.bindLocal(&Var{Name: b.id.Name, Type: invalid, Pos: b.id.Pos(), id: b.id, group: g, used: true})
				c.def(b.id, v)
				c.fn.scope.insert(b.id.Name, v)
				b.info.Var = v
				vars[b.id.Name], types[b.id.Name], groups[b.id.Name] = v, invalid, g
				continue
			}
			c.def(b.id, v)
			b.info.Var = v
			if t := types[b.id.Name]; !isInvalid(t) && !isInvalid(b.t) && !c.identical(t, b.t) {
				c.errorf(b.id, CodeType, "%s is bound as %s by the first pattern of its case and as %s here; every pattern of a case binds each name with one type (rows.md, Totality 1)", b.id.Name, t, b.t)
				groups[b.id.Name].used = true // reported: no S110 beside it (D3)
				// The name binds with the invalid type, so its reads in the
				// body add nothing (D3).
				v.Type, types[b.id.Name] = invalid, invalid
			}
		}
		reported := false
		for _, b := range first {
			if bound[b.id.Name] {
				continue
			}
			if !reported && !isInvalid(types[b.id.Name]) {
				c.errorf(c.caseItemNode(items, k+1, b), CodeType, "%s is bound by the first pattern of its case and not by this one; every pattern of a case binds the same names (rows.md, Totality 1)", b.id.Name)
				reported = true
			}
			groups[b.id.Name].used = true // reported: no S110 beside it (D3)
		}
	}
	for _, id := range placeholders {
		c.declarePlaceholder(id)
	}
	return out
}

// caseItemNode is a node to report a missing binding at: the first binding
// of the item, or the first pattern's binding when the item binds none.
func (c *checker) caseItemNode(items [][]patBinding, k int, b patBinding) syntax.Node {
	if len(items[k]) > 0 {
		return items[k][0].id
	}
	return b.id
}

// unusedBindings reports each binding its arm never reads (S110).
func (c *checker) unusedBindings(vars []armVar) {
	for _, v := range vars {
		if v.group.used {
			continue
		}
		name := v.name.Name
		hint := ""
		if r, _ := utf8.DecodeRuneInString(name); unicode.IsUpper(r) {
			hint = "; no type is named " + name + ", so the pattern binds a new name"
		}
		if v.as {
			c.errorf(v.name, CodeUnused, "the binding %s is read nowhere in its arm; drop `as %s`%s (rows.md, Totality 1)", name, name, hint)
			continue
		}
		c.errorf(v.name, CodeUnused, "the binding %s is read nowhere in its arm; write _ in its place%s (rows.md, Totality 1)", name, hint)
	}
}
