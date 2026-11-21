package lower

// eraseForms (IR-FORMS IRF-0; ir_forms.go): every form of a body back to
// the kinds the reader takes, before the body's end checks (ir_end.go) and
// the IR file see it.

import "fmt"

// eraseForms erases every form in list, nested bodies and function
// literals included, in place, and returns list; seen, when set, is called
// with each form's kind and layout.
func eraseForms(list []*irStmt, seen func(kind, layout string)) []*irStmt {
	e := eraser{seen}
	e.stmts(list)
	return list
}

type eraser struct{ seen func(kind, layout string) }

func (e eraser) stmts(list []*irStmt) {
	for _, s := range list {
		e.stmt(s)
	}
}

func (e eraser) stmt(s *irStmt) {
	if s == nil {
		return
	}
	if s.K == "match" {
		if why := verifyMatch(s); why != "" {
			panic("ergo: IR form: " + why)
		}
		if e.seen != nil {
			e.seen("match", s.F.Layout)
		}
		s.K, s.F, s.Was = s.F.Kind, nil, s.F
	}
	s.X = e.expr(s.X)
	s.Y = e.expr(s.Y)
	e.stmts(s.Then)
	e.stmts(s.Else)
	e.stmts(s.Body)
	for _, c := range s.Cases {
		for i, v := range c.Vals {
			c.Vals[i] = e.expr(v)
		}
		e.stmts(c.Body)
	}
}

// expr is x with every form in it erased.
func (e eraser) expr(x *irExpr) *irExpr {
	if x == nil {
		return nil
	}
	x.X = e.expr(x.X)
	x.Y = e.expr(x.Y)
	for i, a := range x.Args {
		x.Args[i] = e.expr(a)
	}
	for i, el := range x.Elems {
		x.Elems[i] = e.expr(el)
	}
	e.stmts(x.Body)
	x.C = nil // a call's facts (ir_merge.go)
	if x.K == "variant" {
		if e.seen != nil && x.F != nil {
			e.seen("variant", x.F.Layout)
		}
		was := x.F
		v := eraseVariant(x)
		v.Was = was
		return v
	}
	return x
}

// eraseVariant is the variant form x as the reader takes it: a flat
// union's of method on its zero value, or Go's conversion to the sealed
// interface. x's value is erased already.
func eraseVariant(x *irExpr) *irExpr {
	if why := verifyVariant(x); why != "" {
		panic("ergo: IR form: " + why)
	}
	if x.F.Layout == layoutFlat {
		return &irExpr{K: "mcall", Pos: x.Pos, Name: x.F.Of, X: &irExpr{K: "zero", Pos: x.Pos, T: x.T}, Args: []*irExpr{x.X}}
	}
	return &irExpr{K: "toiface", Pos: x.Pos, T: x.T, X: x.X}
}

// irNoForms reports the first form left in f's bodies, as an error: the
// IR file holds none (TestIRFileHoldsNoForm).
func irNoForms(f *irFile) error {
	var found string
	var stmts func([]*irStmt)
	var expr func(*irExpr)
	expr = func(x *irExpr) {
		if x == nil || found != "" {
			return
		}
		if x.F != nil || x.K == "variant" {
			found = "variant"
			return
		}
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
		stmts(x.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			if found != "" {
				return
			}
			if s.F != nil || s.K == "match" {
				found = "match"
				return
			}
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for _, v := range c.Vals {
					expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	for i := range f.Funcs {
		stmts(f.Funcs[i].Body)
		if found != "" {
			return fmt.Errorf("ergo: the IR form %s reached the IR file in %s", found, f.Funcs[i].Name)
		}
	}
	return nil
}
