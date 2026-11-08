package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

func unparen(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

// try checks `try e`: e's whole row escapes to the enclosing failure target
// (rows.md, Error rows 4); try on an expression that cannot fail is S108.
// `try e catch { … }` is one partial catch: the rest escapes through try.
// try covers the whole expression to its right (196r, sixth round): every
// fallible value inside it propagates (handled), the caught expression of
// a partial catch included, while the catch handles its own row (D-C3).
func (c *checker) try(x *syntax.TryExpr, hint Type) operand {
	var t *target
	if c.fn != nil {
		t = c.fn.t
	}
	// try passes its own use on: `try with W { … }` and `try e catch { … }`
	// as statements discard their value (D-C1).
	use := c.useOf(x)
	inner := false
	if t != nil {
		saved := t.tried
		t.tried = false
		t.tries++
		defer func() {
			t.tries--
			t.tried = saved || t.tried
		}()
	}
	if ce, ok := unparen(x.X).(*syntax.CatchExpr); ok {
		openMark := c.openRowSeq
		op, valid := c.catchValid(ce, hint, use != useDiscard, true)
		op.expr = ce
		c.recordDown(x.X, ce, op)
		if op.invalid() {
			return op
		}
		// The rest escapes; an unknown rest is not empty (Row.unknown).
		if valid && op.errs.Empty() && (t == nil || !t.tried) && c.openRowSeq == openMark {
			// The catch handles the whole row and nothing inside it
			// propagates: try has nothing to pass on (review O3).
			c.errorf(x, CodeType, "try over a catch that handles every failure: nothing is left to propagate; drop the try (rows.md, Error rows 6)")
		}
		c.escape(x, op.errs)
		op.errs = nil
		return op
	}
	op := c.exprAs(x.X, hint, use)
	if t != nil {
		inner = t.tried
	}
	if op.invalid() {
		return bad(x)
	}
	switch op.mode {
	case modeValue, modeConst, modeNoValue:
	default:
		c.needValue(&op)
		if op.invalid() {
			return bad(x)
		}
	}
	// The row escapes; an unknown row is fallible (Row.unknown), so the
	// check below is silent on it.
	op.addressable = false
	if !op.fallible() && inner {
		return op
	}
	if !op.fallible() {
		c.errorf(x, CodeType, "try on an expression that cannot fail (rows.md, Error rows 6)")
		return op
	}
	c.escape(x, op.errs)
	op.errs = nil
	return op
}

// catch checks `e catch [b] { case T…: … }`. Each type must be in e's row
// and in one arm (S108); a catch without try handles every type (S103,
// rows.md Catch 4); a partial catch leaves the rest in its row. An arm is
// statements: when the catch's value is used, the arm's last statement
// yields it, and return in an arm leaves the function (rows.md, Catch 7).
func (c *checker) catch(x *syntax.CatchExpr, hint Type, wantValue, partial bool) operand {
	op, _ := c.catchValid(x, hint, wantValue, partial)
	return op
}

// caughtCall is the call a catch catches while it is checked, and the
// first argument of it whose failure no try or catch handles (handled
// leaves it to the catch: K4, G-ex-g-1).
type caughtCall struct {
	call *syntax.CallExpr
	arg  syntax.Expr
	row  *Row
}

// isArg reports whether x is one of the call's arguments, in parentheses
// or not.
func (k *caughtCall) isArg(x syntax.Expr) bool {
	for _, a := range k.call.Args {
		if unparen(a) == unparen(x) {
			return true
		}
	}
	return false
}

// catchValid is catch, and whether the catch is valid: its rest row (a
// partial catch's errs) is then exact.
func (c *checker) catchValid(x *syntax.CatchExpr, hint Type, wantValue, partial bool) (operand, bool) {
	openMark := c.openRowSeq
	// A catch whose value is discarded discards its caught expression's
	// value too (D-C1).
	use := useValue
	if !wantValue {
		use = useDiscard
	}
	prevCaught := c.caught
	c.caught = nil
	if call, ok := unparen(x.X).(*syntax.CallExpr); ok {
		c.caught = &caughtCall{call: call}
	}
	op := c.exprAs(x.X, hint, use)
	caught := c.caught
	c.caught = prevCaught
	argOnly := false
	if caught != nil && caught.arg != nil {
		// A failing argument of the caught call: the catch handles the
		// call's failures, not its arguments' (rows.md, Catch 5).
		callee, arg := exprText(caught.call.Fun), exprText(caught.arg)
		if a, ok := unparen(caught.arg).(*syntax.CallExpr); ok {
			arg = exprText(a.Fun)
		}
		if op.invalid() || !op.errs.Empty() {
			c.errorf(caught.arg, CodeUnhandled, "the failure %s of this expression is not handled: use try, catch or return; the catch handles the failures of %s(…), not of its argument %s(…) (rows.md, Error rows 3; Catch 5)", rowText(caught.row), callee, arg)
		} else {
			c.errorf(caught.arg, CodeUnhandled, "a catch handles the failures of %s(…), not of its argument %s(…); catch %s(…) first (rows.md, Catch 5)", callee, arg, arg)
			argOnly = true
		}
	}
	valid := !op.invalid()
	// open: the row may still grow in this component (rowWaits). operandOK:
	// the caught expression is valid, so an arm its row lacks is not a
	// cascade of its diagnostic. unknown: the caught row holds invalid, so
	// no absence check holds on it (Row.unknown).
	unknown := valid && op.errs.unknown(c.types)
	open, operandOK := c.openRowSeq != openMark, valid && !unknown
	if valid {
		switch op.mode {
		case modeValue, modeConst, modeNoValue:
		default:
			c.needValue(&op)
			valid = !op.invalid()
		}
	}
	row := op.errs
	// The catch's value has the caught expression's type whatever its row:
	// a catch on an expression that cannot fail is a mistake in the row,
	// not in the type, so the value keeps its type and nothing cascades
	// from it. No type depends on an inferred row this way, so a call x.f(…)
	// on the value resolves before the rows settle (selfcalls.go; round 8
	// review M1).
	hasValue := valid && op.mode != modeNoValue
	if valid && row.Empty() { // an unknown row is not empty
		if !argOnly {
			c.errorf(x.X, CodeType, "catch on an expression that cannot fail (rows.md, Catch 1)")
		}
		valid = false
	}
	var valueType Type
	if hasValue {
		valueType = c.typeOf(&op)
		if op.mode == modeConst {
			op.typ = valueType
		}
	}
	// The patterns of every arm resolve first, in source order, into one
	// catch space (one budget); then the arms are judged in source order
	// and their bodies checked (D-C3).
	cs := c.newCatchSpace(x)
	items := make([][]catchItem, len(x.Arms))
	for ai, arm := range x.Arms {
		for _, te := range arm.Types {
			it, ok := cs.resolve(arm, te, row, open, operandOK)
			if !ok {
				valid = false
			}
			items[ai] = append(items[ai], it)
		}
	}
	// rested: a `case err:` or `case _:` arm handled the rest; every arm
	// after it is unreachable (D-C3).
	rested := false
	for ai, arm := range x.Arms {
		// One binding set per pattern of the arm: every pattern binds the
		// same names with one type (rule 6 of the pattern language, D-B3).
		var lists [][]patBinding
		// The names of the arm's skipped patterns (catchItem.placeholders).
		var placeholders []*syntax.Ident
		// The `case err:` binding of the rest, with its row value type.
		var restName *syntax.Ident
		var restType Type
		for _, it := range items[ai] {
			unreachable := rested && valid
			if unreachable {
				c.errorf(it.te, CodeType, "this arm is unreachable: the case err: or case _ before it handles every failure (rows.md, Catch 2)")
			}
			if it.skip {
				placeholders = append(placeholders, it.placeholders...)
				continue
			}
			if it.rest {
				rest := row.without(c.types, cs.covered(row))
				if valid && !unknown && !unreachable && !cs.over() && rest.Empty() {
					c.errorf(it.te, CodeType, "this arm is unreachable: the arms before it handle every failure of the row %s (rows.md, Catch 2)", rowText(row))
				}
				rested = true
				for _, e := range rest.Elems() {
					cs.coverWhole(e, it.te)
				}
				if it.restID.Name == "_" {
					continue
				}
				restName, restType = it.restID, c.rowValue(rest)
				if !valid || it.restRefused || rest.Empty() || rest.unknown(c.types) {
					restType = invalid
				}
				continue
			}
			binds := it.binds
			if it.t != nil && !unreachable && cs.judge(it, row, valid && !unknown) && it.whole {
				// A type test of a type outside the row is refused: it
				// declares its placeholder (rows.md, Catch 7).
				core, _ := asParts(it.te)
				if id := testName(core); id != nil {
					binds = append(binds[:len(binds):len(binds)], placeholderBinding(id))
				}
			}
			lists = append(lists, binds)
		}
		saved := c.fn.scope
		c.fn.scope = newScope(saved)
		vars := c.declareArm(lists)
		var restBind []asBinding
		if restName != nil {
			if b, ok := c.declareAs(restName, restType, false); ok {
				restBind = append(restBind, b)
				if v, isVar := c.fn.scope.names[restName.Name].(*Var); isVar && c.silent == 0 {
					if p := c.pkg.Info.Patterns[restName]; p != nil {
						p.Var = v
					}
				}
			}
		}
		for _, id := range placeholders {
			if _, bound := c.fn.scope.names[id.Name]; !bound {
				c.declareAs(id, invalid, true) // no type: no S110, no cascade
			}
		}
		// Where a value is wanted, an invalid caught expression's arms
		// still yield, into the invalid type, so their values add nothing
		// (D3) and are not discarded statements.
		if wantValue && op.invalid() {
			c.catchArm(arm, invalid, true)
		} else {
			c.catchArm(arm, valueType, wantValue && hasValue)
		}
		c.unusedBindings(vars)
		for _, b := range restBind {
			if !b.group.used {
				c.errorf(b.name, CodeUnused, "the binding %s is read nowhere in its arm; write case _%s (rows.md, Totality 1)", b.name.Name, capitalisedBinderHint(b.name.Name))
			}
		}
		c.fn.scope = saved
	}
	cs.record()
	if cs.over() {
		c.errorIn(x, x.Catch, x.Catch+5, CodeNotTotal, "too many cases to prove which arms of this catch are reachable and what it handles; split it (rows.md, Totality 5)")
		valid = false
	}
	if !valid {
		if partial && unknown {
			// try passes the rest on, and the rest of an unknown row is
			// unknown.
			c.escape(x, newRow().with(c.types, invalid))
		}
		if hasValue {
			return operand{mode: modeValue, typ: valueType}, false
		}
		return bad(x), false
	}
	rest := row.without(c.types, cs.covered(row))
	// An unknown rest is checked by its known part (Row.unknown).
	if known := rest.known(c.types); !partial && !known.Empty() {
		c.errorIn(x, x.Catch, x.Catch+5, CodeNotTotal, "this catch does not handle %s; a catch without try handles every error of the row (rows.md, Catch 4)", rowText(known))
	}
	if !partial && rest.unknown(c.types) {
		// What it may not handle makes the target's row unknown, as an
		// unhandled unknown row does (handled).
		c.escape(x, newRow().with(c.types, invalid))
	}
	m := &Match{Subjects: row.known(c.types).Elems(), Arms: len(x.Arms), Partial: partial}
	if hasValue && wantValue {
		m.Result = valueType
	}
	c.match(x, m)
	res := operand{mode: modeValue, typ: valueType}
	if !hasValue {
		res = operand{mode: modeNoValue, typ: NoValue}
	}
	if partial {
		res.errs = rest
	}
	return res, true
}

// catchSpace is the one pattern space of a catch (D-C3): a subject column
// per error type a record pattern names, and per caught type a matrix of
// the rows its arms wrote, in source order. A type test is the empty row,
// which covers its type. One solver answers every question of the catch,
// so the whole catch shares one budget (rows.md, Totality 5).
type catchSpace struct {
	c      *checker
	r      *patResolver
	cols   map[int32]int // error type id → its subject column
	mats   map[int32]*catchMatrix
	solver *matrixSolver
	// wholeSeen: the types a type test already named, for the "caught
	// twice" diagnostic.
	wholeSeen map[int32]bool
}

type catchMatrix struct {
	rows   *rowSet
	origin []syntax.Node // the pattern of each row, in order
}

// catchItem is one pattern of a catch arm, resolved.
type catchItem struct {
	te     syntax.Expr   // the pattern as written
	skip   bool          // reported: the pattern takes no part
	rest   bool          // `case err:` or `case _`
	restID *syntax.Ident // the rest's name
	t      Type          // the error type, nil when it did not resolve
	row    solverRow     // its matrix row; nil after a reported pattern
	whole  bool          // a type test: it covers t
	binds  []patBinding  // its names, `as` included
	// placeholders: the names a skipped pattern writes, bound in its arm
	// with no type so a read there resolves to them, as the reference
	// walk does, and adds no diagnostic (D3; stage C round 6 fuzz).
	placeholders []*syntax.Ident
	// restRefused: the rest's name is Totality 1's S108 (refusedRest), so
	// it binds with no type.
	restRefused bool
}

// newCatchSpace starts the pattern space of x. Its resolver is a match's:
// the arms are match arms over the failure (rows.md, Catch 7), so a name
// in a record pattern that names a package function, an imported package
// or a package value is Totality 1's S108, never D-D2's let/for reuse
// (rows.md, Totality 6; fuzz-17d6).
func (c *checker) newCatchSpace(x *syntax.CatchExpr) *catchSpace {
	r := c.newPatResolver()
	for _, arm := range x.Arms {
		for _, te := range arm.Types {
			r.sp.written += 1 + patternSize(te)
		}
	}
	return &catchSpace{c: c, r: r, cols: map[int32]int{}, mats: map[int32]*catchMatrix{}, wholeSeen: map[int32]bool{}}
}

// resolve resolves one pattern of arm: `case err:`, `case _`, a type test,
// a record pattern T{F: p}, each with an optional `as` name. ok is false
// after a reported pattern.
func (cs *catchSpace) resolve(arm *syntax.CatchArm, te syntax.Expr, row *Row, open, evidence bool) (catchItem, bool) {
	c, r := cs.c, cs.r
	core, name := asParts(te)
	it := catchItem{te: te}
	if id, isID := core.(*syntax.Ident); isID && (id.Name == "_" || c.catchAll(id)) {
		if len(arm.Types) > 1 || name != nil {
			c.errorf(te, CodeType, "case %s: handles the rest of the row and stands alone in its arm (rows.md, Catch 2)", id.Name)
			it.skip = true
			if id.Name != "_" {
				it.placeholders = append(it.placeholders, id)
			}
			if name != nil && name.Name != "_" {
				it.placeholders = append(it.placeholders, name)
			}
			return it, false
		}
		it.rest, it.restID = true, id
		it.restRefused = c.refusedRest(id)
		if id.Name == "_" {
			c.pattern(core, &Pattern{Kind: PatWildcard})
		} else {
			c.pattern(core, &Pattern{Kind: PatBind})
		}
		return it, true
	}
	r.begin()
	ok := true
	var p *Pattern
	if lit, isLit := core.(*syntax.CompositeLit); isLit {
		var t Type
		if lit.Type == nil {
			c.errorf(lit, CodeType, "a record pattern in a catch arm names its error type: T{F: p} (rows.md, Catch 2)")
		} else {
			t = c.catchType(lit.Type, row, open, evidence)
		}
		if t == nil {
			r.bindNames(lit)
			ok = false
		} else {
			col := cs.column(t)
			res := r.pattern(lit, t, col)
			p = res.info
			it.t = t
			if r.bad || r.resolve.over {
				ok = false
			} else {
				it.row = solverRow{}
				if res.has {
					it.row = r.sp.flat(solverRow{{col, res.pat}})
				}
			}
		}
	} else if t := c.catchType(core, row, open, evidence); t != nil {
		p = c.pattern(core, &Pattern{Kind: PatTypeTest, Type: t})
		it.t, it.row, it.whole = t, solverRow{}, true
	} else {
		// A refused or unknown type, already reported: the arm proves
		// nothing, so no totality verdict follows, as for a record pattern.
		// The test declares its placeholder, as a match arm's does
		// (rows.md, Catch 7).
		r.refusedTest(core)
		ok = false
	}
	if name != nil && name.Name != "_" {
		bt := it.t
		info := &Pattern{Kind: PatAs}
		if p != nil {
			info = c.asPattern(te, p, name)
		}
		if p == nil || !ok {
			bt = invalid
		}
		r.bind(name, bt, info, true)
		if r.bad {
			ok = false
		}
	}
	if !ok {
		for k := range r.binds {
			r.binds[k].t = invalid
		}
		it.row = nil
	}
	it.binds = r.binds
	return it, ok
}

// column is the subject column of error type t.
func (cs *catchSpace) column(t Type) int {
	id := cs.c.types.id(t)
	if col, ok := cs.cols[id]; ok {
		return col
	}
	col := cs.r.subject(t)
	cs.cols[id] = col
	return col
}

// matrix is the rows written so far for the error type id.
func (cs *catchSpace) matrix(id int32) *catchMatrix {
	if cs.solver == nil {
		cs.solver = cs.r.sp.solverFor()
	}
	m := cs.mats[id]
	if m == nil {
		m = &catchMatrix{rows: newRowSet(cs.solver)}
		cs.mats[id] = m
	}
	return m
}

// over reports a budget that ran out: nothing more is proved.
func (cs *catchSpace) over() bool {
	return cs.r.resolve.over || (cs.solver != nil && cs.solver.over)
}

// judge adds a pattern of a valid type to its matrix. A pattern the rows
// before it cover is unreachable, a fact of the arms alone, so a row
// diagnostic never hides it (round 13 review M1); then the type must be in
// the caught row (rows.md, Catch 2). It reports whether the type is
// refused for not being in the row: a refused type test, as a match arm's
// test of a type that does not match the subject.
func (cs *catchSpace) judge(it catchItem, row *Row, valid bool) (refused bool) {
	c := cs.c
	id := c.types.id(it.t)
	c.types.charge(1)
	if it.row != nil && !cs.over() {
		m := cs.matrix(id)
		u, ok := cs.solver.caseUseful(m.rows, it.row)
		if ok && !u {
			if it.whole && cs.wholeSeen[id] {
				c.errorf(it.te, CodeType, "%s is caught twice (rows.md, Catch 2)", it.t)
			} else {
				c.errorf(it.te, CodeType, "%s (unreachable; rows.md, Catch 2)", c.coveredBy(m.rows, m.origin))
			}
			return
		}
		if ok {
			before := len(m.rows.rows)
			m.rows.addRow(it.row)
			if len(m.rows.rows) > before {
				m.origin = append(m.origin, it.te)
			}
		}
	}
	if it.whole {
		cs.wholeSeen[id] = true
	}
	if valid && !row.has(c.types, it.t) {
		c.errorf(it.te, CodeType, "%s is not in the caught row %s (rows.md, Catch 2)", it.t, rowText(row))
		return true
	}
	return false
}

// coverWhole records that the `case err:` or `case _` arm at n handles
// every value of e.
func (cs *catchSpace) coverWhole(e Type, n syntax.Node) {
	m := cs.matrix(cs.c.types.id(e))
	before := len(m.rows.rows)
	m.rows.addRow(solverRow{})
	if len(m.rows.rows) > before {
		m.origin = append(m.origin, n)
	}
}

// covered is the set of row types the arms so far handle whole: a type
// whose matrix is total. A type with no arm is not covered.
func (cs *catchSpace) covered(row *Row) map[int32]bool {
	out := map[int32]bool{}
	for _, e := range row.Elems() {
		id := cs.c.types.id(e)
		m := cs.mats[id]
		if m == nil || cs.over() {
			continue
		}
		if u, ok := cs.solver.caseUseful(m.rows, solverRow{}); ok && !u {
			out[id] = true
		}
	}
	return out
}

// record keeps the catch's proof costs beside the matches' (metered tests).
func (cs *catchSpace) record() {
	c := cs.c
	if cs.solver == nil || c.silent != 0 {
		return
	}
	s := cs.solver
	c.totalitySteps = append(c.totalitySteps, s.steps)
	c.totalityBudgets = append(c.totalityBudgets, s.budget)
	c.usefulSteps = append(c.usefulSteps, s.steps)
	c.patternCosts = append(c.patternCosts, patternCost{cs.r.sp.written, s.budget, s.meter.cellBudget, s.steps, s.steps, s.cells, s.cells})
}

// catchType resolves an arm's type. A generic error type written without
// type arguments takes the instance its row holds. A row that holds no
// instance gives nil with no diagnostic when it may still grow in this
// component (open: the type is provisional, rowWaits) or is no evidence
// (evidence false: the caught expression is invalid, so its own
// diagnostic explains the empty row); otherwise the arm needs its type
// arguments (round 11 review M1 and O1). This error makes the binding
// invalid, so it is the diagnostic an invalid-diagnosed frame, and a
// dropped S108, rest on.
func (c *checker) catchType(te syntax.Expr, row *Row, open, evidence bool) Type {
	t := c.typExpr0(te, c.localTypeCtx())
	if isInvalid(t) {
		return nil
	}
	if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		for _, e := range row.Elems() {
			if en, ok := e.(*Named); ok && en.origin() == n {
				return en
			}
		}
		if c.rowWaits(open) || !evidence {
			return nil
		}
		c.errorf(te, CodeType, "generic type %s needs type arguments", n)
		return nil
	}
	return t
}

// catchArm checks an arm's statements by the one tail rule (D-C1, D-C2):
// when the catch's value is used (yield), every path of the arm yields a
// value of the caught expression's type or leaves the function (S107 for
// a path without one); otherwise the arm's tail is discarded. A failure of
// the tail must be handled, as in any value position.
func (c *checker) catchArm(arm *syntax.CatchArm, valueType Type, yield bool) {
	list := arm.Body
	quiet := isInvalid(valueType) // the caught expression is reported
	if len(list) == 0 {
		if yield && !quiet {
			c.errorIn(arm, arm.Colon, arm.Colon+1, CodeMissingRet, "this arm yields no value, and the catch yields a value of type %s (rows.md, Catch 3)", valueType)
		}
		return
	}
	k := &sink{what: "the catch arm's value", discard: !yield}
	if yield {
		k.want = valueType
		k.missing = func(at syntax.Pos) {
			if quiet {
				return
			}
			c.errorIn(arm, at, at+1, CodeMissingRet, "this path of the arm can reach its end without a value, and the catch yields a value of type %s: end it with an expression, return or an else (rows.md, Catch 7)", valueType)
		}
	}
	c.yieldList(list, k, list[len(list)-1].Pos())
}

// preludeType returns a prelude declaration's type by name.
func preludeType(name string) *Named {
	p := prelude().pkg
	if p == nil {
		return nil
	}
	tn, ok := p.Scope.names[name].(*TypeName)
	if !ok {
		return nil
	}
	n, _ := tn.Type.(*Named)
	return n
}

// rowValue is the type of a `case err:` binding: the row value of the
// failures rest holds, the anonymous union of their error types (D-C3). It
// can be returned (a failure of those types), matched, caught or assigned
// to a declared union that lists each of them (ConvRow); nothing else.
func (c *checker) rowValue(rest *Row) Type {
	return &Union{Variants: rest.Elems()}
}
