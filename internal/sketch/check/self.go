package check

import (
	"fmt"
	"slices"
	"sync"

	"ergo.local/prototype/internal/sketch/syntax"
)

// isSelfName recognizes the contextual spelling; the caller still decides
// whether the spelling is shadowed in its scope.
func isSelfName(name string) bool { return name == "Self" }

func (c *checker) isContextualSelf(name string, ctx *typeCtx) bool {
	return isSelfName(name) && ctx != nil && ctx.self != nil && ctx.params[name] == nil
}

func (c *checker) selfType(at syntax.Expr, args []syntax.Expr, ctx *typeCtx) Type {
	s := &SelfType{owner: ctx.self, source: ctx.selfSource}
	if args != nil {
		s.Args = c.reapplicationArgs(at, "Self", ctx.self.TParams, args, ctx)
		if s.Args == nil {
			return invalid
		}
	}
	if ctx.selfUses != nil {
		*ctx.selfUses = append(*ctx.selfUses, s)
	}
	return s
}

func (c *checker) reapplicationArgs(at syntax.Expr, name string, ps []*TypeParam, args []syntax.Expr, ctx *typeCtx) []Type {
	if len(args) != len(ps) || len(ps) == 0 {
		c.errorf(at, CodeType, "%s takes %d type arguments when re-applied, not %d", name, len(ps), len(args))
		return nil
	}
	ts := make([]Type, len(args))
	for i, a := range args {
		if ps[i].Kind == KindType {
			ts[i] = c.typExpr(a, ctx)
		} else {
			ts[i] = c.rowArg(a, ps[i].Kind, ctx)
		}
		if ts[i] == nil || containsInvalid(ts[i]) {
			return nil
		}
	}
	return ts
}

func (c *checker) noOperationValue(at syntax.Node, n *Named) {
	it := asInterface(n)
	for _, op := range it.Methods {
		if len(op.Sig.TParams) != 0 {
			c.errorf(at, CodeType, "%s has a generic operation, so it is a constraint and has no values; write [M %s] (types.md, Interface assignability, Generic operations)", n.Obj.Name, n)
			return
		}
	}
	c.errorf(at, CodeType, "%s names Self, so it is a constraint and has no values; write [M %s] (types.md, Interface assignability, Self)", n.Obj.Name, n)
}

// A law is checked after ordinary declarations. Its written type positions
// still make the operation type constraint-only before any value can use it.
func (c *checker) lawsMentionSelf(spec *syntax.InterfaceType, owner *Named) bool {
	return lawSelfSites(spec, owner) != 0
}

// lawSelfSites records declaration-time use, before law bodies are checked:
// 1 names contextual Self, and 2 re-applies it.
func lawSelfSites(spec *syntax.InterfaceType, owner *Named) int {
	if owner == nil || slices.ContainsFunc(owner.TParams, func(p *TypeParam) bool { return isSelfName(p.Name) }) {
		return 0
	}
	sites := 0
	for _, law := range spec.Laws {
		shadowed := false
		if law.TypeParams != nil {
			for _, f := range law.TypeParams.List {
				if slices.ContainsFunc(f.Names, func(id *syntax.Ident) bool { return isSelfName(id.Name) }) {
					shadowed = true
					break
				}
			}
		}
		if shadowed {
			continue
		}
		w := typeWalker{
			f: func(id *syntax.Ident, _ Kind) {
				if isSelfName(id.Name) {
					sites = max(sites, 1)
				}
			},
			index: func(ix *syntax.IndexExpr) bool {
				if id, ok := unparen(ix.X).(*syntax.Ident); ok && isSelfName(id.Name) {
					sites = 2
				}
				return false
			},
		}
		syntax.Inspect(law, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.Field:
				w.walk(n.Type, KindType)
			case *syntax.VarSpec:
				w.walk(n.Type, KindType)
			}
			return sites < 2
		})
		if sites == 2 {
			break
		}
	}
	return sites
}

func mapTypes(types []Type, read func(Type) Type) []Type {
	var out []Type
	for i, t := range types {
		n := read(t)
		if n != t {
			if out == nil {
				out = slices.Clone(types)
			}
			out[i] = n
		}
	}
	if out != nil {
		return out
	}
	return types
}

// selfPositions is the layout of an operation type's claim arguments in the
// claimant. It reads the declaration (not the instance): equal concrete
// arguments at two positions must not make them interchangeable.
func selfPositions(claimant, claim *Named) ([]int, string) {
	return selfPositionsText(claimant, claim, nil)
}

func selfPositionsText(claimant, claim *Named, texts []string) ([]int, string) {
	ps := claimant.origin().TParams
	positions := make([]int, 0, len(claim.TArgs))
	for j, a := range claim.TArgs {
		p, _ := a.(*TypeParam)
		if r, ok := a.(*RowArg); ok {
			if es := r.Row.Elems(); len(es) == 1 {
				p, _ = es[0].(*TypeParam)
			}
		}
		i := slices.Index(ps, p)
		if i < 0 {
			text := a.String()
			if j < len(texts) {
				text = texts[j]
			}
			return nil, fmt.Sprintf("%s re-applies Self, and %s is not a type parameter of %s", claim.origin().Obj.Name, text, claimant.Obj.Name)
		}
		if slices.Contains(positions, i) {
			return nil, fmt.Sprintf("%s re-applies Self, and %s is named more than once in the claim", claim.origin().Obj.Name, p.Name)
		}
		positions = append(positions, i)
	}
	return positions, ""
}

func (c *checker) readClaim(claimant, claim *Named) *Named {
	return c.subst(claim, paramMap(claimant.origin().TParams, claimant.TArgs)).(*Named)
}

func (c *checker) claimTemplate(claimant, op *Named) (*Named, string) {
	var found *Named
	for _, claim := range c.declaredOps(claimant.origin()) {
		if claim.origin() != op.origin() || op.Orig != nil && !c.identical(c.readClaim(claimant, claim), op) {
			continue
		}
		if found != nil {
			return nil, fmt.Sprintf("%s declares more than one claim of %s", claimant, op)
		}
		found = claim
	}
	if found == nil {
		return nil, fmt.Sprintf("%s does not claim %s", claimant, op)
	}
	return found, ""
}

func (c *checker) replaceClaimArgs(claimant *Named, positions []int, args []Type) *Named {
	if len(args) == 0 {
		return claimant
	}
	ts := slices.Clone(claimant.TArgs)
	for i, p := range positions {
		ts[p] = args[i]
	}
	if slices.Equal(ts, claimant.TArgs) {
		return claimant
	}
	return c.instantiate(claimant.origin(), ts)
}

// claimSubst is the shared reading of a claim's arguments and Self at a
// receiver. It retains the declaration's positional mapping.
func (c *checker) claimSubst(claimant, op *Named) (map[*TypeParam]Type, selfReader, string) {
	claim, why := c.claimTemplate(claimant, op)
	if why != "" {
		return nil, nil, why
	}
	m, read := c.claimSubstAt(claimant, claim)
	return m, read, ""
}

func (c *checker) claimSubstAt(claimant, claim *Named) (map[*TypeParam]Type, selfReader) {
	actual := c.readClaim(claimant, claim)
	m := paramMap(claim.origin().TParams, actual.TArgs)
	return m, func(s *SelfType, args []Type) Type {
		if len(args) == 0 {
			return claimant
		}
		template := c.selfClaim(claim, s.owner)
		if template == nil {
			return invalid
		}
		positions, why := selfPositions(claimant, template)
		if why != "" || len(positions) != len(args) || len(claimant.TArgs) != len(claimant.origin().TParams) {
			return invalid
		}
		return c.replaceClaimArgs(claimant, positions, args)
	}
}

func (c *checker) selfClaim(op, owner *Named) *Named {
	if op.origin() == owner {
		return op
	}
	for _, d := range c.declaredOps(op) {
		if d.origin() == owner {
			return d
		}
	}
	return nil
}

func (c *checker) writtenType(x syntax.Expr, fallback Type) string {
	src := c.cur.syntax.Src
	if x.Pos().IsValid() && int(x.End()) <= len(src) {
		return src[x.Pos():x.End()]
	}
	if fallback != nil {
		return fallback.String()
	}
	return syntaxText(x)
}

func (c *checker) selfClaimWhy(claimant, op *Named, at syntax.Expr) string {
	it := asInterface(op)
	var texts []string
	if ix, ok := unparen(at).(*syntax.IndexExpr); ok {
		for _, arg := range ix.Indices {
			texts = append(texts, c.writtenType(arg, nil))
		}
	}
	if reappliesSelf(op) {
		if _, why := selfPositionsText(claimant, op, texts); why != "" {
			return why
		}
	}
	var uses []*SelfType
	uses = append(uses, it.selfUses...)
	for _, d := range append([]*Named{op}, c.declaredOps(op)...) {
		m := paramMap(d.origin().TParams, d.TArgs)
		for _, law := range d.Obj.Laws {
			for _, s := range law.selfUses {
				uses = append(uses, c.subst(s, m).(*SelfType))
			}
		}
	}
	for _, s := range uses {
		if len(s.Args) == 0 {
			continue
		}
		claim := c.selfClaim(op, s.owner)
		if claim == nil {
			continue
		}
		positions, why := selfPositions(claimant, claim)
		if why != "" {
			return why
		}
		reapplied := c.replaceClaimArgs(claimant, positions, s.Args)
		if p, a := c.reapplicationUnmet(reapplied); p != nil {
			description := a.String()
			if t, ok := a.(*TypeParam); ok {
				description = fmt.Sprintf("%s's %s is %s", s.source, t.Name, constraintText(t))
			}
			return fmt.Sprintf("%s, and %s's %s is %s", description, claimant.Obj.Name, p.Name, constraintText(p))
		}
	}
	return ""
}

// Checking a substituted instance needs the bound, not the operation bodies:
// the nominal declaration was checked at its own implements clause.
func (c *checker) reapplicationUnmet(n *Named) (*TypeParam, Type) {
	m := paramMap(n.origin().TParams, n.TArgs)
	for i, p := range n.origin().TParams {
		a := n.TArgs[i]
		cons := c.subst(p.Constraint, m)
		if p.Kind == KindType && !p.Own && c.mayHoldResource(a) || cons != nil && !c.meetsReapplicationBound(a, cons) {
			return p, a
		}
	}
	return nil, nil
}

func (c *checker) meetsReapplicationBound(a, cons Type) bool {
	it := asInterface(cons)
	if it == nil {
		return true
	}
	if it.Set == SetWire {
		return c.wireReason(a) == ""
	}
	if it.Set != SetAll {
		return inSet(a, it.Set)
	}
	return (it.Terms == nil || c.inTypeSet(a, it.Terms)) && (len(it.Methods) == 0 || c.declares(a, cons))
}

func (c *checker) reappliedType(x *syntax.IndexExpr, p *TypeParam, ctx *typeCtx) Type {
	op, _ := p.Constraint.(*Named)
	if op == nil || !reappliesSelf(op) {
		c.errorf(x, CodeType, "%s is not a generic type; only a parameter constrained by an operation type that re-applies Self is re-applied", p)
		return invalid
	}
	args := c.reapplicationArgs(x, p.Name, op.origin().TParams, x.Indices, ctx)
	if args == nil {
		return invalid
	}
	newOp := c.instantiate(op.origin(), args)
	if q, a := c.reapplicationUnmet(newOp); q != nil {
		c.errorf(x, CodeType, "%s does not satisfy %s when re-applying %s: %s", a, q.Constraint, p, q.Name)
		return invalid
	}
	return &Reapplied{Param: p, Args: args}
}

func reappliesSelf(op *Named) bool {
	it := asInterface(op)
	if it == nil || !it.ConstraintOnly {
		return false
	}
	if slices.ContainsFunc(it.selfUses, func(use *SelfType) bool { return len(use.Args) != 0 }) {
		return true
	}
	origin := op.origin()
	if origin.Obj.Decl != nil && len(origin.Obj.Laws) != 0 {
		if spec, ok := origin.Obj.Decl.Spec.(*syntax.InterfaceType); ok && lawSelfSites(spec, origin) == 2 {
			return true
		}
	}
	return slices.ContainsFunc(it.includes, reappliesSelf)
}

func (c *checker) reapply(t Type, param *TypeParam, args []Type) Type {
	switch t := t.(type) {
	case *TypeParam:
		return &Reapplied{Param: t, Args: args}
	case *Reapplied:
		return &Reapplied{Param: t.Param, Args: args}
	case *SelfType:
		return &SelfType{owner: t.owner, source: t.source, Args: args}
	case *Named:
		op, _ := param.Constraint.(*Named)
		if op == nil {
			return invalid
		}
		for _, actual := range c.declaredOps(t) {
			if actual.origin() != op.origin() {
				continue
			}
			claim, why := c.claimTemplate(t, actual)
			if why != "" {
				return invalid
			}
			positions, why := selfPositions(t, claim)
			if why != "" || len(positions) != len(args) {
				return invalid
			}
			return c.replaceClaimArgs(t, positions, args)
		}
	}
	return invalid
}

// receiverOperationType is the operation type at the receiver's current
// arguments, before a selected operation's own parameters are bound.
func (c *checker) receiverOperationType(t Type) *Named {
	switch t := t.(type) {
	case *TypeParam:
		n, _ := t.Constraint.(*Named)
		return n
	case *Reapplied:
		if n := c.receiverOperationType(t.Param); n != nil {
			return c.instantiate(n.origin(), t.Args)
		}
	case *SelfType:
		if len(t.Args) != 0 {
			return c.instantiate(t.owner, t.Args)
		}
		if len(t.owner.TParams) != 0 {
			ts := make([]Type, len(t.owner.TParams))
			for i, p := range t.owner.TParams {
				ts[i] = p
			}
			return c.instantiate(t.owner, ts)
		}
		return t.owner
	}
	return nil
}

func (c *checker) abstractSelf(t Type) selfReader {
	return func(s *SelfType, args []Type) Type {
		if len(args) == 0 {
			return t
		}
		op := c.receiverOperationType(t)
		if op.origin() != s.owner {
			var ps []Type
			for _, p := range op.origin().TParams {
				ps = append(ps, p)
			}
			template := c.selfClaim(c.instantiate(op.origin(), ps), s.owner)
			if template == nil {
				return invalid
			}
			positions, why := selfPositions(op, template)
			if why != "" {
				return invalid
			}
			next := slices.Clone(op.TArgs)
			for i, p := range positions {
				next[p] = args[i]
			}
			args = next
		}
		switch t := t.(type) {
		case *TypeParam:
			return &Reapplied{Param: t, Args: args}
		case *Reapplied:
			return &Reapplied{Param: t.Param, Args: args}
		case *SelfType:
			return &SelfType{owner: t.owner, source: t.source, Args: args}
		}
		return invalid
	}
}

// Standalone law readings use interning owned by the claimant's package,
// not by whichever caller happens to request a law instance.
var lawSelfMu sync.Mutex

func (p *Package) claimReader() *checker {
	if p.lawSelf == nil {
		p.lawSelf = newChecker(p.Path, nil, p.Scope)
		p.lawSelf.pkg = p
	}
	return p.lawSelf
}

// CopyReader supplies the checker's types at a copy's bindings. It keeps
// instance identity in the package which owns the copied declaration.
func CopyReader(p *Package, bound map[*TypeParam]Type) func(Type) Type {
	return func(t Type) Type {
		lawSelfMu.Lock()
		defer lawSelfMu.Unlock()
		return p.claimReader().subst(t, bound)
	}
}

// CopyInfo reads the expression and call facts of a closed copy. Other
// facts retain declaration identity, notably the local variables shared
// by definitions and uses. A type read from those facts uses CopyReader.
func CopyInfo(base *Info, root syntax.Node, read func(Type) Type) (*Info, string) {
	out := *base
	out.Types = make(map[syntax.Expr]Type)
	out.Calls = make(map[*syntax.CallExpr]*Call)
	out.Tails = make(map[syntax.Stmt]Type)
	out.Literals = make(map[syntax.Node]*Signature)
	out.Instances = make(map[syntax.Expr]*Instance)
	out.Implicit = make(map[syntax.Expr]Conversion)
	out.ImplicitElems = make(map[syntax.Expr][]Conversion)
	out.Patterns = make(map[syntax.Expr]*Pattern)
	out.Matches = make(map[syntax.Node]*Match)
	var pattern func(*Pattern) *Pattern
	pattern = func(p *Pattern) *Pattern {
		if p == nil {
			return nil
		}
		next := *p
		next.Type = read(p.Type)
		next.Elems, next.Fields = slices.Clone(p.Elems), slices.Clone(p.Fields)
		for i := range next.Elems {
			next.Elems[i] = pattern(next.Elems[i])
		}
		for i := range next.Fields {
			next.Fields[i].Pattern = pattern(next.Fields[i].Pattern)
		}
		return &next
	}
	conv := func(v Conversion) Conversion {
		v.From, v.To = read(v.From), read(v.To)
		return v
	}
	why := ""
	syntax.Inspect(root, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		if x, ok := n.(syntax.Expr); ok {
			if t := base.Types[x]; t != nil {
				out.Types[x] = read(t)
			}
			if inst := base.Instances[x]; inst != nil {
				copy := *inst
				copy.TypeArgs = mapTypes(inst.TypeArgs, read)
				out.Instances[x] = &copy
			}
			if v, ok := base.Implicit[x]; ok {
				out.Implicit[x] = conv(v)
			}
			if vs := base.ImplicitElems[x]; len(vs) != 0 {
				cp := make([]Conversion, len(vs))
				for i, v := range vs {
					cp[i] = conv(v)
				}
				out.ImplicitElems[x] = cp
			}
			if p := base.Patterns[x]; p != nil {
				out.Patterns[x] = pattern(p)
			}
		}
		if s, ok := n.(syntax.Stmt); ok && base.Tails[s] != nil {
			out.Tails[s] = read(base.Tails[s])
		}
		if sig := base.Literals[n]; sig != nil {
			out.Literals[n] = read(sig).(*Signature)
		}
		if m := base.Matches[n]; m != nil {
			cp := *m
			cp.Subjects, cp.Result = mapTypes(m.Subjects, read), read(m.Result)
			out.Matches[n] = &cp
		}
		x, ok := n.(*syntax.CallExpr)
		if !ok || base.Calls[x] == nil {
			return true
		}
		src := base.Calls[x]
		next := *src
		next.Type = read(src.Type)
		next.TypeArgs = mapTypes(src.TypeArgs, read)
		if src.Fails != nil {
			next.Fails = read(&RowArg{Row: src.Fails}).(*RowArg).Row
		}
		if src.Needs != nil {
			next.Needs = read(&RowArg{Row: src.Needs}).(*RowArg).Row
		}
		// A closed constraint call is a call of the provider, not an
		// operation value (which cannot exist for this constraint).
		if src.Kind == CallOp && src.Recv != nil && src.Op != nil {
			op := *src.Op
			op.Sig = read(op.Sig).(*Signature)
			next.Op = &op
			recv := base.Types[src.Recv]
			if p, ok := recv.(*Pointer); ok {
				recv = p.Elem
			}
			tp, _ := recv.(*TypeParam)
			if r, ok := recv.(*Reapplied); ok {
				tp = r.Param
			}
			_, symbolicSelf := recv.(*SelfType)
			if symbolicSelf || tp != nil && ConstraintOnly(tp.Constraint) {
				self := read(recv)
				f := ProvidingFunc(self, src.Op.Name)
				if f == nil {
					why = fmt.Sprintf("closed constraint %s has no implementation of %s", self, src.Op.Name)
					return false
				}
				next.Kind, next.Func, next.Op = CallFunc, f, nil
				next.TypeArgs = ProviderTypeArgs(self, f, next.TypeArgs)
				if len(next.TypeArgs) != 0 {
					out.Instances[x] = &Instance{Func: f, TypeArgs: next.TypeArgs}
				}
			}
		} else if (src.Kind == CallProviderOp || src.Kind == CallLawOp) && src.Op != nil {
			op := *src.Op
			op.Sig = read(op.Sig).(*Signature)
			next.Op = &op
		}
		out.Calls[x] = &next
		return true
	})
	return &out, why
}

// SubstSelf reads a member law's types at a named claimant. It does not run
// the law or look up the functions that provide its operations.
func SubstSelf(law *Law, claimant *Named, projection ...*Named) (func(Type) Type, string) {
	if law == nil || law.Owner == nil || claimant == nil || claimant.Obj == nil || claimant.Obj.Pkg == nil {
		return nil, "Self needs a member law and a named claimant"
	}
	op, ok := law.Owner.Type.(*Named)
	if !ok || asInterface(op) == nil {
		return nil, "Self needs a member law of an operation type"
	}
	pkg := claimant.Obj.Pkg
	lawSelfMu.Lock()
	defer lawSelfMu.Unlock()
	c := pkg.claimReader()
	var m map[*TypeParam]Type
	var self selfReader
	if len(projection) == 1 && projection[0] != nil && projection[0].origin() == op {
		m, self = c.claimSubstAt(claimant, projection[0])
	} else {
		var why string
		m, self, why = c.claimSubst(claimant, op)
		if why != "" {
			return nil, why
		}
	}
	for _, s := range law.selfUses {
		if n, ok := c.substWith(s, m, self).(*Named); !ok {
			return nil, fmt.Sprintf("cannot read %s at %s", s, claimant)
		} else if p, a := c.reapplicationUnmet(n); p != nil {
			return nil, fmt.Sprintf("%s does not satisfy %s of %s's %s", a, p.Constraint, claimant.Obj.Name, p.Name)
		}
	}
	return func(t Type) Type {
		lawSelfMu.Lock()
		defer lawSelfMu.Unlock()
		return c.substWith(t, m, self)
	}, ""
}
