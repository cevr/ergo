package syntax

// The Go parser's methods for refinements, codec pairs, or-patterns and
// derived types: the test-only reference of capstone S2d, deleted in S2e.

// atWhere reports the contextual word `where`, which is no keyword: a
// name `where` stays a name everywhere else.
func (p *parser) atWhere() bool { return p.tok.tok == IDENT && p.tok.lit == "where" }

// parseRefinement parses `where P` after the type of d, which starts at the
// token typeStart, and derives the parse function and the law into
// p.derived. A generic type derives nothing: the checker refuses its where.
func (p *parser) parseRefinement(d *TypeDecl, typeStart int) {
	d.WherePos = p.tok.pos
	p.next()
	predStart := p.i
	d.Where = p.parseExpr()
	if d.TypeParams != nil {
		return
	}
	again := func(at int, parse func() Expr) Expr {
		saved := p.i
		p.backtrack(at)
		x := parse()
		p.backtrack(saved)
		return x
	}
	base := func() Expr { return again(typeStart, func() Expr { return p.parseType(ctxDecl) }) }
	pred := func() Expr { return again(predStart, p.parseExpr) }
	text := tokenText(p.src, p.toks, predStart, p.i)
	p.derived = deriveRefinement(d, base, pred, text)
}

// again parses the type expression t once more from its own tokens, so
// each node stands once in the file.
func (p *parser) again(t Expr) Expr {
	for k, tok := range p.toks {
		if tok.pos == t.Pos() {
			saved := p.i
			p.backtrack(k)
			out := p.parseType(ctxDecl)
			p.backtrack(saved)
			return out
		}
	}
	return t
}

// parseAlternatives parses `Pattern { "|" Pattern }` where a nested
// pattern stands. Outside a case's patterns a | is S001.
func (p *parser) parseAlternatives() Expr {
	first := p.parsePattern()
	if p.tok.tok != OR {
		return first
	}
	if !p.orAllowed {
		p.errorf("alternatives with | stand only in a case's patterns; a let or for pattern matches one shape")
		return first
	}
	or := &OrPattern{Alts: []Expr{first}}
	for p.tok.tok == OR {
		or.Bars = append(or.Bars, p.tok.pos)
		p.next()
		or.Alts = append(or.Alts, p.parsePattern())
	}
	return or
}

// expandCase is a case's patterns and, when one holds an OrPattern, the
// patterns as written: the first result is then the expansion.
func (p *parser) expandCase(list []Expr) ([]Expr, []Expr) {
	has := false
	for _, x := range list {
		if holdsOr(x) {
			has = true
			break
		}
	}
	if !has {
		return list, nil
	}
	var out []Expr
	for _, x := range list {
		out = append(out, expandPattern(x)...)
	}
	if len(out) > maxAlternatives {
		p.errorAt(list[0].Pos(), int(list[len(list)-1].End()-list[0].Pos()), "this case expands to %d patterns, more than %d: split it into several cases", len(out), maxAlternatives)
	}
	return out, list
}

// atDerive reports the contextual word `derive` before an expression on
// its line: the scanner ends a line after a name with a semicolon, so a
// line break after `derive` leaves it a type name.
func (p *parser) atDerive() bool {
	if p.tok.tok != IDENT || p.tok.lit != "derive" {
		return false
	}
	switch p.peek(1).tok {
	case IDENT, INT, FLOAT, STRING, RUNE, LPAREN, NOT, SUB, ADD:
		return true
	}
	return false
}

// parseDerive parses `derive E` after the name (and any type parameters)
// of d. A derived type has no `where`, and no type parameters unless E is
// a call of the plan constructor Provider (patterns.md, Provider plans),
// whose shape follows its type argument; each refusal is S001 at `derive`.
func (p *parser) parseDerive(d *TypeDecl) {
	d.DerivePos = p.tok.pos
	p.next()
	d.Derive = p.parseExpr()
	if IsProviderPlan(d.Derive) {
		p.providerPlan(d)
	} else if d.TypeParams != nil {
		p.errorAt(d.DerivePos, len("derive"), "a derived type has no type parameters: the plan fixes its shape (a Provider plan excepted)")
	}
	if p.atWhere() {
		p.errorAt(d.DerivePos, len("derive"), "derive does not combine with where: write the span in the plan, Refine(Of[B](), lo, hi)")
	}
	if p.tok.tok == LBRACE {
		p.errorAt(d.DerivePos, len("derive"), "derive does not combine with a body: the plan gives the type its fields")
	}
}

// providerPlan reads the direct plan `Provider(Of[S](), Of[F](), Of[N]())`
// of d (patterns.md, Provider plans 1). Each argument is syntax, so the
// parser, not the derive evaluation, gives d its body, a record with no
// fields of its own, and its plan: every package that parses d, std's
// included, has the same declaration with no expansion. The checker builds
// each instance from its capability's operations (check/script.go). A
// misshapen plan is S001 at derive, and d stays an empty record.
func (p *parser) providerPlan(d *TypeDecl) {
	d.Spec = &StructType{Fields: &FieldList{Opening: d.DerivePos, Closing: d.DerivePos}}
	if _, _, why := ProviderPlanOf(d); why != "" {
		p.errorAt(d.DerivePos, len("derive"), "%s", why)
	}
}
