package check

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Resource types and mode crossing (#197 L8 A1; resources.md R1, R22,
// R34, R40; plan 1.2). Ownership is a mode, not a type mark: the
// ownership rules apply only to a value whose type holds a resource, and
// every other type crosses (OxCaml's mode crossing). One property decides
// it, in one place:
//
//  1. Does not cross: a type with a checked Close() (one parameter, the
//     receiver; no result; no written failure row; no type parameter of its
//     own), and an operation type with a Close() operation of that shape
//     (R1). An operation type whose operation takes a parameter (a written
//     `takes`) does not cross when that parameter's type does not cross at
//     the instance: queue.Queue[Res] holds the values pushed into it.
//  2. A record, union, tuple, slice or Map crosses exactly when every
//     field, variant, element (and key) crosses; a generic record's type
//     arguments count as its elements (a phantom parameter is
//     conservative). A generic declaration carries no mark: Stack[int]
//     crosses, Stack[Res] does not.
//  3. Recursion: a back edge counts as no resource yet; a cycle holds one
//     only when a member holds one directly (the least fixpoint).
//  4. A function value crosses (R33), a borrow *T crosses (it owns
//     nothing; types.md, Borrows), and so does an operation type with no
//     Close and no taking operation: a shared handle (queue.Queue[int],
//     actor.Actor).
//  5. A type parameter: holdsResource treats it as crossing (the concrete
//     question; an instantiation decides it, A3's copy sites), and
//     mayHoldResource as a resource (rule 6 of plan 1.2: a generic body is
//     checked as if T were one; a `takes` of a T has effect).
//  6. A host (Go) type does not cross when its method set has Close()
//     or Close() error (IG9, hostres.go), nor does go.Ref[T] of one (its
//     type argument); any other host type crosses.
//
// The walk is the holdsTask walk (taskescape.go): Tarjan's components over
// interned type identity, memoized per finished component, one memo per
// mode, linear in the type graph (resSteps counts its visits).
//
// Rules that read it in A1: a resource map key is S112 (R40); a resource
// converted to an operation type with no release (releasable) is S108
// (R22, typeops.go);
// a type whose zero value would be a resource is not defaultable
// (typeops.go, letZero); a written `takes` on a crossing parameter is
// S110.
//
// Prior art: OxCaml's mode crossing and with-bounds (a type crosses when
// its parts do); Rust's auto traits are the same coinductive walk; Swift's
// ~Copyable is a written mark the owner declined (q56).

// holdsResource reports whether a value of type t holds a resource, a type
// parameter counting as plain data.
func (c *checker) holdsResource(t Type) bool {
	w := resWalk{c: c}
	h, _ := w.visit(t)
	return h
}

// mayHoldResource is holdsResource with every type parameter counted as a
// resource: the question inside a generic declaration.
func (c *checker) mayHoldResource(t Type) bool {
	w := resWalk{c: c, generic: true}
	h, _ := w.visit(t)
	return h
}

// releasable reports whether t has a release (resources R1, R13): it
// holds a resource and dropOf derives its release. The one definition of
// "has a release": an operation type that only keeps the values it takes
// holds a resource but has none. R22 reads it at a conversion and at an
// instantiation; the ownership pass reports its absence on an owned value.
func (c *checker) releasable(t Type) bool {
	if !c.holdsResource(t) {
		return false
	}
	d, _ := c.dropOf(t)
	return d != nil
}

// crosses reports whether ownership means nothing for a value of type t.
func (c *checker) crosses(t Type) bool { return !c.holdsResource(t) }

type resWalk struct {
	c       *checker
	generic bool
	nodes   map[int32]*taskNode
	stack   []int32
	next    int
}

func (w *resWalk) memo() map[int32]bool {
	i := 0
	if w.generic {
		i = 1
	}
	if w.c.resMemo[i] == nil {
		w.c.resMemo[i] = map[int32]bool{}
	}
	return w.c.resMemo[i]
}

// visit returns whether t holds a resource as far as the walk knows, and
// t's low link (taskWalk.visit's protocol).
func (w *resWalk) visit(t Type) (bool, int) {
	c := w.c
	c.resSteps++
	switch t := t.(type) {
	case *TypeParam:
		return w.generic, noLow
	case *Named:
		if t.foreign {
			return false, noLow
		}
		if t.Obj != nil && t.Obj.Pkg != nil && t.Obj.Pkg.Host() {
			// A Go type is a resource when its method set has Close() or
			// Close() error (IG9, hostres.go); its fields are Go's.
			if c.hostClose(t) != nil {
				return true, noLow
			}
			if it := asInterface(t); it != nil && hostCloseOp(t, it) != nil {
				return true, noLow
			}
			return false, noLow
		}
	case *Slice, *Array, *Map, *Tuple, *Struct, *Union, *Interface:
	default:
		// A basic type, a borrow, a row and a function type cross.
		return false, noLow
	}
	id, infer := c.types.intern(t)
	memo := w.memo()
	if h, ok := memo[id]; ok {
		return h, noLow
	}
	if n := w.nodes[id]; n != nil {
		if n.onStack {
			return n.holds, n.index
		}
		return n.holds, noLow
	}
	if w.nodes == nil {
		w.nodes = map[int32]*taskNode{}
	}
	n := &taskNode{index: w.next, low: w.next, onStack: true, final: !infer}
	w.next++
	w.nodes[id] = n
	w.stack = append(w.stack, id)
	edge := func(u Type) {
		if n.holds {
			return
		}
		h, low := w.visit(u)
		n.holds = n.holds || h
		n.low = min(n.low, low)
	}
	// operations reads an operation type's body; sub maps a generic
	// declaration's type parameters to the instance's arguments.
	operations := func(it *Interface, sub map[*TypeParam]Type) {
		if m := it.method("Close"); closeShape(m) && !m.Sig.Fallible {
			n.holds = true
			return
		}
		for _, m := range it.Methods {
			if m.Sig == nil {
				continue
			}
			for _, i := range m.Sig.Takes {
				if i >= 0 && i < len(m.Sig.Params) {
					pt := m.Sig.Params[i].Type
					if len(sub) > 0 {
						pt = c.subst(pt, sub)
					}
					edge(pt)
				}
			}
		}
	}
	switch t := t.(type) {
	case *Named:
		o := t.origin()
		switch {
		case o.state != 2:
			n.final = false
		case c.closeMethod(t) != nil:
			n.holds = true
		default:
			switch ou := o.underlying().(type) {
			case *Interface:
				// The declared operations, a taking operation's parameter
				// read at this instance.
				var sub map[*TypeParam]Type
				for i, tp := range o.TParams {
					if i < len(t.TArgs) && t.TArgs[i] != tp {
						if sub == nil {
							sub = map[*TypeParam]Type{}
						}
						sub[tp] = t.TArgs[i]
					}
				}
				operations(ou, sub)
			case *Signature:
			default:
				for _, a := range t.TArgs {
					edge(a)
				}
				// The declared body, as holdsTask reads it: a growing
				// generic stays a finite graph. Its type parameters stand
				// for the arguments above, so the generic walk reads the
				// body as the concrete one does.
				if w.generic {
					n.holds = n.holds || c.holdsResource(o.underlying())
				} else {
					edge(o.underlying())
				}
			}
		}
	case *Slice:
		edge(t.Elem)
	case *Array:
		edge(t.Elem)
	case *Map:
		edge(t.Key)
		edge(t.Elem)
	case *Tuple:
		for _, e := range t.Types {
			edge(e)
		}
	case *Struct:
		for _, f := range t.Fields {
			edge(f.Type)
		}
	case *Union:
		for _, v := range t.Variants {
			edge(v)
		}
	case *Interface:
		operations(t, nil)
	}
	if n.low < n.index {
		return n.holds, n.low
	}
	i := len(w.stack) - 1
	for w.stack[i] != id {
		i--
	}
	members := w.stack[i:]
	holds, final := false, true
	for _, m := range members {
		holds = holds || w.nodes[m].holds
		final = final && w.nodes[m].final
	}
	for _, m := range members {
		mn := w.nodes[m]
		mn.holds, mn.onStack = holds, false
		if final {
			memo[m] = holds
		}
	}
	w.stack = w.stack[:i]
	return holds, noLow
}

// closeMethod returns n's checked Close (R1): a function Close whose one
// parameter is the receiver, with no result, no written failure row and no
// type parameter of its own; nil when n has none. A Close with a failure
// row is closeFacts' R2 error and makes no resource.
func (c *checker) closeMethod(n *Named) *Func {
	f := n.method("Close")
	if f == nil || f.owner == nil {
		return nil
	}
	sig := f.Sig
	if sig == nil {
		sig = c.funcSig(f)
	}
	if sig == nil || len(sig.Params) != 1 || len(sig.Results) != 0 || sig.Fallible {
		return nil
	}
	recv := sig.Params[0].Type
	if p, ok := recv.(*Pointer); ok {
		recv = p.Elem
	}
	rn, ok := recv.(*Named)
	if !ok || ownTypeParam(rn, f) != nil {
		return nil
	}
	return f
}

// zeroHoldsResource reports whether the zero value of t would be a resource:
// a type with a checked Close, or a tuple holding one. An empty slice or Map
// holds nothing.
func (c *checker) zeroHoldsResource(t Type) bool {
	switch u := t.(type) {
	case *Named:
		if c.closeMethod(u) != nil {
			return true
		}
		if tup, ok := u.underlying().(*Tuple); ok {
			return c.zeroHoldsResource(tup)
		}
	case *Tuple:
		for _, e := range u.Types {
			if c.zeroHoldsResource(e) {
				return true
			}
		}
	}
	return false
}

// resourceSite is a place A1's late pass decides once every Close is known:
// a written map key type or a parameter a written takes clause names.
type resourceSite struct {
	file *fileInfo
	at   syntax.Node
	t    Type
	name string // the taken parameter; "" for a map key
}

// takesClause resolves a written takes clause against sig's parameters.
// method is true in an operation type, where a bare takes names the value
// the operation is called on.
func (c *checker) takesClause(u *syntax.TakesClause, sig *Signature, method bool) {
	if c.takesSeen == nil {
		c.takesSeen = map[*syntax.TakesClause]bool{}
	}
	first := !c.takesSeen[u]
	c.takesSeen[u] = true
	if len(u.Names) == 0 {
		if method {
			sig.TakesSelf = true
		} else if first {
			c.errorf(u, CodeType, "a bare takes names the value an operation is called on; name the parameter: takes p")
		}
		return
	}
	seen := map[string]bool{}
	for _, id := range u.Names {
		at := -1
		for i, p := range sig.Params {
			if p.Name == id.Name && id.Name != "_" {
				at = i
				break
			}
		}
		switch {
		case at < 0:
			if first {
				c.errorf(made(c, id, syntax.Node(u)), CodeType, "takes %s names no parameter", id.Name)
			}
			continue
		case seen[id.Name]:
			if first {
				c.errorf(made(c, id, syntax.Node(u)), CodeType, "takes names %s twice", id.Name)
			}
			continue
		}
		seen[id.Name] = true
		sig.Takes = append(sig.Takes, at)
		if first {
			c.resourceSites = append(c.resourceSites, resourceSite{file: c.cur, at: made(c, id, syntax.Node(u)), t: sig.Params[at].Type, name: id.Name})
		}
	}
}

// mapKeySite records a written map key type for the late R40 check.
func (c *checker) mapKeySite(x syntax.Expr, k Type) {
	if c.mapKeysSeen == nil {
		c.mapKeysSeen = map[syntax.Expr]bool{}
	}
	if c.mapKeysSeen[x] {
		return
	}
	c.mapKeysSeen[x] = true
	c.resourceSites = append(c.resourceSites, resourceSite{file: c.cur, at: x, t: k})
}

// checkResourceTypes decides the recorded sites once every signature and
// body is checked: a resource map key is S112 (R40); a taken parameter
// whose type crosses is S110. A key type that names a type parameter is
// decided at the instantiation (A3).
func (c *checker) checkResourceTypes() {
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, s := range c.resourceSites {
		if s.t == nil || isInvalid(s.t) {
			continue
		}
		c.cur = s.file
		if s.name == "" {
			if c.holdsResource(s.t) {
				c.errorf(s.at, "S112", "map key type %s holds a resource, and a map key has no owner to release it (resources R40); key the map by a plain value, such as an id", s.t)
			}
			continue
		}
		if !c.mayHoldResource(s.t) {
			c.errorf(s.at, CodeUnused, "takes %s has no effect: %s is not a resource", s.name, s.t)
		}
	}
	c.checkOwnershipSites()
}

// providerAssignability is assignability for a provider that implements a
// capability (a context entry, a builder, a law's provider): the with that
// builds the provider owns and releases it through its own Close (resources
// R24, R25), so R22 does not apply; the capability's value lends it.
func (c *checker) providerAssignability(v, t Type) (assignment, string) {
	saved := c.providing
	c.providing = true
	defer func() { c.providing = saved }()
	return c.assignability(v, t)
}

// resourceIntoCrossingText is R22's refusal: a resource converted to an
// operation type that crosses would never be released.
func resourceIntoCrossingText(v, t Type) string {
	return fmt.Sprintf("%s is a resource and %s has no Close, so the value would never be released (resources R22); give %s a Close() operation", v, t, t)
}

// R22 at an instantiation (16a fix r4). A type parameter whose
// constraint is an operation type is the conversion R22 decides, made
// once for the whole generic body: the body uses its values through that
// operation type only. So a type argument that holds a resource is
// admitted only for a constraint that has a release (releasable, R1/R13:
// a Close() operation; an operation that only takes the value keeps it
// and releases nothing, 16a fix r5): any other constraint would let the
// body use the value, and the values it yields, as plain data that is
// never released. One rule at every instantiation: a call (written or
// inferred type arguments), a generic type, a generic function value and
// a method of a generic type all record the site where they check the
// constraint (recordOwnership). A type argument that is a resource only
// through the enclosing function's type parameter, and a conversion to an
// operation type in a generic body, are copy sites of that parameter
// (ownershipCopies): the instantiation of the enclosing function decides
// them (R44).

// ownershipSite is a type, from, that meets an operation type without a
// Close, to: a type argument (tp is its type parameter of the generic
// declaration name) or a conversion in a generic body (tp nil).
type ownershipSite struct {
	file     *fileInfo
	at       syntax.Node
	name     string
	tp       *TypeParam
	from, to Type
}

type ownershipKey struct {
	at syntax.Node
	tp *TypeParam
}

// recordOwnership records the type arguments of an instantiation of name
// at at whose constraint is an operation type; the late pass decides them
// once every Close is known.
func (c *checker) recordOwnership(name string, tparams []*TypeParam, targs []Type, at syntax.Node) {
	if c.silent > 0 || at == nil {
		return
	}
	m := paramMap(tparams, targs)
	for i, tp := range tparams {
		if i >= len(targs) || tp.Kind != KindType || tp.Constraint == nil || targs[i] == nil {
			continue
		}
		cons := c.subst(tp.Constraint, m)
		if it := asInterface(cons); it == nil || it.Set != SetAll || len(it.Methods) == 0 {
			continue
		}
		c.addOwnershipSite(ownershipSite{file: c.cur, at: at, name: name, tp: tp, from: targs[i], to: cons})
	}
}

// recordConversion records a value of type v that assignability put into
// the operation type t, when v is a resource only through a type
// parameter: the conversion is a copy site of that parameter.
func (c *checker) recordConversion(x syntax.Expr, v, t Type) {
	if c.silent > 0 || x == nil || c.providing || c.hostLendArg(t) || !namesTypeParam(v) {
		return
	}
	if it := asInterface(t); it == nil || it.Set != SetAll || len(it.Methods) == 0 {
		return
	}
	c.addOwnershipSite(ownershipSite{file: c.cur, at: x, from: v, to: t})
}

// namesTypeParam reports whether t is built from a type parameter.
func namesTypeParam(t Type) bool {
	found := false
	eachTypeParam(t, func(*TypeParam) { found = true })
	return found
}

func (c *checker) addOwnershipSite(s ownershipSite) {
	k := ownershipKey{s.at, s.tp}
	if c.ownershipSeen[k] {
		return
	}
	if c.ownershipSeen == nil {
		c.ownershipSeen = map[ownershipKey]bool{}
	}
	c.ownershipSeen[k] = true
	c.ownershipSites = append(c.ownershipSites, s)
}

// checkOwnershipSites refuses a type argument that is a resource for a
// constraint that has no release (S108, R22).
func (c *checker) checkOwnershipSites() {
	for _, s := range c.ownershipSites {
		if s.tp == nil || containsInvalid(s.from) || containsInvalid(s.to) {
			continue
		}
		if c.holdsResource(s.from) && !c.releasable(s.to) {
			c.cur = s.file
			c.errorf(s.at, CodeType, "%s does not satisfy the constraint %s of %s: %s (types.md, Generics 2)", s.from, s.to, s.tp.Name, resourceIntoCrossingText(s.from, s.to))
		}
	}
}

// ownershipCopies records, for a generic function, each type parameter
// that makes a site's type a resource when the operation type it meets
// cannot hold one: the instantiation decides it (R44).
func (c *checker) ownershipCopies(owner map[*TypeParam]*Func) {
	for _, s := range c.ownershipSites {
		if containsInvalid(s.from) || containsInvalid(s.to) || c.holdsResource(s.from) || !c.mayHoldResource(s.from) || c.releasable(s.to) {
			continue
		}
		what := fmt.Sprintf("%s is converted to %s, which has no Close (resources R22)", s.from, s.to)
		if s.tp != nil {
			what = fmt.Sprintf("it binds %s of %s to %s, and its constraint %s has no Close (resources R22)", s.tp.Name, s.name, s.from, s.to)
		}
		eachTypeParam(s.from, func(tp *TypeParam) {
			if f := owner[tp]; f != nil {
				c.addCopy(f, CopySite{TParam: slices.Index(f.Sig.TParams, tp), At: s.at, Where: c.sitePos(s.file, s.at), What: what})
			}
		})
	}
}
