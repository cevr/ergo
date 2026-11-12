package check

import "ergo.local/prototype/internal/sketch/syntax"

// The instantiation-cycle check (round-2 M1; go/types mono.go). Each
// instantiation G[targs] written or inferred inside a generic declaration D
// adds, for every type parameter Q of D that occurs in targs[i], an edge
// from Q to G's i-th parameter: weight 0 when targs[i] is Q itself, weight
// 1 when Q occurs inside a larger type ([]Q, Box[Q]). A cycle of positive
// weight instantiates D with ever larger types (f[T] calls f[[]T], which
// calls f[[][]T], …): the set of instances, and of the rows they carry, is
// infinite. Go rejects such a cycle ("instantiation cycle"); so does the
// checker (S108), before row inference iterates over the cycle and before
// the recursive-struct walk expands instances, so both walk finite sets.
//
// With weights 0 and 1 a cycle of positive weight exists exactly when a
// strongly connected component of the parameter graph holds a weight-1
// edge, which Tarjan's algorithm finds in linear time (Go uses a
// Bellman-Ford variant over the same graph).

type monoEdge struct {
	from, to *TypeParam
	weight   int
	at       syntax.Node
	file     *fileInfo
	arg      Type
	inst     string
}

type monoState struct {
	owner    []*TypeParam // the type parameters of the declaration being checked
	edges    []monoEdge
	seen     map[[2]*TypeParam]int // edge → index in edges, the heavier kept
	bad      map[*TypeParam]bool
	reported map[*TypeParam]bool
}

// monoOwner sets the generic declaration whose parameters instantiations
// are attributed to; the returned func restores the previous one.
func (c *checker) monoOwner(tps []*TypeParam) func() {
	prev := c.mono.owner
	c.mono.owner = tps
	return func() { c.mono.owner = prev }
}

// recordInstantiation adds the edges of one instantiation of name with
// tparams := targs at the position at.
func (c *checker) recordInstantiation(name string, tparams []*TypeParam, targs []Type, at syntax.Node) {
	// Every instantiation site comes here: R22 decides its bounds late
	// (resource.go, recordOwnership).
	c.recordOwnership(name, tparams, targs, at)
	if len(c.mono.owner) == 0 || at == nil {
		return
	}
	for i, p := range tparams {
		if i >= len(targs) || p.Kind != KindType || targs[i] == nil {
			continue
		}
		arg := targs[i]
		for _, q := range c.mono.owner {
			if q.Kind != KindType || !occurs(q, arg) {
				continue
			}
			w := 1
			if tp, ok := arg.(*TypeParam); ok && tp == q {
				w = 0
			}
			key := [2]*TypeParam{q, p}
			if c.mono.seen == nil {
				c.mono.seen = map[[2]*TypeParam]int{}
			}
			if j, ok := c.mono.seen[key]; ok {
				if c.mono.edges[j].weight >= w {
					continue
				}
				c.mono.edges[j] = monoEdge{q, p, w, at, c.cur, arg, name}
				continue
			}
			c.mono.seen[key] = len(c.mono.edges)
			c.mono.edges = append(c.mono.edges, monoEdge{q, p, w, at, c.cur, arg, name})
		}
	}
}

// monoLinkSelf joins a 196m method's type parameters to its owner's: in
// `func Grow[P](b Box[P], x Box[[]P])` the P that the first parameter binds
// is Box's P, as a receiver's parameter was, so an instantiation in the
// signature or the body grows Box's parameter. Go canonicalizes a
// receiver's type parameters to the type's the same way (go/types mono.go,
// recordCanon); here each pair gets weight-0 edges both ways, which puts
// them in one strongly connected component. Only a function whose type
// parameters are all its first parameter's is joined: it is the one that
// lowers to a Go method; one with a parameter of its own lowers to a Go
// function (Go methods take no type parameters), whose parameters Go does
// not canonicalize either.
func (c *checker) monoLinkSelf(f *Func, ctx *typeCtx) {
	if f.owner == nil || len(f.selfArgs) == 0 || len(f.selfArgs) != len(ctx.params) {
		return
	}
	for i, a := range f.selfArgs {
		id, ok := unparen(a).(*syntax.Ident)
		if !ok || i >= len(f.owner.TParams) {
			continue
		}
		p, q := ctx.params[id.Name], f.owner.TParams[i]
		if p == nil || p == q || p.Kind != KindType || q.Kind != KindType {
			continue
		}
		for _, pair := range [][2]*TypeParam{{p, q}, {q, p}} {
			key := [2]*TypeParam{pair[0], pair[1]}
			if c.mono.seen == nil {
				c.mono.seen = map[[2]*TypeParam]int{}
			}
			if _, ok := c.mono.seen[key]; ok {
				continue
			}
			c.mono.seen[key] = len(c.mono.edges)
			c.mono.edges = append(c.mono.edges, monoEdge{pair[0], pair[1], 0, a, c.cur, pair[0], f.owner.Obj.Name})
		}
	}
}

// occurs reports whether the type parameter q occurs in t.
func occurs(q *TypeParam, t Type) bool {
	seen := map[*Named]bool{}
	var walk func(t Type) bool
	walk = func(t Type) bool {
		switch t := t.(type) {
		case *TypeParam:
			return t == q
		case *Named:
			if seen[t] {
				return false
			}
			seen[t] = true
			for _, a := range t.TArgs {
				if walk(a) {
					return true
				}
			}
		case *Slice:
			return walk(t.Elem)
		case *Array:
			return walk(t.Elem)
		case *Map:
			return walk(t.Key) || walk(t.Elem)
		case *Pointer:
			return walk(t.Elem)
		case *Tuple:
			for _, e := range t.Types {
				if walk(e) {
					return true
				}
			}
		case *Struct:
			for _, f := range t.Fields {
				if walk(f.Type) {
					return true
				}
			}
		case *Union:
			for _, v := range t.Variants {
				if walk(v) {
					return true
				}
			}
		case *Signature:
			for _, p := range t.Params {
				if walk(p.Type) {
					return true
				}
			}
			for _, r := range t.Results {
				if walk(r) {
					return true
				}
			}
			for _, e := range t.Errors.Elems() {
				if walk(e) {
					return true
				}
			}
		case *Interface:
			for _, m := range t.Methods {
				if m.Sig != nil && walk(m.Sig) {
					return true
				}
			}
		case *MachineType:
			return walk(t.State) || walk(t.Signal)
		case *RowArg:
			for _, e := range t.Row.Elems() {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(t)
}

// checkInstantiationCycles reports each new cycle of positive weight once
// (S108, at a growing instantiation of the cycle) and marks its parameters
// bad: a call of a function, or an instance of a type, whose parameters are
// bad adds nothing more (D3), so nothing expands further.
func (c *checker) checkInstantiationCycles() {
	out := map[*TypeParam][]int{}
	var verts []*TypeParam
	add := func(v *TypeParam) {
		if _, ok := out[v]; !ok {
			out[v] = nil
			verts = append(verts, v)
		}
	}
	for i, e := range c.mono.edges {
		add(e.from)
		add(e.to)
		out[e.from] = append(out[e.from], i)
	}
	index := map[*TypeParam]int{}
	low := map[*TypeParam]int{}
	on := map[*TypeParam]bool{}
	comp := map[*TypeParam]int{}
	var stack []*TypeParam
	next, ncomp := 0, 0
	var visit func(v *TypeParam)
	visit = func(v *TypeParam) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, i := range out[v] {
			w := c.mono.edges[i].to
			if _, ok := index[w]; !ok {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			for {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[top] = false
				comp[top] = ncomp
				if top == v {
					break
				}
			}
			ncomp++
		}
	}
	for _, v := range verts {
		if _, ok := index[v]; !ok {
			visit(v)
		}
	}
	for _, e := range c.mono.edges {
		if e.weight == 0 || comp[e.from] != comp[e.to] {
			continue
		}
		members := []*TypeParam{}
		for _, v := range verts {
			if comp[v] == comp[e.from] {
				members = append(members, v)
			}
		}
		already := false
		for _, m := range members {
			if c.mono.reported[m] {
				already = true
			}
		}
		if c.mono.bad == nil {
			c.mono.bad = map[*TypeParam]bool{}
			c.mono.reported = map[*TypeParam]bool{}
		}
		for _, m := range members {
			c.mono.bad[m] = true
			c.mono.reported[m] = true
		}
		if already {
			continue
		}
		saved := c.cur
		c.cur = e.file
		c.errorf(e.at, CodeType, "instantiation cycle: %s is instantiated with %s, which grows on every round, so its instances never end (types.md, Generics 6; Go rejects it too)", e.inst, e.arg)
		c.cur = saved
	}
}

// monoBad reports whether any of tps is on an instantiation cycle.
func (c *checker) monoBad(tps []*TypeParam) bool {
	for _, tp := range tps {
		if c.mono.bad[tp] {
			return true
		}
	}
	return false
}
