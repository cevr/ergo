package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

func (c *checker) stmtList(list []syntax.Stmt, _ bool) {
	for _, s := range list {
		c.stmt(s)
	}
}

func (c *checker) openScope() func() {
	saved := c.fn.scope
	c.fn.scope = newScope(saved)
	return func() { c.fn.scope = saved }
}

// declareLocal declares a local in the current block. A second declaration
// in one block is S108; a local beside a capability-row binding of the same
// name in the body's frame is S104 (rows-and-constraints §7 amendment 3).
func (c *checker) declareLocal(id *syntax.Ident, t Type) *Var {
	if id.Name == "_" {
		return nil
	}
	sc := c.fn.scope
	refused := c.localNameRules(id)
	if t == nil {
		t = invalid
	}
	v := c.bindLocal(&Var{Name: id.Name, Type: t, Pos: id.Pos(), id: id, refused: refused})
	c.def(id, v)
	sc.insert(id.Name, v)
	if isInvalid(t) || refused {
		v.used = true
	}
	c.fn.locals = append(c.fn.locals, v)
	c.recordUse(id, v)
	return v
}

// localNameRules runs the rules of a local's name about to bind in the
// current block, a let's value or a local context alike, and reports at
// most one diagnostic: a prelude name (D-D3; refused, and the name still
// binds), a second declaration in one block (S108), a capability-row
// binding of the same name in the body's frame (S104), or a binding of an
// enclosing scope (D-D2).
func (c *checker) localNameRules(id *syntax.Ident) (refused bool) {
	sc := c.fn.scope
	if c.preludeName(id) {
		// D-D3: the one diagnostic; the name still binds below, marked
		// used, so an unread refused binder adds no S110.
		refused = true
	} else if prev, ok := sc.names[id.Name]; ok && !isRefusedBinder(prev) {
		switch prev := prev.(type) {
		case *capBinding:
			c.errorf(id, CodeUnknown, "%s is in this function's capability row, so a local of that name in the same frame is an error; rename the local (rows-and-constraints §7 amendment 3)", id.Name)
		case *Var:
			// The second declaration is the error; the first is not
			// also reported as unused (no cascade, D3).
			prev.used = true
			if prev.param {
				c.errorf(id, CodeType, "%s is already a parameter of this function; choose another name (no shadowing, #196 196v rule 2)", id.Name)
			} else {
				c.errorf(id, CodeType, "%s is already declared in this block; choose another name (no shadowing, #196 196v rule 2)", id.Name)
			}
		default:
			c.errorf(id, CodeType, "%s is already declared in this block", id.Name)
		}
	} else {
		c.shadowed(id, sc)
	}
	return refused
}

// declarePlaceholder hides, for the rest of the current scope, the type or
// capability that a refused type test names from a read in a value
// position, so its later reads add no diagnostic (D3; fuzz-17d6; P1). The
// identifier stays a type test: Info.Uses keeps the object it names and
// Info.Defs has no entry, as the reference walk reads it. The placeholder
// has the invalid type, counts as read, and meets none of the binder rules
// (D-D2, D-D3, bound twice), because it declares nothing. It is a refused
// binder: every lookup outside a value position sees through it (P2). A
// name the scope already binds keeps that binding.
func (c *checker) declarePlaceholder(id *syntax.Ident) {
	if _, bound := c.fn.scope.names[id.Name]; bound {
		return
	}
	v := c.bindLocal(&Var{Name: id.Name, Type: invalid, Pos: id.Pos(), id: id, used: true, refused: true})
	c.fn.scope.insert(id.Name, v)
}

func (c *checker) stmt(s syntax.Stmt) {
	savedMark := c.stmtMark
	c.stmtMark = c.undiagSeq
	defer func() { c.stmtMark = savedMark }()
	switch s := s.(type) {
	case nil:
	case *syntax.DeclStmt:
		c.varDecl(s.Decl)
	case *syntax.ExprStmt:
		c.exprStmt(s)
	case *syntax.AssignStmt:
		c.assignStmt(s)
	case *syntax.ReturnStmt:
		c.returnStmt(s)
	case *syntax.BranchStmt:
		c.branchStmt(s)
	case *syntax.IfStmt:
		// An else block is a branch of its if, like the body: a jump in it
		// leaves what a jump in the body leaves (grammar.md, Statements 5).
		// The grammar has no bare block statement (Statements 4), so no
		// other block reaches stmt.
		done := c.openScope()
		c.condition(s.Cond)
		c.block(s.Body)
		switch e := s.Else.(type) {
		case *syntax.BlockStmt:
			c.block(e)
		case *syntax.IfStmt:
			c.stmt(e)
		}
		done()
	case *syntax.SwitchStmt:
		c.switchHeader(s, c.armStmts)
	case *syntax.TypeSwitchStmt:
		c.typeSwitchHeader(s, c.armStmts)
	case *syntax.ForStmt:
		c.forStmt(s)
	case *syntax.RangeStmt:
		c.rangeStmt(s)
	case *syntax.AssertStmt:
		c.assertStmt(s)
	default:
		c.undecided(s, "")
	}
}

// armStmts checks a statement match's arm.
func (c *checker) armStmts(list []syntax.Stmt) { c.stmtList(list, false) }

// switchHeader checks a match on literal patterns with body checking each
// arm; false when its header is not decided syntax (reported).
func (c *checker) switchHeader(s *syntax.SwitchStmt, body func([]syntax.Stmt)) bool {
	if s.Tag == nil {
		c.undecided(s, "a match with no subject")
		return false
	}
	c.matchStmt(s, s.Switch, []syntax.Expr{s.Tag}, s.Clauses, body)
	return true
}

// typeSwitchHeader checks any other match, one subject per tuple element.
func (c *checker) typeSwitchHeader(s *syntax.TypeSwitchStmt, body func([]syntax.Stmt)) bool {
	subjects := make([]syntax.Expr, len(s.Subjects))
	for i, g := range s.Subjects {
		subjects[i] = g.X
	}
	c.matchStmt(s, s.Switch, subjects, s.Clauses, body)
	return true
}

func (c *checker) block(b *syntax.BlockStmt) {
	if b == nil {
		return
	}
	done := c.openScope()
	c.stmtList(b.List, false)
	done()
}

func (c *checker) condition(x syntax.Expr) {
	if x == nil {
		return
	}
	op := c.valueExpr(x, nil)
	if op.invalid() {
		return
	}
	if !isBoolean(op.typ) {
		c.errorf(x, CodeType, "a condition must be a bool, not %s", op.typ)
	}
}

// recordDown records op for inner and for each parenthesis from outer down
// to it: a catch checked as a statement, not through expr, still has its
// Types entry (Info invariant 1).
func (c *checker) recordDown(outer, inner syntax.Expr, op operand) {
	for x := outer; ; x = x.(*syntax.ParenExpr).X {
		c.record(x, op)
		if x == inner {
			return
		}
	}
}

// exprStmt checks an expression statement: a call, try, catch or with
// (S110 otherwise). A fallible statement is S105.
func (c *checker) exprStmt(s *syntax.ExprStmt) {
	// The statement's value is discarded: a with, try or catch here is a
	// body with no result (D-C1; review m4).
	op := c.exprAs(s.X, nil, useDiscard)
	if op.invalid() {
		return
	}
	switch op.mode {
	case modeValue, modeConst, modeNoValue:
	default:
		c.needValue(&op)
		if op.invalid() {
			return
		}
	}
	c.handled(&op)
	c.unusedValue(s.X, op)
}

// unusedValue reports a value in statement position that is not a call's
// result, Go's "evaluated but not used" (S110): a literal, a name, an
// operator, a conversion or a built-in's value. A call's result is
// discarded. One rule for an expression statement and for the discarded
// tail of every body kind: an if or match arm, a with block, a trailing
// block, a catch arm and a body with no result (rows.md, Totality 3;
// stage C round 3 review m2).
func (c *checker) unusedValue(at syntax.Expr, op operand) {
	x := unparen(at)
	if !isCallLike(x) {
		c.errorf(at, CodeUnused, "this value is not used; an expression statement is a call, try, catch or with")
		return
	}
	call, ok := x.(*syntax.CallExpr)
	if !ok || op.mode == modeNoValue {
		return
	}
	// The call's resolved kind decides, never the callee's spelling, so a
	// generic N[int](n) and a qualified pkg.T(x) conversion are a
	// conversion like int8(n) (stage C round 4 review M1). Under silent
	// checking Info has no entry and errorf reports nothing.
	info := c.pkg.Info.Calls[call]
	if info == nil {
		return
	}
	switch {
	case info.Kind == CallLen:
		c.errorf(at, CodeUnused, "the value of len is not used")
	case info.Kind == CallConversion && !isCapability(info.Type):
		c.errorf(at, CodeUnused, "the conversion to %s is not used", info.Type)
	}
}

// namePattern is the single-name binder rule, one for a `let` and a `for`
// binder (stage B round 2 review, item 4): a single name that names a type
// or a constant of the package is a pattern (a type test, a constant); a
// prelude name is a binder that D-D3 refuses, binding with the value's
// type (declareLocal); any other name binds (patternNameTests).
func (c *checker) namePattern(id *syntax.Ident) bool {
	return c.patternTests(id.Name, true)
}

// varDecl checks a local var declaration. A var of a type with no zero
// value needs a value (types.md, Zero values 3).
func (c *checker) varDecl(d *syntax.VarDecl) {
	mut := d.Mut.IsValid()
	// declare binds one let name: immutable, or mut under `let mut`
	// (D-D1).
	declare := func(n *syntax.Ident, t Type) *Var {
		v := c.declareLocal(n, t)
		if v != nil {
			v.let, v.mut = true, mut
		}
		return v
	}
	for _, spec := range d.Specs {
		if lit := localContextLit(spec); lit != nil {
			// A local context (rows.md, Context 9).
			c.localContextLet(d, spec, lit)
			continue
		}
		if mut && spec.Pattern != nil {
			c.mutPattern(d)
		}
		p := spec.Pattern
		if p == nil && len(spec.Names) == 1 && c.namePattern(spec.Names[0]) {
			p = spec.Names[0]
		}
		if p != nil {
			// The copy is made from spec (owner.go): it reports in spec's file.
			patternSpec := clone(c, spec)
			patternSpec.Pattern = p
			c.letPattern(patternSpec)
			continue
		}
		var declared Type
		if spec.Type != nil {
			declared = c.typExpr(spec.Type, c.localTypeCtx())
		}
		switch {
		case len(spec.Values) == 0:
			if declared != nil && !isInvalid(declared) && !defaultable(declared) {
				c.errorf(spec.Names[0], CodeType, "let %s of type %s needs a value: %s has no zero value (types.md, Zero values 3)", spec.Names[0].Name, declared, declared)
			} else if declared != nil && !isInvalid(declared) {
				if a := zeroAuthority(declared, c.pkg); a != nil {
					c.errorf(spec.Names[0], CodeAuthority, "let %s of type %s needs a value: its zero value builds a new value of %s, and %s (types.md, Zero values 6)", spec.Names[0].Name, c.localTypeText(declared), qualifiedName(a), authorityText(a))
				}
			}
			for _, n := range spec.Names {
				if spec.Type == nil {
					c.untypedLet(n) // D-D1; n binds with the invalid type
				}
				declare(n, declared)
			}
		case len(spec.Values) == len(spec.Names):
			types := make([]Type, len(spec.Names))
			for i, v := range spec.Values {
				op := c.valueExpr(v, declared)
				if declared != nil {
					c.assign(&op, declared, "let "+spec.Names[i].Name)
					types[i] = declared
				} else {
					types[i] = c.typeOf(&op)
				}
			}
			for i, n := range spec.Names {
				if v := declare(n, types[i]); v != nil && mut && declared == nil {
					v.byValue = spec.Values[i]
				}
			}
		case len(spec.Values) == 1:
			types := c.multiValue(spec.Values[0], len(spec.Names))
			if declared != nil {
				c.assignElems(spec.Values[0], types, declared)
			}
			for i, n := range spec.Names {
				t := types[i]
				if declared != nil && !isInvalid(t) {
					t = declared
				}
				declare(n, t)
			}
		default:
			c.errorf(spec, CodeType, "%d names and %d values", len(spec.Names), len(spec.Values))
			for _, n := range spec.Names {
				declare(n, invalid)
			}
		}
	}
}

// multiValue checks a value that must give n values: a tuple of n
// elements (or a type defined over one), or a comma-ok map index.
func (c *checker) multiValue(x syntax.Expr, n int) []Type {
	out := make([]Type, n)
	for i := range out {
		out[i] = invalid
	}
	if n == 2 {
		switch e := unparen(x).(type) {
		case *syntax.IndexExpr:
			op := c.valueExpr(e, nil)
			if op.invalid() {
				return out
			}
			if base, ok := c.pkg.Info.Types[e.X]; ok {
				if _, isMap := under(base).(*Map); isMap {
					out[0], out[1] = op.typ, Typ[Bool]
					return out
				}
			}
			c.errorf(x, CodeType, "2 values from one value")
			return out
		}
	}
	op := c.valueExpr(x, nil)
	if op.invalid() || isInvalid(op.typ) {
		// A refused result type is reported at the signature.
		return out
	}
	// A type defined over a tuple gives its elements too (C45).
	tup, ok := under(op.typ).(*Tuple)
	if !ok || len(tup.Types) != n {
		got := 1
		if ok {
			got = len(tup.Types)
		}
		if op.mode == modeNoValue {
			got = 0
		}
		c.errorf(x, CodeType, "%d names for %d values", n, got)
		return out
	}
	copy(out, tup.Types)
	return out
}

// assignStmt checks `x = e` and `x op= e`: one target and one value. A
// `:=` or a statement with several sides is not produced by the decided
// parser (D-A5).
func (c *checker) assignStmt(s *syntax.AssignStmt) {
	if s.Tok == syntax.DEFINE || len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		c.undecided(s, "a declaring assignment, or one with several sides,")
		return
	}
	l, r := s.Lhs[0], s.Rhs[0]
	if s.Tok == syntax.ASSIGN {
		if isBlank(l) {
			op := c.valueExpr(r, nil)
			c.typeOf(&op)
			return
		}
		lop := c.lhs(l)
		var hint Type
		if !lop.invalid() {
			hint = lop.typ
		}
		op := c.valueExpr(r, hint)
		if lop.invalid() {
			return
		}
		if c.typedByValue(l, &op, lop.typ) {
			return
		}
		c.assign(&op, lop.typ, "assignment")
		return
	}
	lop := c.lhs(l)
	// The operation is made from the assignment (owner.go).
	bin := made(c, &syntax.BinaryExpr{X: l, OpPos: s.TokPos, Op: assignOp(s.Tok), Y: r}, s)
	if lop.invalid() {
		c.valueExpr(r, nil)
		return
	}
	op := c.binary(bin, nil)
	op.expr = r
	if !op.invalid() {
		c.assign(&op, lop.typ, "assignment")
	}
}

func isBlank(x syntax.Expr) bool {
	id, ok := x.(*syntax.Ident)
	return ok && id.Name == "_"
}

func assignOp(t syntax.Token) syntax.Token {
	switch t {
	case syntax.ADD_ASSIGN:
		return syntax.ADD
	case syntax.SUB_ASSIGN:
		return syntax.SUB
	case syntax.MUL_ASSIGN:
		return syntax.MUL
	case syntax.QUO_ASSIGN:
		return syntax.QUO
	case syntax.REM_ASSIGN:
		return syntax.REM
	case syntax.AND_ASSIGN:
		return syntax.AND
	case syntax.OR_ASSIGN:
		return syntax.OR
	case syntax.XOR_ASSIGN:
		return syntax.XOR
	case syntax.SHL_ASSIGN:
		return syntax.SHL
	case syntax.SHR_ASSIGN:
		return syntax.SHR
	case syntax.AND_NOT_ASSIGN:
		return syntax.AND_NOT
	}
	return syntax.ILLEGAL
}

// lhs checks an assignment target: a local, a field or an element, rooted
// at a `let mut` local (D-D1, Q2; mutableRoot). A package-level let is
// immutable (grammar.md, Declarations 6). Assigning does not read the
// variable.
func (c *checker) lhs(x syntax.Expr) operand {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		o := c.lookup(id.Name)
		if v, ok := o.(*Var); ok {
			// The root rule reports and never changes the typing (D-D1);
			// a package-level let stays unassignable, as before.
			c.mutableRoot(x)
			if v.pkg || v.Type == nil || isInvalid(v.Type) {
				return bad(x)
			}
			c.recordUse(id, v)
			op := operand{mode: modeValue, typ: v.Type, addressable: true, expr: x}
			// Info records the assigned variable's type, as go/types
			// records an assignment's left side (D1).
			for e := x; ; {
				c.record(e, op)
				p, ok := e.(*syntax.ParenExpr)
				if !ok {
					break
				}
				e = p.X
			}
			return op
		}
	}
	saved := c.writeTarget
	c.writeTarget = unparen(x)
	op := c.valueExpr(x, nil)
	c.writeTarget = saved
	if op.invalid() {
		// A target that fails typing (`c.Cuont = 2`) carries its own
		// diagnostic; its let mut root still counts as reassigned (D3).
		c.markReassigned(x)
		return op
	}
	c.assignable0(x, &op)
	if op.invalid() {
		// The target's own diagnostic is the one; its let mut root still
		// counts as reassigned, so no S110 joins it (D3).
		c.markReassigned(x)
	} else {
		c.mutableRoot(x)
	}
	return op
}

func (c *checker) assignable0(x syntax.Expr, op *operand) {
	if !op.addressable && !op.mapElem {
		c.errorf(x, CodeType, "cannot assign to %s", exprText(x))
		op.mode = modeInvalid
	}
}

// returnStmt checks return: a fallible value passes its success and its
// failures through; a value of an error type is a failure; return cannot
// leave a with block or a trailing block (rows.md, Error rows 4;
// grammar.md, Statements 5).
func (c *checker) returnStmt(s *syntax.ReturnStmt) {
	t := c.fn.t
	for _, b := range c.fn.branches {
		if b.kind == brBarrier {
			c.errorf(s, CodeType, "return cannot leave a with block or a block argument; the block yields its last expression (grammar.md, Statements 5)")
			for _, r := range s.Results {
				c.expr(r, nil)
			}
			return
		}
	}
	if !t.isFunc {
		c.errorf(s, CodeType, "return is not allowed here")
		return
	}
	results := t.results
	switch len(s.Results) {
	case 0:
		if len(results) > 0 {
			c.errorf(s, CodeType, "missing return value of type %s", results[0])
		}
		return
	case 1:
		// A law's value has no expected type, as its tail has none
		// (lawBody).
		var hint Type
		if len(results) == 1 && !t.law {
			hint = results[0]
		}
		op := c.expr(s.Results[0], hint)
		if op.invalid() {
			return
		}
		// The row is returned; an unknown row is fallible (Row.unknown), so
		// `return b()` over a refused member passes it on.
		switch op.mode {
		case modeValue, modeConst, modeNoValue:
		default:
			c.needValue(&op)
			if op.invalid() {
				return
			}
		}
		if op.fallible() {
			c.escape(s.Results[0], op.errs)
			op.errs = nil
			switch {
			case len(results) == 0 && op.mode != modeNoValue:
				c.errorf(s.Results[0], CodeType, "%s returns no value, and this expression has one", c.describeTarget())
			case len(results) == 1:
				c.returnValue(&op, results[0])
			case len(results) > 1:
				c.returnTuple(s.Results[0], op, results)
			}
			return
		}
		if row := c.returnedErrorRow(op.typ); row != nil && op.mode == modeValue {
			c.escape(s.Results[0], row)
			return
		}
		switch {
		case len(results) == 0:
			c.errorf(s.Results[0], CodeType, "%s returns no value", c.describeTarget())
		case len(results) == 1:
			c.returnValue(&op, results[0])
		default:
			c.returnTuple(s.Results[0], op, results)
		}
	default:
		if len(s.Results) != len(results) {
			c.errorf(s, CodeType, "%d values returned, %d expected", len(s.Results), len(results))
			for _, r := range s.Results {
				c.valueExpr(r, nil)
			}
			return
		}
		for i, r := range s.Results {
			op := c.valueExpr(r, results[i])
			c.assign(&op, results[i], "return")
		}
	}
}

// returnValue checks the success value of a return against the target's
// one result. A law's own body has no declared result: lawResult admits the
// value by the rule its tail uses (laws.go, rule 8).
func (c *checker) returnValue(op *operand, result Type) {
	if c.fn.t.law {
		t := op.typ
		if op.mode == modeNoValue {
			t = NoValue
		}
		c.lawResult(op.expr, t)
		return
	}
	c.assign(op, result, "return")
}

func (c *checker) returnTuple(x syntax.Expr, op operand, results []Type) {
	tup, ok := op.typ.(*Tuple)
	if !ok || len(tup.Types) != len(results) {
		c.errorf(x, CodeType, "%d values expected", len(results))
		return
	}
	as := make([]assignment, len(results))
	for i := range results {
		a, why := c.assignability(tup.Types[i], results[i])
		if c.sealedRefused(x, a) {
			return
		}
		if !a.ok() {
			c.errorf(x, CodeType, "cannot return %s as %s: %s", tup.Types[i], results[i], why)
			return
		}
		as[i] = a
	}
	c.implicitElems(x, as)
}

// assignElems checks the elements of x, a value of several elements that
// a multi-name let with a written type takes apart, against that type, and
// records each element's conversion (Info.ImplicitElems). One refused
// element is one diagnostic.
func (c *checker) assignElems(x syntax.Expr, types []Type, declared Type) {
	if containsInvalid(declared) {
		return
	}
	as := make([]assignment, len(types))
	for i, t := range types {
		if isInvalid(t) {
			continue
		}
		a, why := c.assignability(t, declared)
		if c.sealedRefused(x, a) {
			return
		}
		if !a.ok() {
			if why != "" {
				why = ": " + why
			}
			c.errorf(x, CodeType, "cannot use %s as %s%s", t, declared, why)
			return
		}
		as[i] = a
	}
	c.implicitElems(x, as)
}

// errorValueRow returns the failures that returning a value of type t
// escapes: {t} for an error type, the variants for the binding of a catch
// arm that names several error types (rows.md, Catch 7; M4), else nil.
func (c *checker) errorValueRow(t Type) *Row {
	if u, ok := t.(*Union); ok {
		row := newRow()
		for _, v := range u.Variants {
			if !c.isErrorValue(v) {
				return nil
			}
			row = row.with(c.types, v)
		}
		return row
	}
	if c.isErrorValue(t) {
		return c.types.row(t)
	}
	return nil
}

// returnedErrorRow is errorValueRow for a value a function body returns
// or yields: nil when the function's declared result is that error type,
// so the value is the success value, as a constructor
// func missing(k string) Missing { Missing{Key: k} } returns it (K1;
// rows.md, Error rows 4). In any other body an error value is a failure.
func (c *checker) returnedErrorRow(t Type) *Row {
	row := c.errorValueRow(t)
	if row == nil || c.fn == nil || c.fn.t == nil || c.fn.t.law {
		return row
	}
	if r := c.fn.t.results; len(r) == 1 && c.errorValueRow(r[0]) != nil && c.identical(t, r[0]) {
		return nil
	}
	return row
}

// isErrorValue reports whether t is an error type in the body being
// checked: a type named in an error row of its package that the body sees
// (types.md, Error types 1 and 2; owners.go).
func (c *checker) isErrorValue(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin()
	if c.errorTypeIn(o, c.owner) {
		return true
	}
	if p := o.Obj.Pkg; p != nil && p != c.pkg && p.ErrorTypes != nil {
		return p.IsErrorType(o, OwnerProduction)
	}
	return o.Obj.Pkg == universeOnce.goPkg && o.Obj.Name == "Error"
}

// ---------------------------------------------------------------- branches

// branchStmt checks break and continue: they may not leave a with block or
// a block argument (grammar.md, Statements 5).
func (c *checker) branchStmt(s *syntax.BranchStmt) {
	crossed := false
	for i := len(c.fn.branches) - 1; i >= 0; i-- {
		b := c.fn.branches[i]
		if b.kind == brBarrier {
			crossed = true
			continue
		}
		if b.kind != brLoop {
			continue
		}
		if crossed {
			c.errorf(s, CodeType, "%s cannot leave a with block or a block argument (grammar.md, Statements 5)", s.Tok)
		}
		return
	}
	if crossed {
		c.errorf(s, CodeType, "%s cannot leave a with block or a block argument (grammar.md, Statements 5)", s.Tok)
		return
	}
	c.errorf(s, CodeType, "%s is not inside a for", s.Tok)
}

func (c *checker) push(kind branchKind) func() {
	c.fn.branches = append(c.fn.branches, branch{kind: kind})
	return func() { c.fn.branches = c.fn.branches[:len(c.fn.branches)-1] }
}

func (c *checker) forStmt(s *syntax.ForStmt) {
	done := c.openScope()
	defer done()
	c.condition(s.Cond)
	pop := c.push(brLoop)
	c.block(s.Body)
	pop()
}

// ---------------------------------------------------------------- termination

// terminates reports whether the last statement of a body that is no
// expression, if, match or branch ends every path (yieldStmt's default
// arm, its one caller): a return, or a for with no condition and no break
// of its own (Go's rules). An if and a match at the end of a body are
// decided where they yield (ifYield, matchYield; rows.md, Totality 3; D8),
// so they never reach here.
func terminates(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.ForStmt:
		return s.Cond == nil && !hasBreak(s.Body)
	}
	return false
}

// hasBreak reports a break of the enclosing loop: match is transparent,
// and a nested for owns its own breaks (the decided language has no labels).
func hasBreak(s syntax.Stmt) bool {
	found := false
	syntax.Inspect(s, func(x syntax.Node) bool {
		if found {
			return false
		}
		switch x := x.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.BranchStmt:
			if x.Tok == syntax.BREAK {
				found = true
			}
		case *syntax.ForStmt, *syntax.RangeStmt:
			return false
		}
		return true
	})
	return found
}
