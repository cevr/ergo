package check

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Claimed reports whether a member law takes concrete claimants instead
// of a provider under test. Such a contract has no runtime values.
func (l *Law) Claimed() bool {
	return l.Owner != nil && ConstraintOnly(l.Owner.Type)
}

// ClaimEquation uses the function-rewrite semantics, not a provider
// window: there is no installed provider to call without a receiver.
func (l *Law) ClaimEquation() bool {
	return l.Claimed() && l.Decl != nil && l.Decl.Rewrite != nil
}

func (c *checker) checkClaimEquation(l *Law) {
	left, right := l.Decl.RewriteSides()
	if guard := l.Decl.RewriteGuard(); guard != nil {
		c.errorf(guard, CodeType, "a rewrite over functions has no guard; write { lhs == rhs } for a property")
	}
	for _, b := range []*syntax.BlockStmt{left, right} {
		if _, ok := oneClaimSide(b); !ok {
			c.errorf(b, CodeType, "a claim rewrite has one expression on each side")
		} else {
			c.yieldList(b.List, &sink{what: "the rewrite side's value"}, b.Rbrace)
		}
	}
}

func oneClaimSide(b *syntax.BlockStmt) (syntax.Expr, bool) {
	if b != nil && len(b.List) == 1 {
		if s, ok := b.List[0].(*syntax.ExprStmt); ok {
			return s.X, true
		}
	}
	return nil, false
}

// Operands reads a call in evaluation order, including a selected
// receiver before its explicit arguments.
func (c *Call) Operands(x *syntax.CallExpr) []syntax.Expr {
	if c.Recv == nil {
		return x.Args
	}
	return append([]syntax.Expr{c.Recv}, x.Args...)
}

// ClaimantType is the concrete sampled type of a claim law binding
// (nominal At, or function-form ClaimAt).
func (p LawProvider) ClaimantType() Type {
	if p.At != nil {
		return p.At
	}
	return p.ClaimAt
}

// ClaimantDeclType is the declaring type of a claim law binding
// (nominal Type, or function-form ClaimType).
func (p LawProvider) ClaimantDeclType() Type {
	if p.Type != nil {
		return p.Type
	}
	return p.ClaimType
}

// ClaimantPkg is the package that declares the claim.
func (p LawProvider) ClaimantPkg() *Package {
	if p.Type != nil {
		return p.Type.Obj.Pkg
	}
	if p.ClaimHead != nil {
		return p.ClaimHead.Pkg
	}
	return nil
}

// ClaimantTitle is the report's name for the claimant relative to pkg.
func (p LawProvider) ClaimantTitle(pkg *Package) string {
	if p.Type != nil {
		if q := p.Type.Obj.Pkg; q != nil && q != pkg {
			return q.Name + "." + p.Type.Obj.Name
		}
		return p.Type.Obj.Name
	}
	if p.ClaimType != nil {
		return p.ClaimType.String()
	}
	return ""
}

// ClaimantTParams are the claimant's declaration type parameters
// (nominal Type.TParams, or function-form receiver binders).
func (p LawProvider) ClaimantTParams() []*TypeParam {
	if p.Type != nil {
		return p.Type.TParams
	}
	if p.ClaimHead != nil && p.ClaimHead.Sig != nil {
		return receiverBinders(p.ClaimHead.Sig)
	}
	return nil
}

// HasDistinctInstance reports whether the sampled instance differs from the
// declaring claimant spelling (e.g. Tree at Tree[int], []T at []int).
func (p LawProvider) HasDistinctInstance() bool {
	if p.Claim == nil {
		return false
	}
	if p.Type != nil {
		return p.At != p.Type
	}
	return p.ClaimAt != nil && p.ClaimType != nil && p.ClaimAt.String() != p.ClaimType.String()
}

// Subst reads the law's types at this binding. The persisted inputs,
// rather than a closure, are the snapshot representation of this reader.
func (p LawProvider) Subst(law *Law) (func(Type) Type, string) {
	slot := p.ClaimSlot
	if p.At == nil && p.Slot != nil {
		slot = p.Slot
	}
	read, why := SubstSelf(law, p.ClaimantType(), slot)
	if why != "" || len(p.LawTypeArgs) == 0 {
		return read, why
	}
	at := CopyReader(law.Pkg, paramMap(law.TParams, p.LawTypeArgs))
	return func(t Type) Type { return read(at(t)) }, ""
}

// InstanceTitle is the reported sample type, relative to the claim.
func (p LawProvider) InstanceTitle() string {
	if p.At != nil {
		return strings.TrimPrefix(p.At.String(), p.At.Obj.Pkg.Path+".")
	}
	if p.ClaimAt != nil {
		return p.ClaimAt.String()
	}
	return ""
}

// ClaimBlame points to the written claim, not the projected contract.
func (p LawProvider) ClaimBlame() string {
	if p.ClaimExpr == nil {
		return ""
	}
	if p.Type != nil {
		if p.Type.Obj.file == nil {
			return ""
		}
		f := p.Type.Obj.file.syntax
		line, col := f.Position(p.ClaimExpr.Pos())
		file := f.Name
		if file == "" {
			file = path.Base(p.Type.Obj.Pkg.Path) + ".ergo"
		}
		text := f.Src[p.ClaimExpr.Pos():p.ClaimExpr.End()]
		return fmt.Sprintf("; %s claims %s at %s:%d:%d", p.Type.Obj.Name, text, file, line, col)
	}
	if p.ClaimType == nil || p.ClaimHead == nil || p.ClaimHead.file == nil {
		return ""
	}
	f := p.ClaimHead.file.syntax
	line, col := f.Position(p.ClaimExpr.Pos())
	file := f.Name
	if file == "" {
		file = path.Base(p.ClaimHead.Pkg.Path) + ".ergo"
	}
	text := f.Src[p.ClaimExpr.Pos():p.ClaimExpr.End()]
	return fmt.Sprintf("; %s claims %s at %s:%d:%d", p.ClaimType.String(), text, file, line, col)
}

// claimBindings runs inherited laws at the law's declaring slot. A claim
// visible only through an import never acquires a second local run.
func (c *checker) claimBindings(law *Law) (out [][]LawProvider) {
	own := law.Owner.Type.(*Named)
	for _, obj := range c.pkg.Types {
		p, ok := obj.Type.(*Named)
		if !ok || p.Obj != obj || len(p.impls) == 0 || asInterface(p) != nil {
			continue
		}
		var seen []*Named
		for i, root := range p.impls {
			for _, slot := range append([]*Named{root}, c.declaredOps(root)...) {
				if slot.origin() != own || slices.ContainsFunc(seen, func(s *Named) bool { return c.identical(s, slot) }) {
					continue
				}
				seen = append(seen, slot)
				b := LawProvider{Type: p, Slot: slot, ClaimSlot: slot, Claim: root, ClaimExpr: p.implExprs[i]}
				c.claimBinding(law, &b)
				out = append(out, []LawProvider{b})
			}
		}
	}
	if own.Obj != nil && own.Obj.Pkg == c.pkg && len(c.pkg.functionClaims[own]) > 0 {
		var seen []*functionClaim
		for _, g := range c.pkg.functionClaims[own] {
			if slices.ContainsFunc(seen, func(prev *functionClaim) bool { return c.sameFunctionClaim(prev, g) }) {
				continue
			}
			seen = append(seen, g)
			root := g.role
			if g.head != nil && g.head.Decl != nil && g.head.Decl.Implements != nil {
				if r, ok := c.pkg.Info.Types[g.head.Decl.Implements].(*Named); ok {
					root = r
				}
			}
			var claimExpr syntax.Expr
			if g.head != nil && g.head.Decl != nil {
				claimExpr = g.head.Decl.Implements
			}
			b := LawProvider{
				ClaimType: g.recv,
				ClaimHead: g.head,
				Slot:      g.role,
				ClaimSlot: g.role,
				Claim:     root,
				ClaimExpr: claimExpr,
			}
			c.functionClaimBinding(law, g, &b)
			out = append(out, []LawProvider{b})
		}
	}
	return out
}

func (c *checker) lawSample(ps []*TypeParam) ([]Type, string) {
	ts := make([]Type, len(ps))
	for i, p := range ps {
		if p.Kind == KindType {
			ts[i] = Typ[Int]
		} else {
			ts[i] = &RowArg{Row: newRow()}
		}
	}
	m := paramMap(ps, ts)
	for i, p := range ps {
		if p.Constraint == nil {
			continue
		}
		bound := c.subst(p.Constraint, m)
		if why := c.satisfies(ts[i], bound); why != "" {
			return nil, fmt.Sprintf("sample %s does not satisfy %s of %s: %s", ts[i], bound, p.Name, why)
		}
	}
	return ts, ""
}

func (c *checker) claimBinding(law *Law, b *LawProvider) {
	at, why := c.lawSample(b.Type.TParams)
	if why != "" {
		b.Unrun, b.noGenerator = why, !law.ClaimEquation()
		return
	}
	b.At = b.Type
	if len(at) != 0 {
		b.At = c.instantiate(b.Type, at)
	}
	b.Slot = c.readClaim(b.At, b.Slot)
	if it := asInterface(b.Slot); it != nil {
		var held captured
		_ = c.implementsAt(&Pointer{Elem: b.At}, it, true, &held, b.Slot)
		if held.ambient != nil {
			b.Unrun = "its operations require Ambient, which a law case does not hold"
			b.noGenerator = !law.ClaimEquation()
			return
		}
		b.Native = held.foreign
	}
	b.LawTypeArgs, why = c.lawSample(law.TParams)
	if why == "" {
		var read func(Type) Type
		read, why = b.Subst(law)
		if why == "" {
			b.Params = make([]Type, len(law.Params))
			var builtWhy string
			for i, p := range law.Params {
				t := read(p.Type)
				b.Params[i] = t
				if sig, ok := t.(*Signature); ok {
					if len(sig.Results) != 1 {
						if law.ClaimEquation() {
							continue // report the missing family in the licence run
						}
						why = "its callback " + p.Name + " has no pure generated family"
						break
					}
					if !law.ClaimEquation() && !ClaimCallback(sig) {
						why = "its callback " + p.Name + " has no pure generated family"
						break
					}
					t = sig.Results[0]
					if !law.ClaimEquation() {
						if n, ok := t.(*Named); ok && n.origin() == b.Type {
							if only, whyBuilt := c.claimConstructor(n, b); only && whyBuilt != "" {
								why = "its callback " + p.Name + " cannot draw a result: " + whyBuilt
								break
							}
						}
					}
				}
				from := law.Pkg
				if n, ok := t.(*Named); ok && n.origin() == b.Type {
					from = b.Type.Obj.Pkg
					if built, missing := c.claimConstructor(n, b); built {
						if missing == "" {
							continue
						}
						if law.ClaimEquation() || b.ClaimFeed != 0 || !c.identical(n, b.At) {
							why = missing
							b.ClaimFeed = 0
							break
						}
						b.ClaimFeed, builtWhy = i+1, missing
						continue
					}
				}
				if s := c.ungeneratableFrom(t, from); s != "" {
					why = "its parameter " + p.Name + " has no generator: " + s
					break
				}
			}
			if why == "" && !law.ClaimEquation() && law.Decl != nil && law.Decl.Body != nil && law.Pkg != nil && law.Pkg.Info != nil {
				syntax.Inspect(law.Decl.Body, func(n syntax.Node) bool {
					if why != "" {
						return false
					}
					be, ok := n.(*syntax.BinaryExpr)
					if !ok || (be.Op != syntax.EQL && be.Op != syntax.NEQ) {
						return true
					}
					if lt := law.Pkg.Info.Types[be.X]; lt != nil {
						if ct := read(lt); !isInvalid(ct) && !Equatable(ct) {
							why = fmt.Sprintf("%s values cannot be compared with %s", ct, be.Op)
							return false
						}
					}
					return true
				})
			}
			if why == "" {
				why = builtWhy
			} else {
				b.ClaimFeed = 0
			}
		}
	}
	if why != "" {
		b.Unrun, b.noGenerator = why, !law.ClaimEquation()
	}
}

func (c *checker) functionClaimBinding(law *Law, g *functionClaim, lp *LawProvider) {
	at, why := c.lawSample(g.bind)
	if why != "" {
		lp.Unrun, lp.noGenerator = why, !law.ClaimEquation()
		return
	}
	m := paramMap(g.bind, at)
	lp.ClaimAt = c.subst(g.recv, m)
	lp.Slot = c.subst(lp.ClaimSlot, m).(*Named)
	if it := asInterface(lp.Slot); it != nil {
		var held captured
		for _, op := range it.Methods {
			fn, _ := c.functionProvider(lp.ClaimAt, lp.Slot, op.Name, nil)
			if fn != nil && fn.Sig != nil {
				held.add(fn.Sig.Uses)
			}
		}
		if held.ambient != nil {
			lp.Unrun = "its operations require Ambient, which a law case does not hold"
			lp.noGenerator = !law.ClaimEquation()
			return
		}
		lp.Native = held.foreign
	}
	lp.LawTypeArgs, why = c.lawSample(law.TParams)
	if why == "" {
		var read func(Type) Type
		read, why = lp.Subst(law)
		if why == "" {
			lp.Params = make([]Type, len(law.Params))
			for i, p := range law.Params {
				t := read(p.Type)
				lp.Params[i] = t
				if sig, ok := t.(*Signature); ok {
					if len(sig.Results) != 1 {
						if law.ClaimEquation() {
							continue
						}
						why = "its callback " + p.Name + " has no pure generated family"
						break
					}
					if !law.ClaimEquation() && !ClaimCallback(sig) {
						why = "its callback " + p.Name + " has no pure generated family"
						break
					}
					t = sig.Results[0]
				}
				if s := c.ungeneratableFrom(t, law.Pkg); s != "" {
					why = "its parameter " + p.Name + " has no generator: " + s
					break
				}
			}
			if why == "" && !law.ClaimEquation() && law.Decl != nil && law.Decl.Body != nil && law.Pkg != nil && law.Pkg.Info != nil {
				syntax.Inspect(law.Decl.Body, func(n syntax.Node) bool {
					if why != "" {
						return false
					}
					be, ok := n.(*syntax.BinaryExpr)
					if !ok || (be.Op != syntax.EQL && be.Op != syntax.NEQ) {
						return true
					}
					if lt := law.Pkg.Info.Types[be.X]; lt != nil {
						if ct := read(lt); !isInvalid(ct) && !Equatable(ct) {
							why = fmt.Sprintf("%s values cannot be compared with %s", ct, be.Op)
							return false
						}
					}
					return true
				})
			}
		}
	}
	if why != "" {
		lp.Unrun, lp.noGenerator = why, !law.ClaimEquation()
	}
}

// ClaimCallback is the checked shape of a callback drawn for a claim
// property. Results are independently drawable; the callback has no
// authority and uses up to two integer operands to select its result.
func ClaimCallback(sig *Signature) bool {
	if sig == nil || len(sig.Params) == 0 || len(sig.Params) > 2 || len(sig.Results) != 1 || rowHolds(sig.Errors) || rowHolds(sig.Uses) {
		return false
	}
	for _, p := range sig.Params {
		if p.Type != Typ[Int] || p.Mut() {
			return false
		}
	}
	return true
}

// claimConstructor recognizes a private record with an actual constructor
// of this instance. Infer from the result: the declaration may reorder
// or fix type arguments, and other instances are not constructors here.
func (c *checker) claimConstructor(n *Named, b *LawProvider) (built bool, why string) {
	s, ok := n.Underlying().(*Struct)
	if !ok || !slices.ContainsFunc(s.Fields, func(f *Field) bool { return !isExported(f.Name) }) {
		return false, ""
	}
constructors:
	for _, f := range n.Obj.Pkg.Funcs {
		sig := f.Sig
		if sig == nil || len(sig.Results) != 1 {
			continue
		}
		r, ok := sig.Results[0].(*Named)
		if !ok || r.origin() != n.origin() {
			continue
		}
		in := &inferer{c: c, set: map[*TypeParam]bool{}, m: map[*TypeParam]Type{}}
		for _, tp := range sig.TParams {
			in.set[tp] = true
		}
		if !in.unify(r, n, unifyExact) {
			continue
		}
		var args, params []Type
		drawable := true
		for _, tp := range sig.TParams {
			a := in.m[tp]
			if a == nil || tp.Constraint != nil && c.satisfies(a, c.subst(tp.Constraint, in.m)) != "" {
				drawable = false
			}
			args = append(args, a)
		}
		for _, p := range sig.Params {
			t := c.subst(p.Type, in.m)
			ptr, recv := under(t).(*Pointer)
			if c.identical(t, n) || recv && c.identical(ptr.Elem, n) {
				continue constructors // this function needs a Self to make a Self
			}
			params = append(params, t)
		}
		built = true
		if why == "" {
			why = n.String() + " is built only by its constructor " + f.Name + ", which a law case cannot build from draws"
		}
		uses := c.subst(&RowArg{Row: sig.Uses}, in.m).(*RowArg).Row
		errs := c.subst(&RowArg{Row: sig.Errors}, in.m).(*RowArg).Row
		native := goForeignRow(uses)
		if drawable && !rowHolds(errs) && (!rowHolds(uses) || native) {
			for _, p := range params {
				if c.ungeneratableFrom(p, n.Obj.Pkg) != "" {
					drawable = false
					break
				}
			}
			if drawable {
				b.ClaimCtor, b.ClaimCtorArgs, b.ClaimCtorParams = f, args, params
				b.Native = b.Native || native
				return true, ""
			}
		}
	}
	return built, why
}

// ClaimInfo is the binding-local copy of a law's checked facts.
func (p LawProvider) ClaimInfo(law *Law) (*Info, string) {
	read, why := p.Subst(law)
	if why != "" {
		return nil, why
	}
	return CopyInfo(law.Pkg.Info, law.Decl, read)
}

// ClaimRewriteBinding is non-nil only on the transient rewrite view.
func (l *Law) ClaimRewriteBinding() *LawProvider { return l.rewriteClaim }

func (l *Law) RewritePackage() *Package {
	if l.rewriteClaim != nil {
		return l.rewriteClaim.ClaimantPkg()
	}
	return l.Pkg
}

func (l *Law) RewriteInfo() *Info {
	if l.rewriteInfo != nil {
		return l.rewriteInfo
	}
	return l.Pkg.Info
}

func (l *Law) RewriteType(t Type) Type {
	if l.rewriteRead != nil {
		return l.rewriteRead(t)
	}
	return t
}

func (l *Law) rewriteSides() (syntax.Expr, syntax.Expr) {
	if l.ClaimEquation() {
		left, right := l.Decl.RewriteSides()
		a, _ := oneClaimSide(left)
		b, _ := oneClaimSide(right)
		return a, b
	}
	a, b, _ := l.Decl.EquationSides()
	return a, b
}

// claimRewriteView keeps the claimant's declaration parameters symbolic
// for matching sites. The licence run uses ClaimInfo at the int sample.
func claimRewriteView(l *Law, p *LawProvider) (*Law, string) {
	var at Type
	var declParams []*TypeParam
	if p.Type != nil {
		origin := p.Type
		atNamed := origin
		if len(origin.TParams) != 0 {
			args := make([]Type, len(origin.TParams))
			for i, tp := range origin.TParams {
				args[i] = tp
				if tp.Kind != KindType {
					args[i] = &RowArg{Row: newRow().with(origin.ctx.types, tp)}
				}
			}
			lawSelfMu.Lock()
			atNamed = origin.Obj.Pkg.claimReader().instantiate(origin, args)
			lawSelfMu.Unlock()
		}
		at = atNamed
		declParams = origin.TParams
	} else if p.ClaimType != nil {
		at = p.ClaimType
		if p.ClaimHead != nil && p.ClaimHead.Sig != nil {
			declParams = receiverBinders(p.ClaimHead.Sig)
		}
	} else {
		return nil, "claim rewrite has no claimant"
	}
	read, why := SubstSelf(l, at, p.ClaimSlot)
	if why != "" {
		return nil, why
	}
	info, why := CopyInfo(l.Pkg.Info, l.Decl, read)
	if why != "" {
		return nil, why
	}
	view := *l
	view.rewriteClaim, view.rewriteInfo, view.rewriteRead = p, info, read
	view.FuncsRewrite = p.ClaimRewrite
	params := append(slices.Clone(declParams), l.TParams...)
	view.TParams = params
	return &view, ""
}

// FunctionRewrites visits the checked rewrites declared in p. Imported
// declarations get evidence in the package of their claim, not the role.
func (p *Package) FunctionRewrites() []*Law {
	var out []*Law
	for _, l := range p.Laws {
		if l.TopRewrite() != nil {
			out = append(out, l)
		}
	}
	for _, r := range p.LawRuns {
		if !r.Law.ClaimEquation() {
			continue
		}
		for _, b := range r.Bindings {
			if b[0].ClaimRewrite != nil {
				if b[0].ClaimRewrite.Root == nil {
					v := *r.Law
					v.rewriteClaim, v.FuncsRewrite = &b[0], b[0].ClaimRewrite
					out = append(out, &v)
					continue
				}
				if v, why := claimRewriteView(r.Law, &b[0]); why == "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func (c *checker) checkClaimRewrites() {
	for _, r := range c.pkg.LawRuns {
		l := r.Law
		if !l.ClaimEquation() {
			continue
		}
		for _, binding := range r.Bindings {
			b := &binding[0]
			// A failed sample remains a licence obligation. Its report
			// needs an identity and a reason, not an invented instance.
			rw := &FuncRewrite{OverFuncs: true}
			if b.Unrun == "" {
				view, why := claimRewriteView(l, b)
				if why == "" {
					rw, _, why = c.funcRewrite(view)
				}
				if why != "" {
					if b.Type != nil {
						c.cur = b.Type.Obj.file
					} else if b.ClaimHead != nil {
						c.cur = b.ClaimHead.file
					}
					c.errorf(b.ClaimExpr, CodeType, "law %s is no rewrite at %s: %s%s", l.Title(c.pkg), b.ClaimantTitle(c.pkg), why, b.ClaimBlame())
					continue
				}
			}
			b.ClaimRewrite = rw
		}
	}
}
