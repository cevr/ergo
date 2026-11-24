package check

import (
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Laws under a context (types.md, Laws 10; L5, stage 3). A member law
// of an operation type T also runs once for each named context of this
// package that binds T: each case builds the context, as a with of it
// does, and runs the law against the provider the context binds, so the
// provider is the one the program uses, made by its constructor with its
// arguments and its other capabilities in place. A case releases the
// providers with a Close it built when it ends, as the with does; a
// context whose release a case cannot run does not run (lawRelease,
// LawContext.Unrun). Only a law whose one slot is its owner
// runs under a context; a law of a generic operation type runs under one
// that binds an instance of it, at that instance.

// LawContext is a named context a law runs under (LawRun.Contexts): the
// context, the With fact that builds the provider of the law's owner
// (Discharged holds the owner or its instance), and why it does not run,
// "" when it does. Params are the law's parameter types at the instance,
// for a law of a generic operation type; nil otherwise.
type LawContext struct {
	Context *Context
	With    *With
	Unrun   string
	Params  []Type
	// Witnesses are the generic owner's witnesses at the instance the
	// context binds (lawWitnesses; LawProvider.Witnesses).
	Witnesses []Witness
	Claim     *LawProvider
}

// Title names the context in transcripts and run keys. Qualify it with
// the instance only when the context binds more than one for this law.
func (lc LawContext) Title() string {
	if cap, ok := lc.With.Discharged[0].(*Named); ok && cap.Orig != nil {
		if others := lawContextCaps(cap.Orig, lc.Context); len(others) > 1 {
			return lc.Context.Name + " (" + strings.TrimPrefix(typeString(cap), packagePath(cap.Obj.Pkg)) + ")"
		}
	}
	return lc.Context.Name
}

func packagePath(p *Package) string {
	if p != nil {
		return p.Path + "."
	}
	return ""
}

// planLawContexts adds to each law run the named contexts of this package
// that bind its owner, and a run for each law of another package that has
// only such contexts here. It runs after the ownership pass, so the
// acquisitions and Closes are known.
func (c *checker) planLawContexts() {
	defer c.reportUnfedProviders()
	if len(c.pkg.Contexts) == 0 {
		return
	}
	names := make([]string, 0, len(c.pkg.Contexts))
	for name, w := range c.pkg.Contexts {
		if w.Decl != nil && !w.unknown && w.binds != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	runs := map[*Law]int{}
	for i, r := range c.pkg.LawRuns {
		runs[r.Law] = i
	}
	seen := lawClosures(c.pkg)[c.pkg]
	var pkgs []*Package
	for q := range seen {
		pkgs = append(pkgs, q)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Path < pkgs[j].Path })
	c.planClaimContexts(names, pkgs, runs)
	for _, q := range pkgs {
		for _, law := range q.Laws {
			if law.Unrun != "" || law.Owner == nil || law.Claimed() || len(law.Slots) != 1 || law.Slots[0].Param >= 0 {
				continue
			}
			var under []LawContext
			for _, name := range names {
				w := c.pkg.Contexts[name]
				for _, cap := range lawContextCaps(law.Slots[0].Type, w) {
					if lc, ok := c.lawContext(law, w, cap); ok {
						under = append(under, lc)
					}
				}
			}
			if len(under) == 0 {
				continue
			}
			for _, lc := range under {
				if lc.Unrun == "" {
					e := lc.With.Entries[0]
					if info := providerInfo(c.contextOwners.of[e]); info != nil && e.Provider != nil {
						c.pkg.fed = append(c.pkg.fed, fedBinding{law: law, provider: info.Types[e.Provider]})
					}
				}
			}
			if i, ok := runs[law]; ok {
				r := &c.pkg.LawRuns[i]
				r.Contexts = under
				r.Bindings = slices.DeleteFunc(r.Bindings, func(b []LawProvider) bool {
					return c.builtByContext(b[0], under)
				})
				continue
			}
			runs[law] = len(c.pkg.LawRuns)
			c.pkg.LawRuns = append(c.pkg.LawRuns, LawRun{Law: law, Contexts: under})
		}
	}
}

// planClaimContexts uses the actual selected entry of a named context,
// not its advertised operation type, to feed a private Self input.
func (c *checker) planClaimContexts(names []string, pkgs []*Package, runs map[*Law]int) {
	type candidate struct {
		law *Law
		p   *LawProvider
	}
	var want []candidate
	fed := map[*LawProvider]bool{}
	for i := range c.pkg.LawRuns {
		r := &c.pkg.LawRuns[i]
		if !r.Law.Claimed() {
			continue
		}
		for _, b := range r.Bindings {
			if b[0].ClaimFeed != 0 {
				want = append(want, candidate{r.Law, &b[0]})
			}
		}
	}
	for _, p := range pkgs {
		if p == c.pkg || p.module != c.pkg.module {
			continue
		}
		for _, u := range p.unfed {
			if u.claim != nil && u.claim.ClaimFeed != 0 {
				want = append(want, candidate{u.law, u.claim})
			}
		}
	}
	for _, it := range want {
		for _, name := range names {
			w := c.pkg.Contexts[name]
			c.readContextHeader(w)
			if !w.needs.Empty() {
				continue
			}
			c.contextOwners.add(w)
			var feed LawContext
			w.binds.each(&contextMeter{limit: 1 << 30}, func(key *capNode) bool {
				e := key.entry
				owner := c.contextOwners.of[e]
				info := providerInfo(owner)
				if info == nil || e.Provider == nil || !c.identical(info.Types[e.Provider], it.p.At) || !claimEntryBuilt(info, e.Provider, it.p.At) {
					return true
				}
				fact := &With{Contexts: []*Context{w}, Discharged: []Type{key.t}, Entries: []*ContextEntry{e}}
				fact.Acquired = c.acquire(w, fact.Entries, c.contextOwners.of)
				if c.lawRelease(fact) != "" {
					return true
				}
				feed = LawContext{Context: w, With: fact, Params: it.p.Params, Claim: it.p}
				return false
			})
			if feed.Claim == nil {
				continue
			}
			i, ok := runs[it.law]
			if !ok {
				i = len(c.pkg.LawRuns)
				runs[it.law] = i
				c.pkg.LawRuns = append(c.pkg.LawRuns, LawRun{Law: it.law})
			}
			c.pkg.LawRuns[i].Contexts = append(c.pkg.LawRuns[i].Contexts, feed)
			c.pkg.fed = append(c.pkg.fed, fedBinding{law: it.law, provider: it.p.At, claim: it.p.ClaimSlot})
			// The first admissible named context feeds the one obligation.
			fed[it.p] = true
			break
		}
	}
	for i := range c.pkg.LawRuns {
		r := &c.pkg.LawRuns[i]
		if !r.Law.Claimed() {
			continue
		}
		r.Bindings = slices.DeleteFunc(r.Bindings, func(b []LawProvider) bool {
			return fed[&b[0]]
		})
	}
}

// A local let holding a claimant is not a construction. A context entry
// may build it using a checked result call, including an inferred
// generic instance.
func claimEntryBuilt(info *Info, x syntax.Expr, at *Named) bool {
	call, ok := unwrapTry(x).(*syntax.CallExpr)
	if !ok {
		return false
	}
	checked := info.Calls[call]
	if checked == nil || checked.Kind != CallFunc || checked.Func == nil {
		return false
	}
	for _, op := range checked.Operands(call) {
		t := info.Types[op]
		if p, ok := t.(*Pointer); ok {
			t = p.Elem
		}
		if n, ok := t.(*Named); ok && n.origin() == at.origin() {
			return false // requires a claimant before the context can build it
		}
	}
	return true
}

// builtByContext reports whether a context of under builds the provider p
// that the law's run cannot build itself (p.Unrun): a context that runs
// and whose entry for the owner is a value of p's type. The run under
// that context is then p's run, so p is no skip. This is how a test file
// picks a provider that takes another one, as cmd/NAME/main.ergo picks
// the binary's: let overMemory = context { limits.Limiter:
// NewLoopback(memory.NewMemory()) }.
func (c *checker) builtByContext(p LawProvider, under []LawContext) bool {
	if p.Unrun == "" {
		return false
	}
	for _, lc := range under {
		if lc.Unrun != "" {
			continue
		}
		e := lc.With.Entries[0]
		info := providerInfo(c.contextOwners.of[e])
		if info != nil && e.Provider != nil && c.identical(info.Types[e.Provider], p.Type) {
			return true
		}
	}
	return false
}

// lawContextCaps returns each instance a context binds in key order. A
// non-generic owner has one candidate, found by the ordinary lookup.
func lawContextCaps(own *Named, w *Context) []*Named {
	if len(own.TParams) == 0 {
		return []*Named{own}
	}
	var caps []*Named
	w.binds.each(&contextMeter{limit: 1 << 30}, func(n *capNode) bool {
		if t, ok := n.t.(*Named); ok && t.Orig == own {
			caps = append(caps, t)
		}
		return true
	})
	return caps
}

// lawContext is the run of law under the named context w at the binding cap.
func (c *checker) lawContext(law *Law, w *Context, cap *Named) (LawContext, bool) {
	own := law.Slots[0].Type
	var params []Type
	if cap.Orig != nil {
		m := paramMap(own.TParams, cap.TArgs)
		params = make([]Type, len(law.Params))
		for i, p := range law.Params {
			params[i] = c.subst(p.Type, m)
		}
	}
	found, node := w.binds.find(c.types, cap)
	if !found {
		return LawContext{}, false
	}
	fact := &With{Contexts: []*Context{w}, Discharged: []Type{cap}, Entries: []*ContextEntry{node.entry}}
	c.contextOwners.add(w)
	fact.Acquired = c.acquire(w, fact.Entries, c.contextOwners.of)
	lc := LawContext{Context: w, With: fact, Params: params}
	lc.Unrun = c.lawRelease(fact)
	if c.readContextHeader(w); lc.Unrun == "" && !w.needs.Empty() {
		// A case provides no capability from a with site
		// (contextneeds.go): only TestAmbient, the runner's.
		lc.Unrun = "context " + w.Name + " needs " + rowText(w.needs) + " from the with site that builds it, which a law case does not provide"
	}
	if lc.Unrun == "" && !w.fails.Empty() && law.Decl.Rewrite != nil {
		// A case whose build fails is a failed case (FALLIBLE-CONTEXT,
		// Design 4; lower's lawContextRun). A rewrite law compares two
		// builds of the context, side by side, which have no failure path
		// yet.
		lc.Unrun = "context " + w.Name + " can fail with " + rowText(w.fails) + " while it is built, and a rewrite law's two builds of it have no failure path yet (CTX-ROWS-2c)"
	}
	if lc.Unrun == "" && params != nil {
		// A non-generic law's parameters were judged when it was planned.
		for i, p := range law.Params {
			if why := c.ungeneratableFrom(params[i], law.Pkg); why != "" {
				lc.Unrun = "its parameter " + p.Name + " has no generator at " + cap.String() + ": " + why
				// The context binds the instance, and no case of the law
				// can be fed under it (LAW-UNFED).
				if w.file != nil && w.Decl != nil {
					saved := c.cur
					c.cur = w.file
					c.errorf(w.Decl.Name, CodeType, "law %s cannot run under context %s: %s (types.md, Laws 10)", law.Title(c.pkg), w.Name, lc.Unrun)
					c.cur = saved
				}
				break
			}
		}
		if lc.Unrun == "" {
			lc.Witnesses, lc.Unrun = c.lawWitnesses(own, cap.TArgs)
		}
	}
	if lc.Unrun == "" && law.Decl.Rewrite != nil && !freshEntry(w, node.entry) {
		// Each side of a rewrite law's case needs its own provider
		// (Laws 14): a build of the context makes one only when the
		// entry calls the provider's constructor.
		lc.Unrun = "a rewrite law runs each side on a fresh provider, and only an entry that calls a constructor func NewP(…) P builds one"
	}
	if lc.Unrun == "" && law.Decl.Rewrite != nil {
		// A rewrite side builds the context into its own copy and has no
		// case scope to release in (lower's contextRewriteSource), where
		// a law case releases as a with does (lawRelease).
		for _, a := range fact.Acquired {
			if a.Releases() {
				lc.Unrun = "a rewrite law's sides do not release the providers their build acquires, and this build acquires one with a release"
				break
			}
		}
	}
	return lc, true
}

// lawRelease is why a law case cannot release the providers fact acquires,
// or "". A case releases them as a with does at the end of its block
// (resources.md, Context ownership): after the law's last operation, each
// provider once, last built first. Outside that with a case has no
// capability, so a release that needs one (rows.md, a release's needs are
// charged outside the with) cannot run there; a provider that names a
// package let would be released by the first case and read closed by the
// next (R11); a generic Close with witnesses has no instance the case
// could pass (S109).
func (c *checker) lawRelease(fact *With) string {
	for _, a := range fact.Acquired {
		if a.Lent {
			// A named context's lent provider is a package let: every
			// case would share one value, so no case starts fresh.
			if info := providerInfo(c.contextOwners.of[a.Entry]); info != nil && packageRoot(info, a.Entry.Provider) != "" {
				name := packageRoot(info, a.Entry.Provider)
				return "its provider names the package let " + name + ", which every case would share"
			}
			continue
		}
		if !a.Releases() {
			continue
		}
		if closeWitnessed(a.Close) != "" {
			return "its provider's generic Close takes witnesses for a type parameter constrained by operations, and a case has no instance to pass (resource ownership)"
		}
		if needs := c.providerNeeds(a); len(needs) > 0 {
			return "its provider's release needs " + needs[0].cap.String() + "; a law case has no capability to release it with"
		}
		// A release was found from the provider's package facts (acquire),
		// so info is set.
		info := providerInfo(c.contextOwners.of[a.Entry])
		if name := packageRoot(info, a.Entry.Provider); name != "" {
			return "its provider names the package let " + name + ", which a case's release would close for every later case"
		}
	}
	return ""
}

// freshEntry reports whether the entry e, of the context w or of one it
// embeds, builds a new provider each time a with of the context runs: its
// provider calls the constructor New<P> of its provider type P. A let
// that holds a provider is one provider for every build.
func freshEntry(w *Context, e *ContextEntry) bool {
	pkg := entryPackage(w, e, map[*Context]bool{})
	if pkg == nil || e.Provider == nil {
		return false
	}
	call, ok := unparen(e.Provider).(*syntax.CallExpr)
	if !ok {
		return false
	}
	var id *syntax.Ident
	switch x := unparen(call.Fun).(type) {
	case *syntax.Ident:
		id = x
	case *syntax.SelectorExpr:
		id = x.Sel
	}
	if id == nil {
		return false
	}
	f, _ := pkg.Info.Uses[id].(*Func)
	if f == nil || len(f.Sig.Results) != 1 {
		return false
	}
	n, _ := f.Sig.Results[0].(*Named)
	return n != nil && n.Obj.ctor == f
}

// entryPackage is the package of the context, w or one it embeds, whose
// own entries hold e; nil when none does.
func entryPackage(w *Context, e *ContextEntry, seen map[*Context]bool) *Package {
	if w == nil || seen[w] {
		return nil
	}
	seen[w] = true
	for _, own := range w.Entries {
		if own == e {
			return w.pkg
		}
		if p := entryPackage(own.Embedded, e, seen); p != nil {
			return p
		}
	}
	return nil
}
