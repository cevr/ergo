package check

import "ergo.local/prototype/internal/sketch/syntax"

// A law parameter's where over earlier parameters (CONSTRAINTS C5;
// types.md, Laws 15). A where on a law parameter may name the earlier
// parameters and the operations of the law's type: `b K where Equal(a,
// b)`. Such a where is not a refinement (refine.go, a set of values of one
// type); it is an admission: a generated case runs only when every
// admission holds. The checker keeps each as a law of its own (Law.Admit),
// over the parameters up to the one it qualifies, whose body is the
// where, so the test build lowers it as it lowers a law and the run asks
// it before the case.
//
// A parameter whose where names an earlier parameter of an identical type
// is found by search, not by rejection (Law.Near): the run lists that
// earlier value's near variants and takes the first that the where admits
// and that is not identical to the earlier value (runtime.md, Tests 6).

// relationalWhere reports whether where names an earlier parameter of law
// or an operation of the law's own type.
func (c *checker) relationalWhere(law *Law, where syntax.Expr) bool {
	names := map[string]bool{}
	for _, p := range law.Params[:len(law.Params)-1] {
		names[p.Name] = true
	}
	if law.Owner != nil {
		if it, ok := under(law.Owner.Type).(*Interface); ok {
			for _, m := range it.Methods {
				names[m.Name] = true
			}
		}
	}
	found := false
	syntax.Inspect(where, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && names[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// admission checks the where of law's parameter i, bound with every
// parameter before it in the law's frame, as a law body (a bool), and
// keeps it as the parameter's admission law.
func (c *checker) admission(law *Law, d *syntax.LawDecl, i int, where syntax.Expr) {
	body := &syntax.BlockStmt{Lbrace: where.Pos(), List: []syntax.Stmt{&syntax.ExprStmt{X: where}}, Rbrace: where.End()}
	c.lawBody(body)
	ad := *d
	ad.Body = body
	ad.Rewrite, ad.Equation = nil, nil
	p := law.Params[i]
	adm := &Law{
		Name:    law.Name + "Where" + p.Name,
		Decl:    &ad,
		Owner:   law.Owner,
		Pkg:     law.Pkg,
		Machine: law.Machine,
		Params:  append([]*Var(nil), law.Params[:i+1]...),
		Fails:   newRow(),
		Admits:  law,
		file:    law.file,
	}
	// Its slots are set with the law's (planLaw).
	for len(law.Admit) <= i {
		law.Admit = append(law.Admit, nil)
		law.Near = append(law.Near, -1)
	}
	law.Admit[i] = adm
	// The near source: the last earlier parameter of p's type that the
	// where names.
	syntax.Inspect(where, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		for j, q := range law.Params[:i] {
			if q.Name == id.Name && q.Name != "_" && c.identical(q.Type, p.Type) && j > law.Near[i] {
				law.Near[i] = j
			}
		}
		return true
	})
}
