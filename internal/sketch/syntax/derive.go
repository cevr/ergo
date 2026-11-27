package syntax

// Derived types (patterns.md, Derivation 1). `type T derive E` is a type
// declaration whose body is the plan expression E. `derive` is a
// contextual word, not a keyword: it has its meaning only after the name of
// a type declaration and before a token that starts an expression on the
// same line. Everywhere else it is a name, so `type T derive` at the end of
// a line declares T over a type named derive, and `type T derive.X` over a
// type of a package named derive. The parser stores E and derives nothing:
// a plan may call functions of other packages, so the content-mapper pipeline
// expands non-Provider derives (internal/sketch/contentmap) and the checker
// expands Provider derives (script.go).

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

// ProviderPlanOf is the field type F and the type of no arguments N of the
// direct plan `Provider(Of[S](), Of[F](), Of[N]())` of d, as written, or why
// the plan is misshapen (patterns.md, Provider plans 1): not three Of plans,
// or not exactly one type parameter S, named by the first.
func ProviderPlanOf(d *TypeDecl) (field, none Expr, why string) {
	const shape = "Provider takes three Of plans: the capability, the field type and the type of no arguments"
	c, ok := d.Derive.(*CallExpr)
	if !ok || len(c.Args) != 3 {
		return nil, nil, shape
	}
	var types []Expr
	for _, a := range c.Args {
		t, ok := ofPlanType(a)
		if !ok {
			return nil, nil, shape
		}
		types = append(types, t)
	}
	var params []*Ident
	if d.TypeParams != nil {
		for _, f := range d.TypeParams.List {
			params = append(params, f.Names...)
		}
	}
	cap, ok := types[0].(*Ident)
	if len(params) != 1 || !ok || cap.Name != params[0].Name {
		return nil, nil, "a Provider derives " + d.Name.Name + " from the operations of its one type parameter: write type " + d.Name.Name + "[S] derive Provider(Of[S](), …)"
	}
	return types[1], types[2], ""
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
