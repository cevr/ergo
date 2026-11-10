package check

// Invariant laws (X2; types.md, Laws 13). `law Name always p` holds when
// every state a run of the machine reaches, its start included, satisfies
// p; `law Name sometimes p` holds when some reachable state does. p names
// a package function of type func(S) bool, S the machine's state, with no
// error and no capability row, and it is pure: its footprint is empty
// (rows.md, Footprints 2), so the explorer may call it at any state, any
// number of times, and a counterexample replays. The rule is the durable
// step's (durable.go), applied to a law's predicate.

// checkLawPred checks the predicate of the invariant law law over the
// machine's state (nil when the machine was refused) and records the law
// for the purity check of checkLawPreds.
func (c *checker) checkLawPred(law *Law, state Type) {
	id := law.Decl.Reach.Pred
	o := c.pkg.Scope.names[id.Name]
	if o == nil {
		c.errorf(id, CodeUnknown, "unknown function %s", id.Name)
		return
	}
	f, ok := o.(*Func)
	if !ok {
		c.errorf(id, CodeType, "%s is not a function; law %s %s names a pure function of the machine's state to bool (types.md, Laws 13)", id.Name, law.Name, law.Decl.Reach.Invariant)
		return
	}
	f.called = true
	c.pkg.Info.Uses[id] = f
	if isExtern(f) || f.Decl == nil || f.Decl.Body == nil {
		c.errorf(id, CodeType, "the predicate %s of law %s is a host function; a predicate is an Ergo function, so the checker sees that it is pure (types.md, Laws 13)", id.Name, law.Name)
		return
	}
	if state == nil || isInvalid(state) || f.Sig == nil {
		return
	}
	sig := f.Sig
	bad := len(sig.TParams) > 0 || len(sig.Params) != 1 || len(sig.Results) != 1
	if !bad {
		bad = !c.identical(sig.Params[0].Type, state) || !c.identical(sig.Results[0], Typ[Bool])
	}
	if !bad {
		bad = sig.Fallible || !c.types.resolve(sig.Errors).known(c.types).Empty() || !c.types.resolve(sig.Uses).known(c.types).Empty()
	}
	if bad {
		c.errorf(id, CodeType, "the predicate %s of law %s must have the type func(%s) bool, with no error or capability row: the explorer calls it at every state (types.md, Laws 13)", id.Name, law.Name, state)
		return
	}
	law.Pred = f
	c.lawPreds = append(c.lawPreds, law)
}

// checkLawPreds reports each invariant law whose predicate is not pure: a
// capability it exercises, a task it starts or an opaque call, named at its
// first call (impureCall). It runs once the footprints are known.
func (c *checker) checkLawPreds() {
	for _, law := range c.lawPreds {
		f := law.Pred
		if f.Pkg == nil || f.Pkg.Info == nil {
			continue
		}
		fp, ok := f.Pkg.Info.Footprints[f.Decl]
		if !ok || fp.Empty() {
			continue
		}
		c.cur = law.file
		c.errorf(law.Decl.Reach.Pred, CodeType, "the predicate of law %s must be pure, since the explorer calls it at every state: %s (types.md, Laws 13)", law.Name, impureCall("predicate", f, fp))
	}
}
