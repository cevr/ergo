package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Decided forms this checker does not check yet (check-v1) are S109, each
// naming the stage that owns it, and nothing cascades from them (D3; round
// 6 review B1 and M2): an expression that holds the form is invalid.

// outside reports n as a decided form this checker does not check yet: S109
// with the noun that names its stage. It never panics.
func (c *checker) outside(n syntax.Node, noun string) operand {
	c.errorf(n, CodeOutside, "%s is not checked yet (check-v1)", noun)
	if x, ok := n.(syntax.Expr); ok {
		return bad(x)
	}
	return operand{mode: modeInvalid, typ: invalid}
}

// undecided reports a node, or a field of a node, that the decided parser
// never builds (syntax's TestDecidedOnly proves the list; spec D-A5): S109,
// never a panic, so a tree built by hand still gets a located diagnostic.
// The checker has no other branch for such a node.
func (c *checker) undecided(n syntax.Node, what string) operand {
	if what == "" {
		what = strings.TrimPrefix(fmt.Sprintf("%T", n), "*syntax.")
	}
	c.errorf(n, CodeOutside, "%s is not produced by the decided parser", what)
	if x, ok := n.(syntax.Expr); ok {
		return bad(x)
	}
	return operand{mode: modeInvalid, typ: invalid}
}

// patternNameTests is the one rule for a bare name in a pattern (rows.md,
// Totality 1; D-B3 rule 1; D-D3), shared by typing (pattern0, namePattern,
// package-level let patterns) and the reference walk (refWalker.patternTests), so the
// two give the identifier one meaning (fuzz-17d6). o is the object the name
// resolves to where the pattern stands, prelude whether the name is a
// prelude name, and letFor whether the pattern is a let's or a for's. In a
// let or a for, a prelude name binds (D-D3 refuses the binder and it still
// binds). Otherwise a constant, a type or a capability of the row tests the
// value, and the name keeps that meaning even when the test is refused. Any
// other name binds.
func patternNameTests(o Object, prelude, letFor bool) bool {
	if letFor && prelude {
		return false
	}
	switch o.(type) {
	case *Const, *TypeName, *capBinding:
		return true
	}
	return false
}

// patternTests is patternNameTests for a name at the point c checks. A
// refused binder is invisible to it: the name classifies as it would
// without the binder (P2, lookupPast).
func (c *checker) patternTests(name string, letFor bool) bool {
	return patternNameTests(c.lookupPast(name), c.isPreludeName(name), letFor)
}

// patternNames returns the names a pattern binds, in source order: a name
// that does not test (tests; patternNameTests), the names inside a record
// pattern's values, a tuple's elements and an as pattern's name.
func patternNames(p syntax.Expr, tests func(string) bool) []*syntax.Ident {
	var out []*syntax.Ident
	var walk func(syntax.Expr)
	walk = func(e syntax.Expr) {
		switch e := e.(type) {
		case *syntax.Ident:
			if e.Name != "_" && !tests(e.Name) {
				out = append(out, e)
			}
		case *syntax.ParenExpr:
			walk(e.X)
		case *syntax.TupleExpr:
			for _, x := range e.Elts {
				walk(x)
			}
		case *syntax.CompositeLit:
			for _, x := range e.Elts {
				if kv, ok := x.(*syntax.KeyValueExpr); ok {
					walk(kv.Value)
					continue
				}
				walk(x)
			}
		case *syntax.AsPattern:
			walk(e.X)
			if e.Name.Name != "_" {
				out = append(out, e.Name)
			}
		}
	}
	walk(p)
	return out
}

// ---------------------------------------------------------------- irrefutable patterns

// patternSize counts the syntax that is written, including field keys and
// type names. It never walks a type's unwritten fields or an arm's body.
func patternSize(p syntax.Expr) int {
	n := 0
	syntax.Inspect(p, func(node syntax.Node) bool {
		if node != nil {
			n++
		}
		return true
	})
	return n
}

// irrefutable resolves the pattern of a let or a for binding against a
// value of type t (spec D-B5; 196v amended rule 4): a pattern that does
// not cover t is S103 naming one missing case, and its names still bind
// (D-D3's cascade rule). It returns the bindings, each with its type
// (invalid after an error in the pattern), for the caller to declare.
func (c *checker) irrefutable(p syntax.Expr, t Type, what string, own map[*syntax.Ident]bool) []patBinding {
	r := c.newPatResolver()
	r.sp.written = 1 + patternSize(p)
	r.bindings = true
	r.letFor = true
	r.own = own
	r.begin()
	if isInvalid(t) {
		r.bindNames(p)
		return r.binds
	}
	col := r.subject(t)
	res := r.pattern(p, t, col)
	switch {
	case r.resolve.over:
		c.errorf(p, CodeNotTotal, "too many cases to prove this %s pattern irrefutable; split it (rows.md, Totality 6)", what)
	case r.bad:
	default:
		row := solverRow{}
		if res.has {
			row = r.sp.flat(solverRow{{col, res.pat}})
		}
		s := r.sp.solverFor()
		w, missing, ok := s.total([]solverRow{row})
		if c.silent == 0 {
			c.totalitySteps = append(c.totalitySteps, s.steps)
			c.totalityBudgets = append(c.totalityBudgets, s.budget)
			c.usefulSteps = append(c.usefulSteps, 0)
			c.patternCosts = append(c.patternCosts, patternCost{r.sp.written, s.budget, s.meter.cellBudget, s.steps, 0, s.cells, 0})
		}
		switch {
		case !ok:
			c.errorf(p, CodeNotTotal, "too many cases to prove this %s pattern irrefutable; split it (rows.md, Totality 6)", what)
		case missing:
			code := CodeNotTotal
			if _, single := p.(*syntax.Ident); single && what == "let" && res.info != nil && res.info.Kind == PatConst {
				code = CodeType // a bare prelude constant cannot bind (D-D2)
			}
			c.errorf(p, code, "this %s pattern does not match %s, a value of %s: a %s pattern matches every value; use match (rows.md, Totality 6)", what, r.sp.witnessText(w, col), t, what)
		}
	}
	if r.bad {
		for k := range r.binds {
			r.binds[k].t = invalid
		}
	}
	return r.binds
}

// declareBindings declares a let or for pattern's bindings as locals of
// the current block; an unread one is S110 (D-B3 rule 7).
func (c *checker) declareBindings(binds []patBinding) []*Var {
	var out []*Var
	for _, b := range binds {
		if b.placeholder {
			c.declarePlaceholder(b.id)
			continue
		}
		// A binder of an invalid value, or of a pattern with an error
		// elsewhere, still meets D-D2, D-D3 and the duplicate check: they
		// do not depend on its type. A binder the pattern itself refused
		// is recorded there (refuseBinder) and adds nothing.
		v := c.declareLocal(b.id, b.t)
		if v != nil {
			b.info.Var = v
			out = append(out, v)
		}
	}
	return out
}

// letPattern checks a local `let P = e` (spec D-B5): the value, then the
// pattern against its type, written or its own.
func (c *checker) letPattern(spec *syntax.VarSpec) {
	t := c.letPatternValue(spec, c.localTypeCtx())
	c.declareBindings(c.irrefutable(spec.Pattern, t, "let", nil))
}

// letPatternValue checks the value of a let with a pattern and returns the
// type the pattern matches: the written type, or the value's own.
func (c *checker) letPatternValue(spec *syntax.VarSpec, ctx *typeCtx) Type {
	var declared Type
	if spec.Type != nil {
		declared = c.typExpr(spec.Type, ctx)
	}
	switch len(spec.Values) {
	case 0:
		c.errorf(spec, CodeType, "a let with a pattern needs a value: let P = e (grammar.md, Statements 1)")
		return invalid
	case 1:
	default:
		c.undecided(spec, "a let with several values")
		return invalid
	}
	op := c.valueExpr(spec.Values[0], declared)
	if declared != nil {
		if isInvalid(declared) || !c.assign(&op, declared, "let") {
			return invalid
		}
		return declared
	}
	return c.typeOf(&op)
}

// pkgPatternCheck checks a package-level let that binds several names from
// one value, once for all its names: its pattern, or `let q, r = e` over a
// tuple (196t), and gives each name its type.
func (c *checker) pkgPatternCheck(g *pkgPattern) {
	spec := g.spec
	byName := map[*syntax.Ident]*pkgVar{}
	own := map[*syntax.Ident]bool{}
	for _, pv := range g.members {
		if pv.name != nil {
			byName[pv.name] = pv
			own[pv.name] = true
		}
	}
	setAll := func(t Type) {
		for _, pv := range g.members {
			pv.obj.Type = t
		}
	}
	if spec.Pattern == nil {
		// `let q, r = e`: the names destructure one tuple.
		var declared Type
		if spec.Type != nil {
			declared = c.typExpr(spec.Type, nil)
			if c.taskRefused(spec.Type, declared) {
				declared = invalid
			}
		}
		types := c.multiValue(spec.Values[0], len(spec.Names))
		if declared != nil {
			c.assignElems(spec.Values[0], types, declared)
		}
		for i, pv := range g.members {
			t := types[i]
			if declared != nil && !isInvalid(t) {
				t = declared
			}
			pv.obj.Type = t
		}
		return
	}
	setAll(invalid)
	t := c.letPatternValue(spec, nil)
	for _, b := range c.irrefutable(spec.Pattern, t, "let", own) {
		if pv := byName[b.id]; pv != nil {
			pv.obj.Type = b.t
			b.info.Var = pv.obj
		}
	}
}

// forPattern binds a for header's pattern against the element (or
// position) type t; its bindings join the loop's (Info.Loops).
func (c *checker) forPattern(p syntax.Expr, t Type, l *Loop) {
	for _, v := range c.declareBindings(c.irrefutable(p, t, "for", nil)) {
		l.Bindings = append(l.Bindings, v)
	}
}
