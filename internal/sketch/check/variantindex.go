package check

import (
	"sort"
)

// variantIndex finds a union's variant by instance identity (round-7 M4,
// round 9 class C; D16: nothing before the solvers' counters is
// superlinear). It is a discrimination tree over the variants' types,
// built once per origin union and check and shared by every instance of
// it: each variant is read in preorder as a path of type constructors (a
// named origin and its arity, a basic type, a slice, an array length, a
// signature's parameter and result counts, ...). Every edge is hashed:
//   - a constructor edge is keyed by the constructor;
//   - a variable edge, for a type parameter of the union's declaration, is
//     keyed by the parameter. A lookup reaches it through the instance's
//     bound identities: the parameters bound to the case's sub-term,
//     indexed once per instance by interned id (intern.go);
//   - a row-pattern edge, for a row that mentions a declaration parameter
//     (error[E] in func(A) error[E]), is keyed by the pattern's id. A
//     lookup reaches it through the bound row's id: the instance's rows,
//     indexed once per instance and node;
//   - a wildcard edge, for a type the tree does not key (a union, a
//     machine), confirmed by one identity check.
//
// The edges mirror identity (intern.go shape), so a leaf reached without a
// wildcard is the variant identical to the case. As a safety net, every
// operation of a case lookup, the identity work it causes included, is
// charged to the switch's step counter (lookupSteps) and every insertion to
// the union's own counter; past totalityBudget the switch is S103, as the
// solvers are (rows.md, Totality 5). So no uncounted pass runs before the
// solvers.
type variantIndex struct {
	root *vnode
	// first is the first variant of each generic origin, for a case that
	// names the generic type without type arguments.
	first map[*Named]int
	// b counts the insertion work, charged to the union; past
	// totalityBudget every lookup in the index is refused.
	b   lookupSteps
	tab *typeTable
}

type vnode struct {
	next    map[vsym]*vnode
	vars    map[*TypeParam]*vnode
	rows    map[int32]*vrow
	rowList []*vrow // the row patterns in insertion order (determinism)
	wild    *vnode
	leaves  []int
}

// vrow is a row-pattern edge: a row that mentions a declaration parameter.
type vrow struct {
	row  *Row
	node *vnode
}

// vsym is a type constructor: identical types have the same constructor
// and pairwise identical sub-terms (intern.go shape).
type vsym struct {
	kind byte
	ref  any    // the origin of a named type, the basic type
	n    int64  // arity, comparable, row id
	s    string // field or method names, counts and flags
}

const (
	vNamed byte = iota + 1
	vBasic
	vSlice
	vMap
	vPointer
	vTuple
	vSig
	vStruct
	vIface
	vRow
	vArray // n is the length (196y)
)

// lookupSteps is a switch's step counter for resolving its cases: every
// operation of the variant index and of the case lookups is charged to it,
// against totalityBudget. ops also feeds the tests' variantOps count.
type lookupSteps struct {
	steps int
	over  bool
	ops   *int
}

func (b *lookupSteps) charge(w int) bool {
	*b.ops += w
	if b.over {
		return false
	}
	b.steps += w
	if b.steps > totalityBudget {
		b.over = true
		return false
	}
	return true
}

// unionIndex returns the union's variant index, built on first use in this
// check. Its insertion work, identities included, is charged to the index's
// own counter (and to ops).
func (c *checker) unionIndex(u *Union) *variantIndex {
	if ix, ok := c.vindex[u]; ok {
		return ix
	}
	if c.vindex == nil {
		c.vindex = map[*Union]*variantIndex{}
	}
	ix := &variantIndex{root: &vnode{}, first: map[*Named]int{}, b: lookupSteps{ops: &c.variantOps}, tab: c.types}
	c.vindex[u] = ix
	defer c.types.attach(&ix.b)()
	for i, v := range u.Variants {
		vn, ok := v.(*Named)
		if !ok {
			continue
		}
		if _, seen := ix.first[vn.origin()]; !seen {
			ix.first[vn.origin()] = i
		}
		end := ix.insert(ix.root, v)
		if ix.b.over {
			return ix
		}
		end.leaves = append(end.leaves, i)
	}
	return ix
}

// insert adds t's path below n and returns the node it ends at.
func (ix *variantIndex) insert(n *vnode, t Type) *vnode {
	if !ix.b.charge(1) {
		return n
	}
	if tp, ok := t.(*TypeParam); ok {
		if n.vars == nil {
			n.vars = map[*TypeParam]*vnode{}
		}
		child := n.vars[tp]
		if child == nil {
			child = &vnode{}
			n.vars[tp] = child
		}
		return child
	}
	if ra, ok := t.(*RowArg); ok && ix.rowMentions(ra.Row) {
		key, _ := ix.tab.rowID(ra.Row)
		if n.rows == nil {
			n.rows = map[int32]*vrow{}
		}
		vr := n.rows[key]
		if vr == nil {
			vr = &vrow{row: ra.Row, node: &vnode{}}
			n.rows[key] = vr
			n.rowList = append(n.rowList, vr)
		}
		return vr.node
	}
	sym, kids, ok := ix.tab.symOf(t)
	if !ok {
		if n.wild == nil {
			n.wild = &vnode{}
		}
		return n.wild
	}
	if n.next == nil {
		n.next = map[vsym]*vnode{}
	}
	child := n.next[sym]
	if child == nil {
		child = &vnode{}
		n.next[sym] = child
	}
	for _, k := range kids {
		child = ix.insert(child, k)
		if ix.b.over {
			return child
		}
	}
	return child
}

// rowMentions reports whether a row mentions a declaration parameter. The
// test walks the row's elements: each visited node is charged to the
// union's insertion counter (round-9 M1).
func (ix *variantIndex) rowMentions(r *Row) bool {
	steps := 0
	mentions := rowHasParam(r, &steps)
	ix.b.charge(steps + len(r.Elems()))
	return mentions
}

// vcand is a variant a case may name: exact when its path took no
// wildcard, so it is identical to the case.
type vcand struct {
	i     int
	exact bool
}

// vterms is the pending sub-terms of a case, a persistent stack.
type vterms struct {
	t    Type
	next *vterms
}

// candidates returns the variants whose path matches t, in variant order,
// and false when the switch's step counter ran out. Every step is charged
// before it is taken: a node visit and its constructor's sub-terms; the
// identity work (each new node interned, each row operation) is charged by
// the table, which the lookup attached to b.
func (s *switchUnion) candidates(ix *variantIndex, t Type, b *lookupSteps) ([]vcand, bool) {
	var out []vcand
	var walk func(n *vnode, p *vterms, exact bool)
	walk = func(n *vnode, p *vterms, exact bool) {
		if !b.charge(1) {
			return
		}
		if p == nil {
			for _, i := range n.leaves {
				if !b.charge(1) {
					return
				}
				out = append(out, vcand{i, exact})
			}
			return
		}
		if len(n.vars) > 0 {
			tps, ok := s.boundTo(p.t, b)
			if !ok {
				return
			}
			for _, tp := range tps {
				// boundTo matched the ids: the bound type is the case's
				// sub-term.
				if child := n.vars[tp]; child != nil && s.c.identical(s.bind(tp), p.t) {
					walk(child, p.next, exact)
				}
			}
		}
		if len(n.rowList) > 0 {
			if ra, ok := p.t.(*RowArg); ok {
				idx, ok := s.rowsAt(n, b)
				if !ok {
					return
				}
				id, _ := s.c.types.rowID(ra.Row)
				if b.over {
					return
				}
				for _, child := range idx[id] {
					walk(child, p.next, exact)
				}
			}
		}
		if n.wild != nil {
			walk(n.wild, p.next, false)
		}
		if sym, kids, ok := s.c.types.symOf(p.t); ok {
			if !b.charge(len(kids)) {
				return
			}
			if child := n.next[sym]; child != nil {
				rest := p.next
				for k := len(kids) - 1; k >= 0; k-- {
					rest = &vterms{kids[k], rest}
				}
				walk(child, rest, exact)
			}
		}
	}
	walk(ix.root, &vterms{t, nil}, true)
	if b.over {
		return nil, false
	}
	if len(out) > 1 {
		tab := s.c.types
		sort.Slice(out, func(a, c int) bool {
			tab.sortCmps++
			b.charge(1)
			return out[a].i < out[c].i
		})
	}
	return out, true
}

// boundTo returns the declaration parameters whose bound type is t: those
// with t's id, from the instance's bound ids. They are indexed once per
// instance (the switchUnion is shared by every subject of the instance,
// subjectUnion). Outside an instance a parameter stands for itself. ok is
// false when the step counter ran out.
func (s *switchUnion) boundTo(t Type, b *lookupSteps) ([]*TypeParam, bool) {
	if s.m == nil {
		if tp, ok := t.(*TypeParam); ok {
			return []*TypeParam{tp}, true
		}
		return nil, true
	}
	tab := s.c.types
	bound := s.bound
	if bound == nil {
		// A subject whose instance waits on an unbound row variable lives
		// in the inference context only (subjectUnion), so the ids hold.
		bound = map[int32][]*TypeParam{}
		for _, tp := range s.tps {
			a, ok := s.m[tp]
			if !ok {
				continue
			}
			id := tab.id(a)
			if !b.charge(1) {
				return nil, false
			}
			bound[id] = append(bound[id], tp)
		}
		s.bound = bound
	}
	id := tab.id(t)
	if b.over {
		return nil, false
	}
	return bound[id], true
}

// rowsAt returns node n's row-pattern edges indexed by the ids of the rows
// the instance binds them to, built once per instance and node. ok is false
// when the step counter ran out.
func (s *switchUnion) rowsAt(n *vnode, b *lookupSteps) (map[int32][]*vnode, bool) {
	if idx, ok := s.rows[n]; ok {
		return idx, true
	}
	tab := s.c.types
	idx := map[int32][]*vnode{}
	for _, vr := range n.rowList {
		// Substituting the pattern builds a copy of it: its elements'
		// sizes. The bound types are shared, not copied: the instance
		// cache and the row key on their ids (intern.go), whose new nodes
		// the table charges.
		w := 1
		for _, e := range vr.row.Elems() {
			w += tab.size(e)
		}
		if !b.charge(w) {
			return nil, false
		}
		r := vr.row
		if s.m != nil {
			r = s.c.substRow(r, s.m)
		}
		k, _ := tab.rowID(r)
		if b.over {
			return nil, false
		}
		idx[k] = append(idx[k], vr.node)
	}
	if s.rows == nil {
		s.rows = map[*vnode]map[int32][]*vnode{}
	}
	s.rows[n] = idx
	return idx, true
}

// symOf returns t's constructor and sub-terms, or false for a type the
// tree does not key: a type parameter, a union, a machine. A signature's
// sub-terms are its parameters, its results and its two rows; a row is a
// constructor of its id, and a row that mentions a declaration parameter
// becomes a row-pattern edge when inserted.
func (tab *typeTable) symOf(t Type) (vsym, []Type, bool) {
	if ra, ok := t.(*RowArg); ok {
		id, _ := tab.rowID(ra.Row)
		return vsym{kind: vRow, n: int64(id)}, nil, true
	}
	kind, ref, n, s, kids := shape(t)
	if kind == vParam || kind == vFresh {
		return vsym{}, nil, false
	}
	return vsym{kind: kind, ref: ref, n: n, s: s}, kids, true
}

// rowHasParam reports whether a row element mentions a type parameter,
// adding each visited type node to steps (the union's insertion counter).
func rowHasParam(r *Row, steps *int) bool {
	for _, e := range r.Elems() {
		if hasParam(e, steps) {
			return true
		}
	}
	return false
}

// hasParam reports whether t mentions a type parameter, adding each visited
// type node to steps.
func hasParam(t Type, steps *int) bool {
	*steps++
	switch t := t.(type) {
	case *TypeParam:
		return true
	case *Named:
		for _, a := range t.TArgs {
			if hasParam(a, steps) {
				return true
			}
		}
	case *RowArg:
		return rowHasParam(t.Row, steps)
	case *Slice:
		return hasParam(t.Elem, steps)
	case *Array:
		return hasParam(t.Elem, steps)
	case *Map:
		return hasParam(t.Key, steps) || hasParam(t.Elem, steps)
	case *Pointer:
		return hasParam(t.Elem, steps)
	case *Tuple:
		for _, e := range t.Types {
			if hasParam(e, steps) {
				return true
			}
		}
	case *Struct:
		for _, f := range t.Fields {
			if hasParam(f.Type, steps) {
				return true
			}
		}
	case *Signature:
		for _, p := range t.Params {
			if hasParam(p.Type, steps) {
				return true
			}
		}
		for _, r := range t.Results {
			if hasParam(r, steps) {
				return true
			}
		}
		return rowHasParam(t.Errors, steps) || rowHasParam(t.Uses, steps)
	case *Interface:
		for _, m := range t.Methods {
			if m.Sig != nil && hasParam(m.Sig, steps) {
				return true
			}
		}
	case *Union:
		for _, v := range t.Variants {
			if hasParam(v, steps) {
				return true
			}
		}
	case *MachineType:
		return hasParam(t.State, steps) || hasParam(t.Signal, steps)
	}
	return false
}
