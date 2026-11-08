package check

import "ergo.local/prototype/internal/sketch/syntax"

// Tail results (196r; spec D-C1 and D-C2). A body yields its last
// statement: an expression statement's value, or the value of a final if
// or match, whose branches (an else block among them) yield their own last
// statements in turn. One walk, yieldList, checks every such body against a sink: a
// function body's result, a value if or match in an expression, a catch
// arm, or a with block's or trailing block's known result. A branch that
// ends in return, break or continue has no value and joins any type.

// useMode is how an expression's value is used: one discard rule for every
// body with no result (D-C1).
type useMode int

const (
	// useValue: the value is used (an operand, an argument, a let).
	useValue useMode = iota
	// useDiscard: the value is dropped (an expression statement, the tail
	// of a body with no result, the caught expression of a statement
	// catch).
	useDiscard
	// useOptional: the tail of a body whose own use is not known yet (a
	// with block or an inferred trailing block).
	useOptional
)

type useAt struct {
	x syntax.Expr
	m useMode
}

// exprAs checks x, whose value is used as m: a with, try or catch at x reads
// m through useOf. The record names x itself, so no other expression reads
// it, and it does not outlive the check of x.
func (c *checker) exprAs(x syntax.Expr, hint Type, m useMode) operand {
	saved := c.use
	c.use = useAt{unparen(x), m}
	op := c.expr(x, hint)
	c.use = saved
	return op
}

// useOf is how x is used: what exprAs recorded for it, else useValue. It
// reads the record once.
func (c *checker) useOf(x syntax.Expr) useMode {
	if c.use.x != nil && c.use.x == x {
		m := c.use.m
		c.use = useAt{}
		return m
	}
	return useValue
}

// sink is where a body's last value goes.
type sink struct {
	// want is the expected type. Without one, the branches have one type:
	// the first branch with a value fixes it (join), and later literals take
	// it (D-C2).
	want, join Type
	// tuple holds a legacy result list of several types (returnTuple).
	tuple []Type
	// discard drops the value: a body with no result (D-C1).
	discard bool
	// ret marks a function body: return e means the same as a tail e, so a
	// fallible tail's failures escape and an error value is a failure.
	ret bool
	// pass receives a fallible tail when the sink is not a
	// function body; nil reports S105 (the failure must be handled).
	pass func(*operand)
	// what names the value in an assignment mismatch.
	what string
	// missing reports a path that ends in a statement with no value, at the
	// closing brace end of its body (D-C1, S107).
	missing func(end syntax.Pos)
	// noElse reports an if without else on a path that needs a value; nil
	// uses missing at the enclosing body's end.
	noElse func(s *syntax.IfStmt)
	// optional: whether the body has a value is not known yet (a with block,
	// or a trailing block whose result is inferred). A path with no value
	// makes the body yield none (none), and the place that uses the body's
	// value reports it, not the body.
	optional, none bool
	// hint is an optional body's expected type: the type a literal tail
	// takes, never a type the branches must have (M16).
	hint Type
	// noValue is the tail with no value that the path missing reports
	// ends in, while missing runs: a call that returns nothing.
	noValue syntax.Expr
	// joins: the branches of a value if or match, which join variants of
	// one union to it when there is no expected type (unionJoin).
	joins bool
	// valued: some branch yielded a value.
	valued bool
	// bad: a branch's value was invalid and already reported (D3).
	bad bool
}

// typ is the type the sink's branches yield so far.
func (k *sink) typ() Type {
	if k.want != nil {
		return k.want
	}
	return k.join
}

// exprHint is the expected type of a tail expression.
func (k *sink) exprHint() Type {
	if t := k.typ(); t != nil {
		return t
	}
	return k.hint
}

// miss is a path that ends with no value at end: an optional body then has
// no value; any other body reports it.
func (k *sink) miss(end syntax.Pos) {
	if k.optional && k.missing == nil {
		k.none = true
		return
	}
	k.missing(end)
}

// functionBody checks a declared function's or literal's body: its last
// statement is an implicit return (D-C1); a void body discards it.
func (c *checker) functionBody(body *syntax.BlockStmt, name string) {
	results := c.fn.t.results
	k := &sink{ret: true, what: "the body's tail value"}
	switch len(results) {
	case 0:
		k.discard = true
	case 1:
		k.want = results[0]
	default:
		k.tuple = results
	}
	k.missing = func(end syntax.Pos) {
		for _, r := range results {
			if containsInvalid(r) {
				return // the result type has its own diagnostic (D3)
			}
		}
		if end == body.Rbrace {
			c.missingBodyResult(body, name)
			return
		}
		c.errorIn(body, end, end+1, CodeMissingRet, "this branch can reach its end without a value, and %s needs one: end it with an expression, return or an else (rows.md, Totality 3)", name)
	}
	c.yieldList(body.List, k, body.Rbrace)
}

func (c *checker) missingBodyResult(body *syntax.BlockStmt, name string) {
	if len(c.fn.t.results) > 0 {
		c.errorIn(body, body.Rbrace, body.Rbrace+1, CodeMissingRet, "%s can reach the end of its body without returning a value (rows.md, Totality 3)", name)
	}
}

// yieldList checks a statement list whose last statement yields to k; end
// is the list's closing brace.
func (c *checker) yieldList(list []syntax.Stmt, k *sink, end syntax.Pos) {
	if len(list) == 0 {
		if !k.discard {
			k.miss(end)
		}
		return
	}
	c.stmtList(list[:len(list)-1], false)
	c.yieldStmt(list[len(list)-1], k, end)
}

// yieldStmt checks the last statement of a body.
func (c *checker) yieldStmt(s syntax.Stmt, k *sink, end syntax.Pos) {
	savedMark := c.stmtMark
	c.stmtMark = c.undiagSeq
	defer func() { c.stmtMark = savedMark }()
	switch s := s.(type) {
	case *syntax.ExprStmt:
		c.yieldExpr(s, k, end)
		return
	case *syntax.IfStmt:
		c.ifYield(s, k, end)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		if !c.matchYield(s, k) {
			k.bad = true
			return
		}
	case *syntax.BranchStmt:
		// break and continue leave the branch: no value, joins any type.
		c.stmt(s)
		return
	default:
		c.stmt(s)
		if !k.discard && !terminates(s) {
			k.miss(end)
		}
		return
	}
	if !k.discard && k.typ() != nil {
		c.tail(s, k.typ())
	}
}

// yieldExpr checks a body's last expression statement against k.
func (c *checker) yieldExpr(last *syntax.ExprStmt, k *sink, end syntax.Pos) {
	if c.unreachableCall(last.X) {
		// Unreachable leaves the branch with a defect: no value, and it
		// joins any type, as return does (owner q29). No tail is recorded.
		c.exprStmt(last)
		return
	}
	use := useValue
	switch {
	case k.discard:
		use = useDiscard
	case k.optional && k.missing == nil:
		use = useOptional
	}
	op := c.exprAs(last.X, k.exprHint(), use)
	if op.invalid() {
		k.bad = true
		return
	}
	switch op.mode {
	case modeValue, modeConst, modeNoValue:
	default:
		c.needValue(&op)
		if op.invalid() {
			k.bad = true
			return
		}
	}
	// An unknown row is fallible (Row.unknown), so it goes where a failure
	// would.
	fallible := op.fallible()
	switch {
	case !fallible:
	case k.ret:
		c.escape(last.X, op.errs)
		op.errs = nil
	case k.pass != nil:
		k.pass(&op)
		op.errs = nil
	default:
		c.handled(&op)
	}
	if k.ret && op.mode == modeValue {
		if row := c.returnedErrorRow(op.typ); row != nil {
			// A failure, as return e is (rows.md, Error rows 4): this path
			// has no value.
			c.escape(last.X, row)
			c.tail(last, op.typ)
			return
		}
	}
	if k.discard {
		// The value is discarded: a pure value here is S110, a call's
		// result is dropped (one statement-position rule, review m2).
		c.unusedValue(last.X, op)
		if fallible {
			c.tail(last, NoValue)
		}
		return
	}
	if op.mode == modeNoValue {
		if fallible {
			// Its failures pass on from this tail (k.pass).
			c.tail(last, NoValue)
		}
		k.noValue = last.X
		k.miss(end)
		k.noValue = nil
		return
	}
	switch t := k.typ(); {
	case k.tuple != nil:
		c.returnTuple(last.X, op, k.tuple)
		c.tail(last, op.typ)
		k.valued = true
		return
	case t == nil:
		t = c.typeOf(&op)
		if isInvalid(t) {
			k.bad = true
			return
		}
		k.join = t
	default:
		if a, _ := c.assignability(op.typ, t); k.joins && k.want == nil && !a.ok() {
			// Variants of one union join to it (K3; rows.md, Totality
			// 3): the earlier branches' values go into it as variants, a
			// conversion the lowering does not write (ConvVariant).
			if u := c.unionJoin(t, op.typ); u != nil {
				k.join, t = u, u
			}
		}
		if !c.assign(&op, t, k.what) {
			k.bad = true
			return
		}
		if k.want == nil {
			// An unknown row in the join takes this branch's known part.
			k.join = c.widenUnknown(k.join, op.typ)
		}
	}
	k.valued = true
	c.tail(last, k.typ())
}

// ifYield checks an if whose branches yield to k. An if without else has no
// value: on a path that needs one it is noElse, or the enclosing body's
// missing value at end (D-C2).
func (c *checker) ifYield(s *syntax.IfStmt, k *sink, end syntax.Pos) {
	done := c.openScope()
	defer done()
	c.condition(s.Cond)
	inner := c.openScope()
	c.yieldList(s.Body.List, k, s.Body.Rbrace)
	inner()
	switch e := s.Else.(type) {
	case nil:
		switch {
		case k.discard:
		case k.optional && k.missing == nil:
			k.none = true
		case k.noElse != nil:
			k.noElse(s)
		default:
			k.miss(end)
		}
	case *syntax.BlockStmt:
		inner := c.openScope()
		c.yieldList(e.List, k, e.Rbrace)
		inner()
	case *syntax.IfStmt:
		c.ifYield(e, k, end)
	default:
		c.stmt(e)
	}
}

// matchYield checks a match whose arms yield to k; false when its header
// is not decided syntax (already reported). A match the solver cannot prove
// total is S103 (D-B4), so a value match never falls through.
func (c *checker) matchYield(s syntax.Stmt, k *sink) bool {
	var end syntax.Pos
	var ok bool
	arm := func(body []syntax.Stmt) { c.yieldList(body, k, end) }
	switch s := s.(type) {
	case *syntax.SwitchStmt:
		end = s.Rbrace
		ok = c.switchHeader(s, arm)
	case *syntax.TypeSwitchStmt:
		end = s.Rbrace
		ok = c.typeSwitchHeader(s, arm)
	}
	if ok && !k.discard && k.typ() != nil && c.silent == 0 {
		if m := c.pkg.Info.Matches[s]; m != nil {
			m.Result = k.typ()
		}
	}
	return ok
}

// branches checks an if or match in value position (D-C2): with an
// expected type every branch is assigned to it; without one the branches
// have one type. An if without else there is S108.
func (c *checker) branches(x syntax.Expr, hint Type) operand {
	// A fallible branch tail passes its failures to the expression, as a
	// with block's does: an enclosing catch or try handles them (review O2).
	var errs *Row
	k := &sink{want: hint, joins: true, what: "the branch's value", pass: func(op *operand) {
		errs = errs.union(c.types, op.errs)
	}}
	k.missing = func(end syntax.Pos) {
		c.errorIn(x, end, end+1, CodeMissingRet, "this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)")
	}
	k.noElse = func(s *syntax.IfStmt) {
		c.errorf(s, CodeType, "a value if needs an else: without one it has no value (D-C2)")
	}
	switch s := x.(type) {
	case *syntax.IfStmt:
		c.ifYield(s, k, s.Body.Rbrace)
	case *syntax.SwitchStmt:
		if !c.matchYield(s, k) {
			return bad(x)
		}
	case *syntax.TypeSwitchStmt:
		if !c.matchYield(s, k) {
			return bad(x)
		}
	}
	t := k.typ()
	switch {
	case k.bad && k.want == nil:
		// The inferred type is not every branch's (reported, D3).
		return bad(x)
	case t != nil:
		return operand{mode: modeValue, typ: t, errs: errs}
	case k.bad:
		return bad(x)
	}
	// Every branch leaves (return, break or continue): no value.
	return operand{mode: modeNoValue, typ: NoValue, errs: errs}
}
