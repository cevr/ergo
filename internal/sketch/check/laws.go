package check

import "ergo.local/prototype/internal/sketch/syntax"

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
//  7. Running (planLaws): ergo test runs a top-level law over generated
//     values of its parameters, and a member law of a non-generic
//     operation type against each provider of it: each named type of the
//     package, in source order, that implements the type (Law.Providers),
//     with a generated value of the provider and of each parameter per
//     case. The closed world of the package lists the providers.
//  8. A law that cannot run says why (Law.Unrun): a parameter with no
//     generator, a generic operation type, or no provider. A provider
//     whose values have no generator says why (LawProvider.Unrun). The
//     test build reports each as skipped, never as passed.
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
	law := &Law{Name: d.Name.Name, Decl: d, file: fi}
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
		law := &Law{Name: d.Name.Name, Decl: d, Owner: tn, file: fi}
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
	if !ok || !ok2 || asUnion(m.State) == nil || asUnion(m.Signal) == nil || len(law.Targets) != len(law.Decl.Reach.Targets) {
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

// planLaws decides how ergo test runs each law (Running, rules 7 to 9):
// its providers and, when it does not run, why. A duplicate law is
// refused (S108), so its plan is never read.
func (c *checker) planLaws() {
	for _, law := range c.pkg.Laws {
		if law.Decl.Reach != nil {
			c.planMachineLaw(law)
			continue
		}
		for _, v := range law.Params {
			if isInvalid(v.Type) {
				law.Unrun = "a parameter has an invalid type"
				break
			}
			if why := c.ungeneratable(v.Type); why != "" {
				law.Unrun = "its parameter " + v.Name + " has no generator: " + why
				break
			}
		}
		if law.Owner == nil || law.Unrun != "" {
			continue
		}
		own := law.capability()
		if own == nil {
			law.Unrun = "its operation type " + law.Owner.Name + " is generic, and a run has no type arguments for it"
			continue
		}
		for _, tn := range c.pkg.Types {
			p, ok := tn.Type.(*Named)
			if !ok || p == own || len(p.TParams) > 0 {
				continue
			}
			switch p.Underlying().(type) {
			case *Interface, *MachineType:
				continue
			}
			a, _ := c.assignability(p, own)
			if !a.ok() || a.conv.Kind != ConvOperations {
				continue
			}
			lp := LawProvider{Type: p, Conv: a.conv}
			if why := c.ungeneratable(p); why != "" {
				lp.Unrun = "its values have no generator: " + why
			}
			law.Providers = append(law.Providers, lp)
		}
		if len(law.Providers) == 0 {
			law.Unrun = "no type of the package implements " + law.Owner.Name
		}
	}
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
