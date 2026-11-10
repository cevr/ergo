package check

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A pattern partitions a consumed value into disjoint owners. Testing a
// subject does not consume it. A whole binding covers all of its parts;
// otherwise every resource part must be bound. R39 keeps a record's own
// Close together with its fields, so such a record can only bind whole.
type patternPart struct {
	v    *Var
	path []int
}

func (w *ownWalk) patternParts(p *Pattern, t Type, path []int) []patternPart {
	if p == nil {
		return nil
	}
	var out []patternPart
	if p.Var != nil {
		out = append(out, patternPart{p.Var, slices.Clone(path)})
	}
	for i, e := range p.Elems {
		et := t
		ep := path
		if p.Kind == PatTuple {
			if tup, ok := under(t).(*Tuple); ok && i < len(tup.Types) {
				et, ep = tup.Types[i], append(slices.Clone(path), i)
			}
		}
		out = append(out, w.patternParts(e, et, ep)...)
	}
	if st, ok := under(p.Type).(*Struct); ok {
		for _, f := range p.Fields {
			for i, sf := range st.Fields {
				if sf.Name == f.Name {
					out = append(out, w.patternParts(f.Pattern, sf.Type, append(slices.Clone(path), i))...)
				}
			}
		}
	}
	return out
}

func (w *ownWalk) resourceParts(parts []patternPart) []patternPart {
	return slices.DeleteFunc(slices.Clone(parts), func(p patternPart) bool { return !w.tracked(p.v.Type) })
}

func (w *ownWalk) patternRefuse(t Type, at syntax.Node, format string, args ...any) {
	if w.genericOnly(t) {
		w.copySite(t, at, fmt.Sprintf(format, args...), false)
		return
	}
	w.report(at, format, args...)
}

// patternCover validates only consuming patterns. Lent patterns may omit
// parts: their original owner still releases the whole subject.
func (w *ownWalk) patternCover(p *Pattern, t Type, at syntax.Node) {
	if p == nil || !w.tracked(t) {
		return
	}
	if p.Var != nil && w.tracked(p.Var.Type) {
		if p.Kind == PatAs && len(w.resourceParts(w.patternParts(p.Elems[0], t, nil))) > 0 {
			w.patternRefuse(t, at, "this pattern binds a resource both whole and in a part; a resource has exactly one owner (resources R39)")
		}
		return
	}
	switch p.Kind {
	case PatAs:
		w.patternCover(p.Elems[0], t, at)
		return
	case PatTuple:
		if tup, ok := under(t).(*Tuple); ok {
			for i, e := range p.Elems {
				if i < len(tup.Types) {
					w.patternCover(e, tup.Types[i], at)
				}
			}
			return
		}
	case PatRecord:
		if st, ok := under(p.Type).(*Struct); ok {
			d, _ := w.c.dropOf(p.Type)
			if d != nil && d.Close != nil {
				w.report(at, "this pattern splits %s, which has its own Close; bind it whole so that its release and fields keep one owner (resources R39)", p.Type)
				return
			}
			for _, f := range st.Fields {
				var fp *Pattern
				for _, pf := range p.Fields {
					if pf.Name == f.Name {
						fp = pf.Pattern
					}
				}
				if fp == nil && w.tracked(f.Type) {
					w.patternRefuse(f.Type, at, "this pattern omits resource field %s; bind every resource part to exactly one name (resources R39, R42)", f.Name)
				} else {
					w.patternCover(fp, f.Type, at)
				}
			}
			return
		}
	}
	w.patternRefuse(t, at, "this pattern leaves %s without an owner; bind every resource part to exactly one name (resources R39, R42)", t)
}

func (w *ownWalk) letOwners(spec *syntax.VarSpec) {
	if len(spec.Values) != 1 {
		return
	}
	x := spec.Values[0]
	p := w.info.Patterns[spec.Pattern]
	parts := w.patternParts(p, w.info.Types[x], nil)
	w.patternCover(p, w.info.Types[x], spec.Pattern)
	var loans []resourceLoan
	w.region(spec, func() {
		w.expr(x, mMove, hand{at: x, how: "let pattern moves it"})
		loans = w.valueLoans(x)
	})
	loans = mergeLoans(loans, w.patternTypeLoans(w.info.Types[x], parts))
	w.acquireParts(parts, ownLocal{}, loans, spec.Pattern)
}

// patternTypeLoans is the structural contract of fields split by a
// pattern, including nested records/tuples hidden behind a constructor.
func (w *ownWalk) patternTypeLoans(t Type, parts []patternPart) []resourceLoan {
	return w.patternTypeLoans0(t, nil, parts, map[Type]bool{})
}

func (w *ownWalk) patternTypeLoans0(t Type, path []int, parts []patternPart, active map[Type]bool) []resourceLoan {
	w.step(1)
	split := false
	for _, p := range parts {
		split = split || len(p.path) > len(path) && pathPrefix(path, p.path)
	}
	if !split {
		return nil
	} // whole bindings keep their own structural contract
	if active[t] {
		return nil
	}
	active[t] = true
	defer delete(active, t)
	var fields []Type
	switch t := under(t).(type) {
	case *Struct:
		for _, f := range t.Fields {
			fields = append(fields, f.Type)
		}
	case *Tuple:
		fields = t.Types
	}
	var out []resourceLoan
	for i, f := range fields {
		keeper := append(slices.Clone(path), i)
		out = mergeLoans(out, w.patternTypeLoans0(f, keeper, parts, active))
		_, callable := under(f).(*Signature)
		if w.c.running(f) == nil && !callable {
			continue
		}
		for j, root := range fields[:i] {
			if w.tracked(root) {
				out = append(out, resourceLoan{internal: true, keeper: keeper, end: lendPoint{path: append(slices.Clone(path), j)}})
			}
		}
	}
	return out
}

// acquireParts also transports internal aggregate loans to their new
// roots. A split cannot erase a task's lend or reverse its release order.
func (w *ownWalk) acquireParts(parts []patternPart, local ownLocal, loans []resourceLoan, at syntax.Node) {
	for _, p := range parts {
		_, callable := under(p.v.Type).(*Signature)
		if !w.tracked(p.v.Type) && !callable {
			continue
		}
		l := local
		l.v, l.loanOnly = p.v, !w.tracked(p.v.Type)
		i := w.acquire(l)
		if !w.st.reached {
			continue
		}
		for _, loan := range loans {
			if len(loan.keeper) > 0 && !pathPrefix(p.path, loan.keeper) {
				continue
			}
			if loan.internal {
				if pathPrefix(p.path, loan.end.path) {
					loan.end.path = slices.Clone(loan.end.path[len(p.path):])
					loan.keeper = slices.Clone(loan.keeper[len(p.path):])
					loan.root = p.v
				} else {
					for _, root := range parts {
						if pathPrefix(root.path, loan.end.path) {
							loan.root, loan.internal = root.v, false
							loan.end = w.lending.points[root.v]
							loan.keeper = nil
							if !w.releasesBefore(w.lending.points[p.v], loan.end) {
								w.report(at, "this pattern puts a keeper before its lender %s; bind the lender first so the keeper releases first (resources R31, R39)", root.v.Name)
							}
							break
						}
					}
				}
			}
			w.st.slots[i].loans = mergeLoans(w.st.slots[i].loans, []resourceLoan{loan})
		}
	}
}

func pathPrefix(a, b []int) bool { return len(a) <= len(b) && slices.Equal(a, b[:len(a)]) }

func patternNarrowType(p *Pattern, t Type) Type {
	for p != nil && p.Kind == PatAs {
		p = p.Elems[0]
	}
	if p != nil && (p.Kind == PatRecord || p.Kind == PatTypeTest) {
		return p.Type
	}
	return t
}

// subjectOwner distinguishes a whole owned subject from a projection or
// incoming lend. A freshly produced value is already a statement temp.
func (w *ownWalk) subjectOwner(x syntax.Expr) (int, bool) {
	if _, i, ok := w.rootSlot(x); ok {
		return i, !w.vars[i].lent && w.vars[i].depth == w.depth
	}
	for i := len(w.vars) - 1; i >= 0; i-- {
		if w.vars[i].temp == unparen(x) {
			return i, true
		}
	}
	return -1, false
}

func (w *ownWalk) patternClauses(s *syntax.TypeSwitchStmt, subjects []syntax.Expr) {
	n := len(w.vars)
	entry := w.copyState(w.st)
	var out ownState
	for _, cl := range s.Clauses {
		w.st = w.copyState(entry)
		delete(w.info.PatternOwners, cl)
		w.open(cl)
		for j, subject := range subjects {
			var chosen []patternPart
			partType := w.info.Types[subject]
			si, owned := w.subjectOwner(subject)
			for k, item := range cl.List {
				p := w.info.Patterns[item]
				if len(subjects) > 1 && p != nil && p.Kind == PatTuple {
					p = p.Elems[j]
				}
				parts := w.patternParts(p, w.info.Types[subject], nil)
				if k == 0 {
					chosen = parts
					partType = patternNarrowType(p, partType)
				}
				if owned && len(w.resourceParts(parts)) > 0 {
					w.patternCover(p, w.info.Types[subject], item)
				}
			}
			resources := w.resourceParts(chosen)
			local := ownLocal{}
			loans := mergeLoans(w.valueLoans(subject), w.patternTypeLoans(partType, chosen))
			if len(resources) > 0 && owned && w.st.reached {
				source := w.vars[si]
				if source.v != nil {
					id, _, _ := w.rootSlot(subject)
					w.move(si, id, hand{at: subject, how: "the match pattern moves it"})
					delete(w.info.Handoffs, id) // transfer belongs to this arm, not the header
				} else {
					w.st.slots[si].st = ownMoved
				}
				loans = mergeLoans(loans, w.st.slots[si].loans)
				plan := w.info.PatternOwners[cl]
				plan.Sources = append(plan.Sources, Release{Var: source.v, Temp: source.temp})
				for _, p := range resources {
					plan.Bindings = append(plan.Bindings, p.v)
				}
				w.info.PatternOwners[cl] = plan
			} else {
				local.lent, local.from = true, exprText(subject)
				root, ri, ok := w.placeRoot(subject)
				if ok {
					local.root, local.param = root, w.vars[ri].param
				} else {
					end := lendPoint{scope: s, order: int(subject.Pos()), external: true}
					local.lendEnd = &end
				}
				// A callable projected from a lent aggregate still lends
				// the whole root; a pattern cannot erase that lifetime.
				for i := range loans {
					if !loans[i].internal {
						continue
					}
					loans[i].internal, loans[i].root = false, root
					if ok {
						loans[i].end = w.point(root, ri)
					} else {
						loans[i].end = *local.lendEnd
					}
				}
			}
			for i := range loans {
				if loans[i].root == nil && !loans[i].internal && len(chosen) > 0 {
					loans[i].root = chosen[0].v
				}
			}
			w.acquireParts(chosen, local, loans, cl)
		}
		w.stmts(cl.Body, false)
		w.close()
		out = w.join(out, w.st, n)
	}
	w.st = out
}
