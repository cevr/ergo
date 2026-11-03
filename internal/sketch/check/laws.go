package check

import "ergo.local/prototype/internal/sketch/syntax"

// Laws (196l; spec D-E4). A law is a member of an operation type, checked
// and never run: running laws is #162. Each rule is an invariant over its
// whole domain:
//
//  1. A law declares no value. Law names have their own namespace, as test
//     names do: a law is never entered in a value scope, so a use of its
//     name resolves as any undeclared name does (S104), and its type has no
//     member of that name (S104). A second law of one name in one type is
//     S108 at the second, and so is a law named like an operation of its
//     type.
//  2. A law's body is a failure target: an escaping failure refutes the
//     law, so its failure row is inferred and unbounded (Law.Fails). Its
//     service row, after its own with blocks, is empty: a charging call
//     that no with of the law discharges is S102 ("it needs no service").
//  3. Its scope is its parameters, then the operations of its type, then
//     the package. An operation therefore wins over a package function of
//     its name, and no binder of the law may take an operation's name (D-D2:
//     the operation scope encloses the law's body, so shadowed refuses it).
//  4. An operation is call-only in a law: a call of it is checked against
//     the operation's signature and recorded as CallLawOp, whose failures
//     join the law's row and which charges nothing; any other use is S108.
//  5. The body ends in a bool: a tail whose type's underlying type is not
//     bool is S108 once, at the tail; a body that can end with no value is
//     S108 at the end of that path. A fallible tail must be tried (S105), as
//     every value position's. An invalid tail adds nothing (D3).
//  6. A law's parameters are typed in its type's bracket parameters, and a
//     law of a generic type is generic in them, as a generic function body
//     is in its own (funcCtx.typeParams). One written as a function type
//     without `escaping` is non-escaping, as a function's is (writtenParamType).
//     They need not be generatable: that is a property test's rule, and
//     #162 decides how laws are run.
//
// Its facts are owned by OwnerLaw (owners.go), so neither build reads them.
//
// Prior art: Elixir's StreamData `property` and MoonBit's `test` with
// quickcheck state properties beside the code, run by a test runner; Gleam
// and ReScript have no law construct. Haskell's QuickCheck laws and Idris's
// interface laws (proofs carried by the implementation) are the two ends of
// the "checked, not run" and "proved" tiers.

// collectLaws records the laws of tn's operation type spec in source order
// (rule 1), each owned by OwnerLaw. Their bodies are checked by checkLaws,
// once every type is resolved.
func (c *checker) collectLaws(spec *syntax.InterfaceType, tn *TypeName, fi *fileInfo) {
	ops := map[string]bool{}
	for _, m := range spec.Methods {
		for _, id := range m.Names {
			ops[id.Name] = true
		}
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
			// The duplicate form the declaration-order oracle reads
			// (export_classh_test.go, duplicateName).
			c.errorf(d.Name, CodeType, "law %s is declared twice", law.Name)
			continue
		case ops[law.Name]:
			c.errorf(d.Name, CodeType, "law %s is named like an operation of %s", law.Name, tn.Name)
		}
		names[law.Name] = true
		tn.Laws = append(tn.Laws, law)
	}
}

// typeParams are the type parameters of the law's type (rule 6).
func (l *Law) typeParams() []*TypeParam {
	if n, ok := l.Owner.Type.(*Named); ok {
		return n.TParams
	}
	return nil
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
	defer c.beginBodyCheck(law)()
	tparams := law.typeParams()
	defer c.monoOwner(tparams)()
	name := "law " + law.Name
	tg := &target{name: name, errors: newRow(), declUses: newRow(), results: []Type{Typ[Bool]}, resultKnown: true, isFunc: true}
	// The operation scope sits between the law's frame and the package
	// (rule 3).
	ops := newScope(c.pkg.Scope)
	if it, ok := under(law.Owner.Type).(*Interface); ok {
		for _, m := range it.Methods {
			ops.insert(m.Name, m)
		}
	}
	frame := newScope(ops)
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
	if k.bad || missed || !k.valued || isBoolean(k.typ()) {
		return
	}
	var at syntax.Node = body.List[len(body.List)-1]
	if s, ok := at.(*syntax.ExprStmt); ok {
		at = s.X
	}
	c.errorf(at, CodeType, "a law's body ends in a bool, not %s", k.typ())
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
