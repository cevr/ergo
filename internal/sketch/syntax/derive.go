package syntax

// Derived types (patterns.md, Derivation 1). `type T derive E` is a type
// declaration whose body is the plan expression E. `derive` is a
// contextual word, not a keyword: it has its meaning only after the name of
// a type declaration and before a token that starts an expression on the
// same line. Everywhere else it is a name, so `type T derive` at the end of
// a line declares T over a type named derive, and `type T derive.X` over a
// type of a package named derive. The parser stores E and derives nothing:
// a plan may call plan functions of other packages, so the checker expands
// it (internal/sketch/derive).

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
// of d. A derived type has no type parameters and no `where`; each is S001
// at `derive`.
func (p *parser) parseDerive(d *TypeDecl) {
	d.DerivePos = p.tok.pos
	if d.TypeParams != nil {
		p.errorAt(d.DerivePos, len("derive"), "a derived type has no type parameters: the plan fixes its shape")
	}
	p.next()
	d.Derive = p.parseExpr()
	if p.atWhere() {
		p.errorAt(d.DerivePos, len("derive"), "derive does not combine with where: write the span in the plan, Refine(Of[B](), lo, hi)")
	}
	if p.tok.tok == LBRACE {
		p.errorAt(d.DerivePos, len("derive"), "derive does not combine with a body: the plan gives the type its fields")
	}
}
