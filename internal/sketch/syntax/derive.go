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

// IsProviderPlan reports a derive expression that is a direct call of the
// plan constructor Provider.
func IsProviderPlan(x Expr) bool {
	c, ok := x.(*CallExpr)
	if !ok {
		return false
	}
	id, ok := c.Fun.(*Ident)
	return ok && id.Name == "Provider"
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
	c := d.Derive.(*CallExpr)
	var types []Expr
	for _, a := range c.Args {
		t, ok := ofPlanType(a)
		if !ok {
			p.errorAt(d.DerivePos, len("derive"), "Provider takes three Of plans: the capability, the field type and the type of no arguments")
			return
		}
		types = append(types, t)
	}
	if len(types) != 3 {
		p.errorAt(d.DerivePos, len("derive"), "Provider takes three Of plans: the capability, the field type and the type of no arguments")
		return
	}
	var params []*Ident
	if d.TypeParams != nil {
		for _, f := range d.TypeParams.List {
			params = append(params, f.Names...)
		}
	}
	cap, ok := types[0].(*Ident)
	if len(params) != 1 || !ok || cap.Name != params[0].Name {
		p.errorAt(d.DerivePos, len("derive"), "a Provider derives %s from the operations of its one type parameter: write type %s[S] derive Provider(Of[S](), …)", d.Name.Name, d.Name.Name)
		return
	}
	d.Provider = &ProviderPlan{Field: types[1], None: types[2]}
}

// ofPlanType is X of the plan `Of[X]()`.
func ofPlanType(x Expr) (Expr, bool) {
	c, ok := x.(*CallExpr)
	if !ok || len(c.Args) != 0 {
		return nil, false
	}
	ix, ok := c.Fun.(*IndexExpr)
	if !ok || len(ix.Indices) != 1 {
		return nil, false
	}
	id, ok := ix.X.(*Ident)
	if !ok || id.Name != "Of" {
		return nil, false
	}
	return ix.Indices[0], true
}
