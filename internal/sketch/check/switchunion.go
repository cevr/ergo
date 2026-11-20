package check

import "ergo.local/prototype/internal/sketch/syntax"

// switchUnion is a type switch subject's union as the switch reads it
// (round-6 M5; D16: nothing before the solvers' counters is superlinear).
// For an instance of a generic union (U[A]) it keeps the origin's union
// shape and the type arguments, and substitutes a variant only when a case
// or a binding needs it. A switch over n subjects of distinct
// instantiations of a k-variant union then does O(n + k) work before its
// counters, not n × k: a default-only switch instantiates no variant, and
// the origin's variant index is built once and shared by every instance.
type switchUnion struct {
	c    *checker
	orig *Union              // the origin's union, or the subject's own
	m    map[*TypeParam]Type // the instance's type arguments; nil for a non-instance
	inst map[int]Type        // the variants substituted so far
	tps  []*TypeParam        // the declaration's type parameters, in order (determinism)
	// bound indexes the instance's type arguments by interned id, and rows
	// its bound rows per index node by row id, each built once
	// (variantindex.go, intern.go).
	bound map[int32][]*TypeParam
	rows  map[*vnode]map[int32][]*vnode
}

// subjectUnion returns t's union as a switch subject, or nil when t is not
// a union.
//
// The result is shared by every subject of the same instance (or of the
// same plain union), so its caches (the substituted variants, the bound
// identities, the bound rows) are built once per instance, not once per
// switch subject (round-9 M1).
func (c *checker) subjectUnion(t Type) *switchUnion {
	if c.subjects == nil {
		c.subjects = map[any]*switchUnion{}
	}
	if n, ok := t.(*Named); ok && n.Orig != nil && n.under == nil {
		if ou, ok := n.Orig.under.(*Union); ok {
			// An instance whose id waits on an unbound row variable keeps
			// its caches for the component's inference only (class F).
			subjects := c.subjects
			if _, infer := c.types.intern(n); infer {
				subjects = c.inferSubjects
			}
			if s, ok := subjects[n]; ok {
				return s
			}
			s := &switchUnion{c: c, orig: ou, m: paramMap(n.Orig.TParams, n.TArgs), tps: n.Orig.TParams}
			subjects[n] = s
			return s
		}
	}
	if u := asUnion(t); u != nil {
		if s, ok := c.subjects[u]; ok {
			return s
		}
		s := &switchUnion{c: c, orig: u}
		c.subjects[u] = s
		return s
	}
	return nil
}

func (s *switchUnion) len() int  { return len(s.orig.Variants) }
func (s *switchUnion) bad() bool { return s.orig.Bad }

// at returns variant i, substituted on first use.
func (s *switchUnion) at(i int) Type {
	if s.m == nil {
		return s.orig.Variants[i]
	}
	if v, ok := s.inst[i]; ok {
		return v
	}
	if s.inst == nil {
		s.inst = map[int]Type{}
	}
	v := s.c.subst(s.orig.Variants[i], s.m)
	s.inst[i] = v
	return v
}

// variantOver is lookup's result when the switch's step counter, or the
// union's insertion counter, ran out (S103, rows.md Totality 5).
const variantOver = -3

// lookup returns the index of the variant t names: the variant identical
// to t, or, for a generic type written without type arguments, the first
// variant instantiated from it; -1 when none; variantOver past the budget.
// It follows t's path in the origin's variant index (round-7 M4, round 9
// class C): no variant is scanned or substituted to find it, only a
// wildcard candidate is checked, and every step is charged to b.
func (s *switchUnion) lookup(t Type, b *lookupSteps) int {
	n, ok := t.(*Named)
	if !ok {
		return -1
	}
	// The identity work of the lookup (intern.go) is charged to b.
	defer s.c.types.attach(b)()
	ix := s.c.unionIndex(s.orig)
	if ix.b.over {
		return variantOver
	}
	if n.Orig == nil && len(n.TParams) > 0 {
		if i, ok := ix.first[n]; ok {
			return i
		}
		return -1
	}
	cands, ok := s.candidates(ix, t, b)
	if !ok {
		return variantOver
	}
	for _, cand := range cands {
		if cand.exact {
			return cand.i
		}
		// A wildcard candidate is confirmed by substituting the variant
		// (charged by its size) and one identity check (the table charges
		// the nodes it interns).
		if !b.charge(s.c.types.size(s.orig.Variants[cand.i])) {
			return variantOver
		}
		same := s.c.identical(s.at(cand.i), t)
		if b.over {
			return variantOver
		}
		if same {
			return cand.i
		}
	}
	return -1
}

// bind gives a type parameter of the union's declaration the instance's
// type argument; outside an instance a type parameter stands for itself.
func (s *switchUnion) bind(tp *TypeParam) Type {
	if b, ok := s.m[tp]; ok {
		return b
	}
	return tp
}

// leftOutRowsVariant resolves a case written with its type arguments but
// with trailing row parameters left out, Retrying[T] of Async2[T, F]: in
// a case the variant's arguments come from the subject (Generics 6), so a
// left-out row is the subject's, not the empty row Generics 7 gives a
// type written where nothing is expected (ROW-PARAM AC1). It is the
// variant of the written type's origin whose leading arguments are the
// written ones; -1 when there is none.
func (c *checker) leftOutRowsVariant(e syntax.Expr, t Type, u *switchUnion, b *lookupSteps) int {
	ix, ok := unparen(e).(*syntax.IndexExpr)
	n, isNamed := t.(*Named)
	if !ok || !isNamed || n.Orig == nil || len(ix.Indices) >= len(n.TArgs) {
		return -1
	}
	i := u.lookup(n.origin(), b)
	if i < 0 {
		return i
	}
	v, ok := u.at(i).(*Named)
	if !ok || len(v.TArgs) != len(n.TArgs) {
		return -1
	}
	for k := range ix.Indices {
		if !c.identical(n.TArgs[k], v.TArgs[k]) {
			return -1
		}
	}
	return i
}

// variantSet is caseVariant's result for a declared union whose every
// variant is one of the subject's: its indexes come in the set.
const variantSet = -4

// caseVariant resolves a case type to its variant index in the union, -1
// for _ or the subject's own type, -2 on an error (reported), or variantOver when the switch's step
// counter ran out (reported once by the switch). It is a keyed lookup, not a scan:
// resolving the cases stays linear before the solvers' counters (round-5
// M2; D16 as amended in round 5). A declared union whose variants are all
// the subject's, `case A as a`, is variantSet with their indexes in
// A's order and the type A (types.md, Unions 1; DECL-VALUES Design B (b)).
func (c *checker) caseVariant(e syntax.Expr, u *switchUnion, subj Type, b *lookupSteps) (int, []int, Type) {
	if id, ok := e.(*syntax.Ident); ok && id.Name == "_" {
		return -1, nil, nil
	}
	t := c.typExpr0(e, c.localTypeCtx())
	if isInvalid(t) {
		return -2, nil, nil
	}
	if c.identical(t, subj) {
		return -1, nil, nil // The subject's own union type covers all its values.
	}
	if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		if sn, ok := subj.(*Named); ok && sn.origin() == n {
			return -1, nil, nil // A generic own type may omit its subject's arguments.
		}
	}
	if n, ok := t.(*Named); ok && (n.Orig != nil || len(n.TParams) == 0) {
		if su := asUnion(t); su != nil && su.Row == nil && len(su.Variants) > 0 {
			set := make([]int, 0, len(su.Variants))
			for _, v := range su.Variants {
				i := u.lookup(v, b)
				if i == variantOver {
					return variantOver, nil, nil
				}
				if i < 0 {
					if !u.bad() {
						c.errorf(e, CodeType, "%s holds %s, which is not a variant of %s (rows.md, Totality 1)", t, v, subj)
					}
					return -2, nil, nil
				}
				set = append(set, i)
			}
			return variantSet, set, t
		}
	}
	i := u.lookup(t, b)
	if i >= 0 || i == variantOver {
		return i, nil, nil
	}
	if j := c.leftOutRowsVariant(e, t, u, b); j >= 0 || j == variantOver {
		return j, nil, nil
	}
	if !u.bad() {
		what := t.String()
		if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
			what = n.Obj.Name
		}
		c.errorf(e, CodeType, "%s is not a variant of %s (rows.md, Totality 1)", what, subj)
	}
	return -2, nil, nil
}
