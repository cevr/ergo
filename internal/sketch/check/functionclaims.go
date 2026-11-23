package check

import (
	"fmt"
	"slices"
)

// A functionClaim is a checked group, in the contract's package. recv and
// role use the first clause's receiver-bound parameters. Each member keeps
// its own binders: neither the spelling nor the order of those binders is
// shared across clauses. References to an included slot reuse its function.
type functionClaim struct {
	recv Type
	role *Named
	head *Func
	bind []*TypeParam
	ops  map[string]*Func
}

// StaticClaimConstraint reports a contract with function-form claimants.
// The contract stays available as a value through ordinary type claims.
func StaticClaimConstraint(t Type) bool {
	op, ok := t.(*Named)
	return ok && op.Obj != nil && op.Obj.Pkg != nil && len(op.Obj.Pkg.functionClaims[op.origin()]) > 0
}

func receiverBinders(sig *Signature) []*TypeParam {
	if len(sig.Params) == 0 {
		return nil
	}
	var out []*TypeParam
	for _, p := range sig.TParams {
		one := inferer{set: map[*TypeParam]bool{p: true}}
		if one.mentions(sig.Params[0].Type) {
			out = append(out, p)
		}
	}
	return out
}

func (c *checker) claimMatch(pattern, recv Type, bind []*TypeParam) (map[*TypeParam]Type, bool) {
	if len(bind) == 0 {
		return nil, c.identical(pattern, recv)
	}
	if c.identical(pattern, recv) {
		return paramMap(bind, paramTypes(bind)), true
	}
	in := inferer{c: c, m: make(map[*TypeParam]Type, len(bind)), set: make(map[*TypeParam]bool, len(bind))}
	for _, p := range bind {
		in.set[p] = true
	}
	if !in.unify(pattern, recv, unifyExact) || !c.identical(c.subst(pattern, in.m), recv) {
		return nil, false
	}
	for _, p := range bind {
		if in.m[p] == nil {
			return nil, false
		}
	}
	return in.m, true
}

// claimsIntersect unifies both heads, then intersects the bounds of the
// equations' remaining free parameters. The concrete lookup above has only
// one free side. In particular [][]T and []U intersect even though neither
// is alpha-equivalent to the other.
func (c *checker) claimsIntersect(g, h *functionClaim) bool {
	ps := append(slices.Clone(g.bind), h.bind...)
	u := claimUnification{c: c, bound: make(map[*TypeParam]Type), free: make(map[*TypeParam]bool)}
	for _, p := range ps {
		u.free[p] = true
	}
	if !u.same(g.recv, h.recv) || !u.same(g.role, h.role) {
		return false
	}
	terms := make(map[*TypeParam][]*Term)
	for _, p := range ps {
		to := u.read(p)
		bound := termsOf(asInterface(c.subst(p.Constraint, u.bound)))
		if len(bound) == 0 {
			continue
		}
		if q, ok := to.(*TypeParam); ok && u.free[q] {
			prev := terms[q]
			if prev == nil {
				terms[q] = bound
				continue
			}
			var common []*Term
			for _, a := range prev {
				for _, b := range bound {
					if c.termCovers(b, a) {
						common = append(common, a)
					} else if c.termCovers(a, b) {
						common = append(common, b)
					}
				}
			}
			if len(common) == 0 {
				return false
			}
			terms[q] = common
		} else if !c.inTypeSet(to, bound) {
			return false
		}
	}
	return true
}

type claimUnification struct {
	c     *checker
	free  map[*TypeParam]bool
	bound map[*TypeParam]Type
}

func (u *claimUnification) read(t Type) Type {
	for {
		p := claimParam(t)
		if p == nil || u.bound[p] == nil {
			return t
		}
		t = u.bound[p]
	}
}

func (u *claimUnification) occurs(p *TypeParam, t Type) bool {
	t = u.read(t)
	if claimParam(t) == p {
		return true
	}
	parts, _ := claimParts(t)
	return slices.ContainsFunc(parts, func(a Type) bool { return u.occurs(p, a) })
}

func (u *claimUnification) same(a, b Type) bool {
	a, b = u.read(a), u.read(b)
	if u.c.identical(a, b) {
		return true
	}
	if p := claimParam(a); u.free[p] {
		if u.occurs(p, b) {
			return false
		}
		u.bound[p] = b
		return true
	}
	if p := claimParam(b); u.free[p] {
		return u.same(b, a)
	}
	x, head := claimParts(a)
	y, other := claimParts(b)
	if head != other || len(x) == 0 || len(x) != len(y) {
		return false
	}
	for i, t := range x {
		if !u.same(t, y[i]) {
			return false
		}
	}
	return true
}

func claimParts(t Type) ([]Type, any) {
	switch a := t.(type) {
	case *Named:
		return a.TArgs, a.origin()
	case *Slice:
		return []Type{a.Elem}, (*Slice)(nil)
	case *Pointer:
		return []Type{a.Elem}, (*Pointer)(nil)
	case *Array:
		return []Type{a.Elem}, a.Len
	case *Map:
		return []Type{a.Key, a.Elem}, (*Map)(nil)
	case *Tuple:
		return a.Types, (*Tuple)(nil)
	case *Signature:
		ts := make([]Type, 0, len(a.Params)+len(a.Results)+2)
		for _, p := range a.Params {
			ts = append(ts, p.Type)
		}
		ts = append(ts, a.Results...)
		ts = append(ts, &RowArg{Row: a.Errors}, &RowArg{Row: a.Uses})
		return ts, struct {
			params, results           int
			fails, entry, nonescaping bool
		}{len(a.Params), len(a.Results), a.Fallible, a.Entry, a.NonEscaping}
	}
	return nil, nil
}

func paramTypes(ps []*TypeParam) []Type {
	ts := make([]Type, len(ps))
	for i, p := range ps {
		ts[i] = p
	}
	return ts
}

func (c *checker) sameFunctionClaim(g, h *functionClaim) bool {
	m, ok := c.claimMatch(g.recv, h.recv, g.bind)
	if !ok || len(g.bind) != len(h.bind) || !c.identical(c.subst(g.role, m), h.role) {
		return false
	}
	seen := make(map[*TypeParam]bool, len(g.bind))
	for _, p := range g.bind {
		q := claimParam(m[p])
		if q == nil || seen[q] || !slices.Contains(h.bind, q) || p.Kind != q.Kind || p.Own != q.Own ||
			!c.sameConstraint(c.subst(p.Constraint, m), q.Constraint) {
			return false
		}
		seen[q] = true
	}
	return true
}

// claimParam also reads a row argument which names precisely one binder.
func claimParam(t Type) *TypeParam {
	if r, ok := t.(*RowArg); ok && len(r.Row.Elems()) == 1 {
		t = r.Row.Elems()[0]
	}
	p, _ := t.(*TypeParam)
	return p
}

func (c *checker) functionClaim(recv Type, op *Named) (*functionClaim, map[*TypeParam]Type) {
	if op == nil || op.Obj == nil || op.Obj.Pkg == nil {
		return nil, nil
	}
	var found *functionClaim
	var args map[*TypeParam]Type
	for _, g := range op.Obj.Pkg.functionClaims[op.origin()] {
		m, ok := c.claimMatch(g.recv, recv, g.bind)
		if !ok || op.Orig != nil && !c.identical(c.subst(g.role, m), op) {
			continue
		}
		for _, p := range g.bind {
			if p.Kind == KindType && ((!p.Own && c.mayHoldResource(m[p])) ||
				!c.meetsReapplicationBound(m[p], c.subst(p.Constraint, m))) {
				ok = false
				break
			}
		}
		if ok {
			if found == nil {
				found, args = g, m
			} else if !c.identical(c.subst(found.role, args), c.subst(g.role, m)) {
				return nil, nil // an origin alone cannot select a closed instance
			}
		}
	}
	return found, args
}

func (c *checker) functionClaimHead(f *Func) *functionClaim {
	x := f.Decl.Implements
	if x == nil {
		return nil
	}
	c.cur = f.file
	if f.Decl.Recv != nil || f.Sig == nil || len(f.Sig.Params) == 0 {
		c.errorf(x, CodeType, "a function claim needs its receiver as the first parameter")
		return nil
	}
	ctx := &typeCtx{params: make(map[string]*TypeParam, len(f.Sig.TParams))}
	for _, tp := range f.Sig.TParams {
		ctx.params[tp.Name] = tp
	}
	c.inConstraint++
	c.deferBounds++
	t := c.typExpr(x, ctx)
	c.deferBounds--
	c.inConstraint--
	if containsInvalid(t) {
		return nil
	}
	op, ok := t.(*Named)
	if !ok || asInterface(op) == nil || asInterface(op).Set != SetAll || len(asInterface(op).Methods) == 0 {
		c.errorf(x, CodeType, "a function claim names an operation type")
		return nil
	}
	if op.Obj.Pkg != c.pkg {
		c.errorf(x, CodeType, "a function claim is written in the contract's package")
		return nil
	}
	recv := f.Sig.Params[0].Type
	base := recv
	if p, ok := recv.(*Pointer); ok {
		base = p.Elem
	}
	if n, ok := base.(*Named); ok && n.Obj.Pkg == c.pkg {
		c.errorf(x, CodeType, "a function claim for a type of this package is written on the type")
		return nil
	}
	if _, ok := base.(*TypeParam); ok {
		c.errorf(x, CodeType, "a function claim needs a concrete receiver shape in its first parameter")
		return nil
	}
	if asInterface(op).method(f.Name) == nil {
		c.errorf(x, CodeProvider, "%s has no operation named %s", c.writtenType(x, op), f.Name)
		return nil
	}
	g := &functionClaim{recv: recv, role: op, head: f, bind: receiverBinders(f.Sig), ops: map[string]*Func{f.Name: f}}
	if why := c.functionProjectionWhy(g, op); why != "" {
		c.errorf(x, CodeProvider, "%s", why)
		return nil
	}
	if p, why := c.boundsUnmet(op); p != nil {
		c.errorf(x, CodeProvider, "%s claims %s, and %s's %s must satisfy %s: %s", recv, op, op.Obj.Name, p.Name, constraintText(p), why)
		return nil
	}
	return g
}

func (c *checker) functionProjectionWhy(g *functionClaim, op *Named) string {
	for _, t := range op.TArgs {
		in := inferer{set: map[*TypeParam]bool{}}
		for _, p := range g.head.Sig.TParams {
			in.set[p] = !slices.Contains(g.bind, p)
		}
		if in.mentions(t) {
			return "a function claim argument can name only parameters bound by the receiver"
		}
	}
	if reappliesSelf(op) {
		return g.projectionWhy(op)
	}
	return ""
}

func (g *functionClaim) projectionWhy(op *Named) string {
	used := make(map[*TypeParam]bool, len(op.TArgs))
	for _, a := range op.TArgs {
		p := claimParam(a)
		if p == nil || !slices.Contains(g.bind, p) {
			return fmt.Sprintf("%s re-applies Self, and %s is not a type parameter bound by the receiver", op.Obj.Name, a)
		}
		if used[p] {
			return fmt.Sprintf("%s re-applies Self, and %s is named more than once in the claim", op.Obj.Name, p.Name)
		}
		used[p] = true
	}
	return ""
}

func (c *checker) functionClaimSelf(g *functionClaim, m map[*TypeParam]Type) selfReader {
	return func(s *SelfType, args []Type) Type {
		return c.functionClaimReceiver(g, m, s.owner, args)
	}
}

func (c *checker) functionClaimReceiver(g *functionClaim, m map[*TypeParam]Type, owner *Named, args []Type) Type {
	if len(args) == 0 {
		return c.subst(g.recv, m)
	}
	role := c.selfClaim(g.role, owner)
	if role == nil || len(role.TArgs) != len(args) || g.projectionWhy(role) != "" {
		return invalid
	}
	to := make(map[*TypeParam]Type, len(g.bind))
	for p, t := range m {
		to[p] = t
	}
	for i, a := range role.TArgs {
		to[claimParam(a)] = args[i]
	}
	for _, p := range g.bind {
		t := to[p]
		if t != nil && p.Kind == KindType && ((!p.Own && c.mayHoldResource(t)) ||
			!c.meetsReapplicationBound(t, c.subst(p.Constraint, to))) {
			return invalid
		}
	}
	return c.subst(g.recv, to)
}

func (c *checker) collectFunctionClaims() {
	var groups []*functionClaim
	for _, f := range c.funcs {
		g := c.functionClaimHead(f)
		if g == nil {
			continue
		}
		merged := false
		for _, h := range groups {
			if h.role.origin() != g.role.origin() || !c.sameFunctionClaim(h, g) {
				continue
			}
			if prev := h.ops[f.Name]; prev != nil {
				c.claimOverlap(g, prev)
			} else {
				h.ops[f.Name] = f
			}
			merged = true
			break
		}
		if !merged {
			groups = append(groups, g)
		}
	}

	// A projected role retains the declaration which supplies each
	// operation. Only the role's package may publish the projection.
	if len(groups) == 0 {
		return
	}
	c.pkg.functionClaims = make(map[*Named][]*functionClaim)
	accepted := make([]*functionClaim, 0, len(groups))
	for _, g := range groups {
		c.cur = g.head.file
		roles := append([]*Named{g.role}, c.declaredOps(g.role)...)
		valid := true
		for _, role := range roles {
			if role.Obj.Pkg != c.pkg {
				if other, _ := c.functionClaim(g.recv, role); other == nil {
					c.errorf(g.head.Decl.Implements, CodeProvider, "included %s has no function claim for %s in its declaring package; add one there or stop including %s", role, g.recv, role)
					valid = false
				}
				continue
			}
			projected := *g
			projected.role = role
			for _, h := range c.pkg.functionClaims[role.origin()] {
				if c.claimsIntersect(h, &projected) && !c.sameClaimBindings(h, &projected) {
					c.claimOverlap(g, h.head)
					valid = false
					break
				}
			}
		}
		if !valid {
			continue
		}
		accepted = append(accepted, g)
		for _, role := range roles {
			if role.Obj.Pkg == c.pkg {
				projected := *g
				projected.role = role
				c.pkg.functionClaims[role.origin()] = append(c.pkg.functionClaims[role.origin()], &projected)
			}
		}
	}

	// Complete groups before bodies use them. Forward references to
	// included claims are independent of the declarations' order.
	for _, g := range accepted {
		c.cur = g.head.file
		roles := c.declaredOps(g.role)
		for _, method := range asInterface(g.role).Methods {
			for _, role := range roles {
				if asInterface(role).method(method.Name) == nil {
					continue
				}
				if other, _ := c.functionClaim(g.recv, role); other != nil {
					f := other.ops[method.Name]
					if prev := g.ops[method.Name]; f != nil && prev != nil && prev != f {
						c.claimOverlap(g, f)
					} else if f != nil {
						g.ops[method.Name] = f
					}
				}
			}
			if g.ops[method.Name] == nil {
				c.errorf(g.head.Decl.Implements, CodeProvider, "%s claims %s, but no function provides %s", g.recv, g.role, method.Name)
			}
		}
	}
}

func (c *checker) claimOverlap(g *functionClaim, prev *Func) {
	c.cur = g.head.file
	line, col := prev.file.syntax.Position(prev.Decl.Implements.Pos())
	c.errorf(g.head.Decl.Implements, CodeProvider, "function claim of %s for %s overlaps the clause at %d:%d", g.role, g.recv, line, col)
}

func (c *checker) sameClaimBindings(g, h *functionClaim) bool {
	if !c.sameFunctionClaim(g, h) {
		return false
	}
	for _, op := range asInterface(g.role).Methods {
		if a, b := g.ops[op.Name], h.ops[op.Name]; a != nil && b != nil && a != b {
			return false
		}
	}
	return true
}

func (c *checker) checkFunctionClaims() {
	// Use source order for both validation and diagnostics.
	for _, f := range c.pkg.Funcs {
		if f.Decl.Implements == nil {
			continue
		}
		op, _ := c.pkg.Info.Types[f.Decl.Implements].(*Named)
		if op == nil || len(f.Sig.Params) == 0 {
			continue
		}
		recv := f.Sig.Params[0].Type
		g, m := c.functionClaim(recv, op)
		if g == nil || g.ops[f.Name] != f {
			continue
		}
		c.cur = f.file
		sig := asInterface(op).method(f.Name).Sig
		want := c.substWith(sig, nil, c.functionClaimSelf(g, m)).(*Signature)
		got := dropSelf(f.Sig)
		bind := receiverBinders(f.Sig)
		var extra []*TypeParam
		for _, tp := range f.Sig.TParams {
			if !slices.Contains(bind, tp) {
				extra = append(extra, tp)
			}
		}
		var why string
		switch {
		case sigHasInvalid(want):
			why = "re-applies Self outside the receiver's constraints"
		case len(extra) != len(want.TParams):
			why = fmt.Sprintf("%s has %d type parameters beyond its first parameter's, and %s has %d", f.Name, len(extra), f.Name, len(want.TParams))
		default:
			rename := paramMap(extra, paramTypes(want.TParams))
			for i, p := range extra {
				w := want.TParams[i]
				if p.Kind != w.Kind || p.Own != w.Own || !c.sameConstraint(c.subst(p.Constraint, rename), w.Constraint) {
					why = fmt.Sprintf("%s has incompatible type parameter %s", f.Name, p.Name)
					break
				}
			}
			if why == "" {
				got = c.subst(got, rename).(*Signature)
				if _, mut := recv.(*Pointer); mut && !want.MutSelf {
					why = fmt.Sprintf("%s writes its receiver, and the operation does not", f.Name)
				} else {
					why = c.operationShape(recv, f.Name, got, want, got.Results, want.Results, nil, nil)
					if why == "" {
						why = c.operationTakes(recv, f.Name, got, want, recv)
					}
				}
			}
		}
		if why != "" {
			c.errorf(f.Decl.Implements, CodeProvider, "%s does not implement %s: %s", recv, op, why)
		}
	}
}

func (c *checker) functionProvider(recv Type, role *Named, name string, own []Type) (*Func, []Type) {
	g, _ := c.functionClaim(recv, role)
	if g == nil || g.ops[name] == nil {
		return nil, nil
	}
	f := g.ops[name]
	m, ok := c.claimMatch(f.Sig.Params[0].Type, recv, receiverBinders(f.Sig))
	if !ok {
		return nil, nil
	}
	args := make([]Type, len(f.Sig.TParams))
	for i, p := range f.Sig.TParams {
		args[i] = m[p]
		if args[i] == nil && len(own) > 0 {
			args[i], own = own[0], own[1:]
		}
	}
	return f, args
}
