package check

import "ergo.local/prototype/internal/sketch/syntax"

// Branch join (K3; rows.md, Totality 3). Without an expected type the
// branches of an if or match have one type, the first branch's, unless they
// are variants of one union: then the expression has that union's type.
// Open and Closed join to State; Some[int] and None join to Option[int];
// two branches of one variant keep the variant.
//
// Prior art: Rust, ReScript, Gleam and OCaml type a constructor as its
// enum's type, so branches always join; TypeScript joins to a union of the
// branch types. Ergo's records are types of their own (a record may be a
// variant of several unions), so the join is to the one declared union that
// lists every branch, and there is none when two unions do.

// unionJoin is the union the branch types cur and next join to, or nil:
// cur or next itself when it is a declared union that lists the other, else
// the one union, declared in the package of cur's record, that lists both.
// Two unions that list both are no join: the first branch's type stands,
// and the mismatch names the let to write.
func (c *checker) unionJoin(cur, next Type) Type {
	if cur == nil || next == nil || containsInvalid(cur) || containsInvalid(next) {
		return nil
	}
	if c.declaredUnion(next) && c.inUnion(cur, next) {
		return next
	}
	if c.declaredUnion(cur) || c.declaredUnion(next) {
		return nil
	}
	cands := c.unionsListing(cur, next)
	if len(cands) != 1 {
		return nil
	}
	return cands[0]
}

// typedByValue reports (S108) an assignment to a `let mut` name that its
// value typed as one variant, of another variant that joins with it to a
// union: the let gives the name its value's type, so the message names the
// let with the union written. It reports false, with nothing reported,
// for any other assignment.
func (c *checker) typedByValue(target syntax.Expr, op *operand, t Type) bool {
	id, ok := unparen(target).(*syntax.Ident)
	if !ok || op.invalid() || op.mode == modeNoValue || containsInvalid(op.typ) {
		return false
	}
	v, ok := c.lookup(id.Name).(*Var)
	if !ok || !v.mut || v.byValue == nil {
		return false
	}
	if a, _ := c.assignability(op.typ, t); a.ok() {
		return false
	}
	u := c.unionJoin(t, op.typ)
	if u == nil {
		return false
	}
	value := exprText(v.byValue)
	if value == "this expression" {
		value = "…"
	}
	c.errorf(op.expr, CodeType, "%s is typed %s by its value; write let mut %s %s = %s (rows.md, Totality 3)", id.Name, c.localTypeText(t), id.Name, c.localTypeText(u), value)
	op.mode = modeInvalid
	return true
}

// declaredUnion reports a named union, not the row value of a catch arm.
func (c *checker) declaredUnion(t Type) bool {
	_, named := t.(*Named)
	return named && asUnion(t) != nil
}

// inUnion reports whether v goes into the union u as one of its variants
// (Assignability clause 4).
func (c *checker) inUnion(v, u Type) bool {
	if _, isRow := v.(*Union); isRow {
		return false
	}
	a, _ := c.assignability(v, u)
	return a.clause == assignUnion
}

// unionsListing is every union, declared in the package of a's record,
// that lists both a and b as variants: a generic union's instance takes
// its type arguments from the branches (Some[int] gives Option[int]), and
// a parameter neither binds leaves that union out.
func (c *checker) unionsListing(a, b Type) []Type {
	an, ok := a.(*Named)
	if !ok || an.Obj == nil || an.Obj.Pkg == nil || asStruct(a) == nil {
		return nil
	}
	if _, ok := b.(*Named); !ok || asStruct(b) == nil {
		return nil
	}
	pkg := an.Obj.Pkg
	var out []Type
	for _, tn := range pkg.Types {
		un, ok := tn.Type.(*Named)
		if !ok || un.Orig != nil || pkg != c.pkg && !isExported(tn.Name) {
			continue
		}
		u, ok := un.under.(*Union)
		if !ok || u.Bad {
			continue
		}
		cand := Type(un)
		if len(un.TParams) > 0 {
			cand = c.joinInstance(un, u, a, b)
			if cand == nil {
				continue
			}
		}
		if c.inUnion(a, cand) && c.inUnion(b, cand) {
			out = append(out, cand)
		}
	}
	return out
}

// joinInstance is the instance of the generic union origin un whose
// variants the branch types ts are, its type arguments bound by the
// variants that name them, or nil when one stays free.
func (c *checker) joinInstance(un *Named, u *Union, ts ...Type) Type {
	set := map[*TypeParam]bool{}
	for _, tp := range un.TParams {
		set[tp] = true
	}
	inf := &inferer{c: c, m: map[*TypeParam]Type{}, set: set}
	for _, t := range ts {
		tn, _ := t.(*Named)
		for _, v := range u.Variants {
			if vn, ok := v.(*Named); ok && tn != nil && vn.origin() == tn.origin() {
				_ = inf.unify(v, t, unifyExact)
			}
		}
	}
	targs := make([]Type, len(un.TParams))
	for i, tp := range un.TParams {
		if inf.m[tp] == nil {
			return nil
		}
		targs[i] = inf.m[tp]
	}
	return c.instantiate(un, targs)
}
