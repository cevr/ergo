package check

import (
	"slices"
	"sort"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Laws (196l; spec D-E4; law-top; #162 law-run-1). A law is checked here
// and run by ergo test (rules 7 to 9). Its placement sets what it is
// quantified over: a top-level
// law over its parameters; a member of an operation type over its
// parameters and every provider of its type. Both placements take one
// checker path (checkLaw); they differ only in the operation scope of rule
// 3. Each rule is an invariant over its whole domain:
//
//  1. A law declares no value: it is never entered in a value scope, so a
//     use of its name resolves as any undeclared name does (S104), and its
//     type has no member of that name (S104). A top-level law's name is in
//     the namespace of the package's tests (tests.go, claimTestName): a
//     second top-level law of one name, or one named like an earlier test,
//     is S108 at the second. A member law's name is in its type's own
//     namespace: a second law of one name in one type is S108 at the
//     second, and so is a law named like an operation of its type. A type
//     with laws and no operations is S108 once, at its name, with the fix
//     "write it as a top-level law"; its laws are still checked, with no
//     operation scope, so their bodies report their own errors (D3).
//  2. A law's body is a failure target: an escaping failure refutes the
//     law, so its failure row is inferred and unbounded (Law.Fails). Its
//     service row, after its own with blocks, is empty for a top-level law
//     and holds only its own operation type for a member law of a
//     non-generic type (Law.capability): a run binds that capability to
//     the provider under test, so a helper that needs it runs. Any other
//     charging call that no with of the law discharges is S102.
//  3. Its scope is its parameters, then, for a member law, the operations
//     of its type, then the package. An operation therefore wins over a
//     package function of its name, and no binder of a member law may take
//     an operation's name (D-D2: the operation scope encloses the law's
//     body, so shadowed refuses it). A top-level law has no operation
//     scope, so rules 4 and 6 have nothing to apply to.
//  4. An operation is call-only in a law: a call of it is checked against
//     the operation's signature and recorded as CallLawOp, whose failures
//     join the law's row and which charges nothing; any other use is S108.
//  5. The body ends in a bool: a tail whose type's underlying type is not
//     bool is S108 once, at the tail; a body that can end with no value is
//     S108 at the end of that path. A fallible tail must be tried (S105), as
//     every value position's. An invalid tail adds nothing (D3).
//  6. A member law's parameters are typed in its type's bracket parameters,
//     and a law of a generic type is generic in them, as a generic function
//     body is in its own (funcCtx.typeParams). A top-level law has no type
//     parameters (the parser refuses them). A parameter written as a function type
//     without `escaping` is non-escaping, as a function's is (writtenParamType).
//     They need not be generatable: a law whose parameter has no generator
//     is checked and not run (rule 8).
//  7. Running (planLaws): a law quantifies over generated values of its
//     parameters and over each operation type it names (Law.Slots): its
//     owner, for a member law, and each parameter whose type is a
//     non-generic operation type. A run binds
//     each slot to a provider, a named type that implements it, and draws
//     a value of each other parameter per case. A law of a generic
//     operation type runs at each provider's instance, which the
//     provider's operations fix (lawProvider, lawInstance; L4). The test
//     build of a
//     package P runs a binding when P sees the law and every bound
//     provider (each is declared in P or in a package P imports, at any
//     depth, the prelude and std included) and no other package that P
//     sees sees them all (Package.LawRuns): the least package that sees
//     the binding runs it, once in the module. So the laws of an operation
//     type run in the package of each of its providers, wherever the type
//     is declared (the domain package declares the type, a memory package
//     provides it), and a law over two packages' providers runs in a
//     package that imports both (G-exup-1, G-exup-3). A law with no slots
//     runs in its own package. Each value is generated with the
//     construction authority of the package that owns it: a parameter
//     with the law's package's, a provider with its own package's.
//  8. A law that cannot run says why (Law.Unrun): a parameter with no
//     generator or of a generic operation type, or an owner whose type
//     has a row parameter or an operations-constrained type parameter;
//     its own package reports it.
//     A provider is built for each case as its package builds it: by its
//     package's constructor New<P> when it declares one (LawProvider.Ctor;
//     fs4), else generated; one with neither says why (LawProvider.Unrun).
//     The constructor is the one a generator builds any value of P with
//     (tests.go rule 3, TypeName.Constructor), so a provider and a value
//     are built by one rule.
//     The test build reports each skip, and counts the skips in its
//     summary, never as passed. A law with no binding in its own package
//     is no skip: its own test build names it as run where its providers
//     are, so it is never silent.
//  9. A false tail or an escaping failure refutes the law for that
//     provider and case.
//
// Its facts are owned by OwnerLaw (owners.go): the run build never reads
// them, and the test build reads them to run the law.
//
// Prior art (doc.go; types.md, Laws 12): Elixir StreamData's `check all`
// and QuickCheck state properties as code, run by a test runner. MoonBit,
// Gleam and ReScript have no law construct, and Haskell's class laws are
// documentation only. Idris's interface laws (proofs carried by the
// implementation) are the "proved" tier. Ergo checks a law as code and
// keeps it out of the binary, as Zig keeps its `test` blocks out of
// non-test builds.

// collectTopLaw records a top-level law (rule 1), owned by OwnerLaw, with no
// owning type (Law.Owner nil). Its body is checked by checkLaws, once every
// type is resolved.
func (c *checker) collectTopLaw(d *syntax.LawDecl, fi *fileInfo) {
	c.ownBody(d, OwnerLaw)
	law := &Law{Name: d.Name.Name, Decl: d, Pkg: c.pkg, file: fi}
	c.def(d.Name, law)
	// A second law of a name is checked as well, so which one is reported
	// does not decide which body's diagnostics appear.
	c.pkg.Laws = append(c.pkg.Laws, law)
	c.claimTestName(d.Name, "law")
}

// collectLaws records the laws of tn's operation type spec in source order
// (rule 1), each owned by OwnerLaw. Their bodies are checked by checkLaws,
// once every type is resolved. A spec with laws and no operations is S108
// at the type's name, once; its laws are recorded as any type's are.
func (c *checker) collectLaws(spec *syntax.InterfaceType, tn *TypeName, fi *fileInfo) {
	ops := map[string]bool{}
	for _, m := range spec.Methods {
		for _, id := range m.Names {
			ops[id.Name] = true
		}
	}
	if len(spec.Methods) == 0 && len(spec.Laws) > 0 {
		c.errorf(tn.Decl.Name, CodeType, "%s has laws and no operations; write it as a top-level law: law %s(…) { … }", tn.Name, spec.Laws[0].Name.Name)
	}
	names := map[string]bool{}
	for _, d := range spec.Laws {
		law := &Law{Name: d.Name.Name, Decl: d, Owner: tn, Pkg: c.pkg, file: fi}
		c.def(d.Name, law)
		// A second law of a name is checked as well, so which one is
		// reported does not decide which body's diagnostics appear.
		c.pkg.Laws = append(c.pkg.Laws, law)
		switch {
		case names[law.Name]:
			// The duplicate form the declaration-order oracle reads,
			// keyed by its owner (export_classh_test.go, duplicateKey):
			// a top-level law's duplicate has the same message.
			c.errorf(d.Name, CodeType, "law %s is declared twice", law.Name)
			continue
		case ops[law.Name]:
			c.errorf(d.Name, CodeType, "law %s is named like an operation of %s", law.Name, tn.Name)
		}
		names[law.Name] = true
		tn.Laws = append(tn.Laws, law)
	}
}

// collectMachineLaws records the laws of the machine tn in source order
// (Machine laws, MU6), each owned by OwnerLaw. A machine law's name is in
// its machine's namespace, as a member law's is in its type's.
func (c *checker) collectMachineLaws(spec *syntax.MachineType, tn *TypeName, fi *fileInfo) {
	names := map[string]bool{}
	for _, d := range spec.Laws {
		law := &Law{Name: d.Name.Name, Decl: d, Machine: tn, file: fi}
		c.def(d.Name, law)
		c.pkg.Laws = append(c.pkg.Laws, law)
		if names[law.Name] {
			c.errorf(d.Name, CodeType, "law %s is declared twice", law.Name)
			continue
		}
		names[law.Name] = true
	}
}

// checkReachLaw checks a machine's reachability law (MU6): each target
// names a variant of the machine's state.
func (c *checker) checkReachLaw(law *Law) {
	n, _ := law.Machine.Type.(*Named)
	var u *Union
	var state Type
	if n != nil {
		if m, ok := n.Underlying().(*MachineType); ok {
			state, u = m.State, asUnion(m.State)
		}
	}
	if law.Decl.Reach.Pred != nil {
		c.checkLawPred(law, state)
		return
	}
	for _, id := range law.Decl.Reach.Targets {
		tn, ok := c.pkg.Scope.names[id.Name].(*TypeName)
		if !ok {
			c.errorf(id, CodeUnknown, "unknown type %s", id.Name)
			continue
		}
		c.pkg.Info.Uses[id] = tn
		if u == nil {
			continue // the machine's state was refused where it is written
		}
		found := false
		for _, v := range u.Variants {
			if vn, ok := v.(*Named); ok && vn.Obj == tn {
				law.Targets = append(law.Targets, vn)
				found = true
			}
		}
		if !found {
			c.errorf(id, CodeType, "%s is not a variant of %s, the state of machine %s", id.Name, state, law.Machine.Name)
		}
	}
}

// planMachineLaw decides whether a machine's reachability law runs
// (Machine laws, MU6): its step must need no capability, and its state,
// its signal and its start's input must have generators, since a run
// prints them and draws them when they cannot be listed.
func (c *checker) planMachineLaw(law *Law) {
	n, _ := law.Machine.Type.(*Named)
	if n == nil {
		law.Unrun = "its machine is invalid"
		return
	}
	m, ok := n.Underlying().(*MachineType)
	spec, ok2 := law.Machine.Decl.Spec.(*syntax.MachineType)
	// An invariant law reads its state through its predicate, so a record
	// state serves; a reachability law names variants of a union.
	inv := law.Decl.Reach.Pred != nil
	if !ok || !ok2 || !(asUnion(m.State) != nil || inv && unionLike(m.State)) || asUnion(m.Signal) == nil || len(law.Targets) != len(law.Decl.Reach.Targets) || inv && law.Pred == nil {
		law.Unrun = "its machine is invalid"
		return
	}
	// The step's checks (checkMachine) come later; a step that is not a
	// function is refused there, and this law has nothing to run.
	id, _ := spec.Step.(*syntax.Ident)
	var step *Func
	if id != nil {
		step, _ = c.pkg.Scope.names[id.Name].(*Func)
	}
	if step == nil || step.Sig == nil || isExtern(step) {
		law.Unrun = "its machine is invalid"
		return
	}
	if !c.types.resolve(step.Sig.Uses).known(c.types).Empty() {
		law.Unrun = "its step " + step.Name + " needs a capability, and a law runs a step with none"
		return
	}
	for _, part := range []struct {
		what string
		t    Type
	}{{"state", m.State}, {"signal", m.Signal}} {
		if why := c.ungeneratable(part.t); why != "" {
			law.Unrun = "its " + part.what + " type has no generator: " + why
			return
		}
	}
	if start := machineStart(c.pkg, spec); start != nil && start.Sig != nil {
		for _, p := range start.Sig.Params {
			if why := c.ungeneratable(p.Type); why != "" {
				law.Unrun = "its start's input " + p.Name + " has no generator: " + why
				return
			}
		}
		if !c.types.resolve(start.Sig.Uses).known(c.types).Empty() || start.Sig.Fallible {
			law.Unrun = "its start " + start.Name + " has a row, and a law runs a start with none"
		}
	}
}

// typeParams are the type parameters of the law's type (rule 6); a
// top-level law has none.
func (l *Law) typeParams() []*TypeParam {
	if l.Owner == nil {
		return nil
	}
	if n, ok := l.Owner.Type.(*Named); ok {
		return n.TParams
	}
	return nil
}

// capability is the operation type a member law's row holds (rule 2): its
// owner, when the owner is an operation type with no type parameters (a
// capability takes no type arguments). nil for a top-level law and for a
// law of a generic type.
func (l *Law) capability() *Named {
	if l.Owner == nil {
		return nil
	}
	n, ok := l.Owner.Type.(*Named)
	if !ok || len(n.TParams) > 0 {
		return nil
	}
	if _, isOps := n.Underlying().(*Interface); !isOps {
		return nil
	}
	return n
}

// constraintMethods reports whether tp's constraint has operations: a run
// would pass a witness for it (lower/witness.go), which it does not build.
func constraintMethods(tp *TypeParam) bool {
	var it *Interface
	switch c := tp.Constraint.(type) {
	case *Interface:
		it = c
	case *Named:
		it, _ = c.Underlying().(*Interface)
	}
	return it != nil && len(it.Methods) > 0
}

// planLaws decides how ergo test runs each law (Running, rules 7 to 9):
// its providers and, when it does not run, why. A duplicate law is
// refused (S108), so its plan is never read.
func (c *checker) planLaws() {
	for _, law := range c.pkg.Laws {
		if law.Decl.Reach != nil {
			c.planMachineLaw(law)
			continue
		}
		c.planLaw(law)
	}
	c.pkg.LawRuns = c.lawRuns()
}

// planLaw sets the law's slots, or why it does not run (rules 7 and 8).
func (c *checker) planLaw(law *Law) {
	var slots []LawSlot
	for i, v := range law.Params {
		if isInvalid(v.Type) {
			law.Unrun = "a parameter has an invalid type"
			return
		}
		if n, ok := v.Type.(*Named); ok {
			if _, isOps := n.Underlying().(*Interface); isOps {
				if len(n.TParams) > 0 || len(n.origin().TParams) > 0 {
					law.Unrun = "its parameter " + v.Name + " has the generic operation type " + n.String() + ", and a run has no type arguments for it"
					return
				}
				slots = append(slots, LawSlot{Param: i, Type: n})
				continue
			}
		}
		steps := 0
		if hasParam(v.Type, &steps) {
			// Typed in the owner's type parameters: generated at each
			// provider's instance (lawRuns).
			continue
		}
		if why := c.ungeneratable(v.Type); why != "" {
			law.Unrun = "its parameter " + v.Name + " has no generator: " + why
			return
		}
	}
	if law.Owner != nil {
		own := law.capability()
		if own == nil {
			n, _ := law.Owner.Type.(*Named)
			if n == nil {
				law.Unrun = "its owner " + law.Owner.Name + " is not an operation type"
				return
			}
			if _, isOps := n.Underlying().(*Interface); !isOps {
				law.Unrun = "its owner " + law.Owner.Name + " is not an operation type"
				return
			}
			// A law of a generic operation type runs at each provider's
			// instance (rule 7): the provider fixes the type arguments.
			for _, tp := range n.TParams {
				if tp.Kind != KindType {
					law.Unrun = "its operation type " + law.Owner.Name + " has the row parameter " + tp.Name + ", and a run binds only type parameters"
					return
				}
				if constraintMethods(tp) {
					law.Unrun = "its operation type " + law.Owner.Name + " constrains " + tp.Name + " by operations, and a run passes no witness"
					return
				}
			}
			own = n
		}
		slots = append([]LawSlot{{Param: -1, Type: own}}, slots...)
	}
	law.Slots = slots
}

// lawRuns are the laws the package's test build runs or reports (rule 7):
// for each law the package sees, the bindings of its slots to the
// providers the package sees, of which it is the least package to see
// all. Its own laws come first, then each seen package's, the prelude
// and std included; the lowering orders them canonically.
func (c *checker) lawRuns() []LawRun {
	sees := lawClosures(c.pkg)
	seen := sees[c.pkg]
	pkgs := make([]*Package, 0, len(seen))
	for q := range seen {
		if q != c.pkg {
			pkgs = append(pkgs, q)
		}
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Path < pkgs[j].Path })
	pkgs = append([]*Package{c.pkg}, pkgs...)
	providers := map[*Named][]LawProvider{}
	providersOf := func(own *Named) []LawProvider {
		if ps, ok := providers[own]; ok {
			return ps
		}
		var ps []LawProvider
		for _, q := range pkgs {
			for _, tn := range q.Types {
				if lp, ok := c.lawProvider(tn, own); ok {
					ps = append(ps, lp)
				}
			}
		}
		providers[own] = ps
		return ps
	}
	// least reports that no package c.pkg sees, other than c.pkg, sees
	// every package of parts.
	least := func(parts []*Package) bool {
		for _, q := range pkgs[1:] {
			all := true
			for _, p := range parts {
				if !sees[q][p] {
					all = false
					break
				}
			}
			if all {
				return false
			}
		}
		return true
	}
	var runs []LawRun
	for _, q := range pkgs {
		for _, law := range q.Laws {
			own := q == c.pkg
			if law.Unrun != "" || len(law.Slots) == 0 {
				if own {
					run := LawRun{Law: law}
					if law.Unrun == "" {
						run.Bindings = [][]LawProvider{nil}
					}
					runs = append(runs, run)
				}
				continue
			}
			bindings := [][]LawProvider{nil}
			for _, s := range law.Slots {
				var next [][]LawProvider
				for _, b := range bindings {
					for _, p := range providersOf(s.Type) {
						next = append(next, append(slices.Clone(b), p))
					}
				}
				bindings = next
			}
			run := LawRun{Law: law}
			for _, b := range bindings {
				parts := []*Package{q}
				for _, p := range b {
					parts = append(parts, p.Type.Obj.Pkg)
				}
				if least(parts) {
					run.Bindings = append(run.Bindings, c.lawInstance(law, b))
				}
			}
			if own || len(run.Bindings) > 0 {
				runs = append(runs, run)
			}
		}
	}
	return runs
}

// lawInstance is the binding b of a law of a generic operation type at
// its owner's provider's instance (rule 7): the law's parameter types with
// the instance's type arguments, and why the binding does not run when one
// of them has no generator. Any other law's binding is b.
func (c *checker) lawInstance(law *Law, b []LawProvider) []LawProvider {
	if len(law.Slots) == 0 || law.Slots[0].Param >= 0 || len(law.Slots[0].Type.TParams) == 0 {
		return b
	}
	own := law.Slots[0].Type
	b = slices.Clone(b)
	p := b[0]
	m := map[*TypeParam]Type{}
	for i, tp := range own.TParams {
		m[tp] = p.Slot.TArgs[i]
	}
	p.Params = make([]Type, len(law.Params))
	for i, v := range law.Params {
		p.Params[i] = c.subst(v.Type, m)
		if p.Unrun != "" || lawSlotParam(law, i) {
			continue
		}
		if why := c.ungeneratableFrom(p.Params[i], law.Pkg); why != "" {
			p.Unrun = "its parameter " + v.Name + " has no generator at " + p.Slot.String() + ": " + why
		}
	}
	b[0] = p
	return b
}

// lawSlotParam reports whether the law's parameter i is a slot.
func lawSlotParam(law *Law, i int) bool {
	for _, s := range law.Slots {
		if s.Param == i {
			return true
		}
	}
	return false
}

// lawProvider is the provider tn's type is for the operation type own: a
// named type, not generic, not an operation type or a machine, that
// converts to own by its operations. It is built as its package builds it
// (rule 8): by its package's constructor New<P> when there is one, since
// a record whose fields only its package sets may hold an invariant that
// only its functions keep; else generated with its package's authority.
func (c *checker) lawProvider(tn *TypeName, own *Named) (LawProvider, bool) {
	p, ok := tn.Type.(*Named)
	if !ok || p == own || len(p.TParams) > 0 {
		return LawProvider{}, false
	}
	switch p.Underlying().(type) {
	case *Interface, *MachineType:
		return LawProvider{}, false
	}
	slot := own
	var targs []Type
	if len(own.TParams) > 0 {
		// A generic operation type: the provider's operations fix its
		// type arguments, by unifying their signatures with the type's
		// (Generics 6, interfaces through method sets).
		in := &inferer{c: c, m: map[*TypeParam]Type{}, set: map[*TypeParam]bool{}}
		for _, tp := range own.TParams {
			in.set[tp] = true
		}
		it, _ := own.Underlying().(*Interface)
		if it == nil || !in.implements(it, p, 0) {
			return LawProvider{}, false
		}
		for _, tp := range own.TParams {
			if in.m[tp] == nil {
				return LawProvider{}, false
			}
			targs = append(targs, in.m[tp])
		}
		slot = c.instantiate(own, targs)
	}
	a, _ := c.providerAssignability(p, slot)
	if !a.ok() || a.conv.Kind != ConvOperations {
		return LawProvider{}, false
	}
	lp := LawProvider{Type: p, Conv: a.conv, Slot: slot}
	if _, built := c.constructorWhy(p); built && p.Obj.ctor != nil {
		lp.Ctor = p.Obj.ctor
	} else if why := c.ungeneratableFrom(p, p.Obj.Pkg); why != "" {
		lp.Unrun = "its values have no generator: " + why + "; a func New" + p.Obj.Name + "() " + p.Obj.Name + " with no rows would build one for each case"
	}
	return lp, true
}

// lawClosures are, for pkg and each package it sees, the packages each
// sees: itself, the prelude and its imports at any depth (rule 7).
func lawClosures(pkg *Package) map[*Package]map[*Package]bool {
	pre := pkg
	if pkg.Path != std.PreludePath {
		pre = prelude().pkg
	}
	out := map[*Package]map[*Package]bool{}
	var walk func(p *Package) map[*Package]bool
	walk = func(p *Package) map[*Package]bool {
		if s, ok := out[p]; ok {
			return s
		}
		s := map[*Package]bool{p: true, pre: true}
		out[p] = s
		for _, f := range p.files {
			for _, pn := range f.imports {
				if pn.Pkg == nil || pn.broken {
					continue
				}
				for q := range walk(pn.Pkg) {
					s[q] = true
				}
			}
		}
		return s
	}
	walk(pkg)
	walk(pre)
	return out
}

// rowHolds reports whether r holds an element, through its variables'
// solutions.
func rowHolds(r *Row) bool {
	for _, e := range r.Elems() {
		switch e := e.(type) {
		case *RowVar:
			if rowHolds(e.Solution()) {
				return true
			}
		case *RowArg:
			if rowHolds(e.Row) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// checkLaws checks each law body after every function body converged: no
// body refers to a law, so a law is a leaf of the body graph.
func (c *checker) checkLaws() {
	for _, law := range c.pkg.Laws {
		c.checkLaw(law)
	}
}

func (c *checker) checkLaw(law *Law) {
	d := law.Decl
	saved := c.cur
	c.cur = law.file
	defer func() { c.cur = saved }()
	if d.Reach != nil {
		c.checkReachLaw(law)
		return
	}
	defer c.beginBodyCheck(law)()
	tparams := law.typeParams()
	defer c.monoOwner(tparams)()
	name := "law " + law.Name
	// results gives a bare `return` its message; lawResult admits a value.
	// A member law's service row holds its own operation type, which the
	// run binds to the provider under test (rule 2), so a helper that
	// needs it runs inside the law.
	uses := newRow()
	if own := law.capability(); own != nil {
		uses = uses.with(c.types, own)
	}
	tg := &target{name: name, errors: newRow(), declUses: uses, results: []Type{Typ[Bool]}, resultKnown: true, isFunc: true, law: true}
	// A member law's operation scope sits between the law's frame and the
	// package; a top-level law's frame encloses the package directly (rule
	// 3). This is the only step the two placements do not share.
	outer := c.pkg.Scope
	if law.Owner != nil {
		ops := newScope(c.pkg.Scope)
		if it, ok := under(law.Owner.Type).(*Interface); ok {
			for _, m := range it.Methods {
				ops.insert(m.Name, m)
			}
		}
		outer = ops
	}
	frame := newScope(outer)
	frame.frame = true
	fc := &funcCtx{t: tg, scope: frame, law: law}
	prev := c.fn
	c.fn = fc
	defer func() { c.fn = prev }()
	params := map[string]*TypeParam{}
	for _, tp := range tparams {
		params[tp.Name] = tp
	}
	if d.Params != nil {
		// A law's parameters parse as a function's (paramPlain), so each
		// field has a type: a lone name is one (S104 when no type has it).
		for _, field := range d.Params.List {
			typ := writtenParamType(field.Type, c.typExpr(field.Type, &typeCtx{param: true, params: params}))
			if len(field.Names) == 0 {
				law.Params = append(law.Params, &Var{Name: "_", Type: typ, Pos: field.Type.Pos(), param: true, used: true})
				continue
			}
			for _, id := range field.Names {
				v := &Var{Name: id.Name, Type: typ, Pos: id.Pos(), id: id, param: true, used: true}
				law.Params = append(law.Params, v)
				if field.Where != nil && !containsInvalid(typ) {
					// A law parameter's where (types.md, Refinements 10):
					// its generator draws inside the predicate.
					c.refining = true
					r := c.refinement(id.Name, typ, field.Where, id.Name)
					c.refining = false
					if r != nil {
						for len(law.Where) < len(law.Params) {
							law.Where = append(law.Where, nil)
						}
						law.Where[len(law.Params)-1] = r
					}
				}
				if id.Name == "_" {
					continue
				}
				if prior, ok := frame.names[id.Name].(*Var); ok && prior.param {
					c.errorf(id, CodeType, "%s is already a parameter of %s", id.Name, name)
					continue
				}
				// D-D3, else D-D2 against the operations; the parameter
				// still binds.
				if !c.preludeName(id) {
					c.shadowed(id, frame)
				}
				c.def(id, v)
				frame.insert(id.Name, v)
			}
		}
	}
	c.lawBody(d.Body)
	c.reportGoCopyWrites(d.Body)
	law.Fails = c.types.resolve(tg.errors).known(c.types)
	c.reportUnused(fc)
	c.reportNeverReassigned(fc)
}

// lawBody checks a law's body, which ends in a bool (rule 5). Its tail is
// not a function's: a fallible tail does not escape implicitly (S105 unless
// tried), and an error value is a value like any other, so not a bool.
func (c *checker) lawBody(body *syntax.BlockStmt) {
	missed := false
	k := &sink{what: "the law's tail value"}
	k.missing = func(end syntax.Pos) {
		missed = true
		c.errorIn(body, end, end+1, CodeType, "a law's body ends in a bool, not nothing")
	}
	c.yieldList(body.List, k, body.Rbrace)
	if k.bad || missed || !k.valued {
		return
	}
	var at syntax.Node = body.List[len(body.List)-1]
	if s, ok := at.(*syntax.ExprStmt); ok {
		at = s.X
	}
	c.lawResult(at, k.typ())
}

// lawResult is the one rule for a law's success result (rule 8): its
// type's underlying type is bool, so a type over bool is one. The tail
// (lawBody) and every return from the law's own target (returnValue) ask
// it, so no exit admits a value another refuses: `return e` means the same
// as a tail e (rows.md, Totality 3). It reports at x when t breaks the rule;
// an invalid t was reported where it came from.
func (c *checker) lawResult(x syntax.Node, t Type) {
	switch {
	case containsInvalid(t), isBoolean(t):
	case t == NoValue:
		c.errorf(x, CodeType, "a law's body ends in a bool, not nothing")
	default:
		c.errorf(x, CodeType, "a law's body ends in a bool, not %s", t)
	}
}

// lawOpCall checks x when its callee is a bare name that resolves to an
// operation of the law around it (rule 4), and reports whether it did. The
// call has no receiver and charges nothing: an operation writes no
// capability row (interfaceType), so its Needs is empty.
func (c *checker) lawOpCall(x *syntax.CallExpr) (operand, bool) {
	id, ok := unparen(x.Fun).(*syntax.Ident)
	if !ok {
		return operand{}, false
	}
	m, ok := c.lookup(id.Name).(*Method)
	if !ok {
		return operand{}, false
	}
	c.recordUse(id, m)
	fun := operand{mode: modeValue, typ: m.Sig, method: m}
	// The callee and each parenthesis around it have the operation's type.
	for e := x.Fun; ; {
		fun.expr = e
		c.record(e, fun)
		p, ok := e.(*syntax.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	call := c.callInfo(x)
	call.Kind, call.Op = CallLawOp, m
	return c.callSig(x, m.Sig, fun), true
}
