package check

import (
	"sort"
)

// Laws under a context (types.md, Laws 10; L5, #237 stage 3). A member law
// of an operation type T also runs once for each named context of this
// package that binds T: each case builds the context, as a with of it
// does, and runs the law against the provider the context binds, so the
// provider is the one the program uses, made by its constructor with its
// arguments and its other capabilities in place. A context whose build
// acquires a provider with a Close does not run (LawContext.Unrun): the
// case would have to release it. Only a law whose one slot is its owner
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
}

// planLawContexts adds to each law run the named contexts of this package
// that bind its owner, and a run for each law of another package that has
// only such contexts here. It runs after the ownership pass, so the
// acquisitions and Closes are known.
func (c *checker) planLawContexts() {
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
	for _, q := range pkgs {
		for _, law := range q.Laws {
			if law.Unrun != "" || law.Owner == nil || len(law.Slots) != 1 || law.Slots[0].Param >= 0 {
				continue
			}
			var under []LawContext
			for _, name := range names {
				if lc, ok := c.lawContext(law, c.pkg.Contexts[name]); ok {
					under = append(under, lc)
				}
			}
			if len(under) == 0 {
				continue
			}
			if i, ok := runs[law]; ok {
				c.pkg.LawRuns[i].Contexts = under
				continue
			}
			runs[law] = len(c.pkg.LawRuns)
			c.pkg.LawRuns = append(c.pkg.LawRuns, LawRun{Law: law, Contexts: under})
		}
	}
}

// lawContext is the run of law under the named context w, when w binds its
// owner (an instance of it, for a generic owner).
func (c *checker) lawContext(law *Law, w *Context) (LawContext, bool) {
	own := law.Slots[0].Type
	var cap Type = own
	var params []Type
	if len(own.TParams) > 0 {
		// A generic owner: the instance w binds, the first in key order.
		var inst *Named
		w.binds.each(&contextMeter{limit: 1 << 30}, func(n *capNode) bool {
			if t, ok := n.t.(*Named); ok && t.Orig == own && inst == nil {
				inst = t
			}
			return inst == nil
		})
		if inst == nil {
			return LawContext{}, false
		}
		cap = inst
		m := map[*TypeParam]Type{}
		for i, tp := range own.TParams {
			m[tp] = inst.TArgs[i]
		}
		for _, p := range law.Params {
			params = append(params, c.subst(p.Type, m))
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
	for _, a := range fact.Acquired {
		if a.Close != nil {
			lc.Unrun = "its build acquires a provider with a Close, which a law case does not release"
			break
		}
	}
	if lc.Unrun == "" && params != nil {
		// A non-generic law's parameters were judged when it was planned.
		for i, p := range law.Params {
			if why := c.ungeneratableFrom(params[i], law.Pkg); why != "" {
				lc.Unrun = "its parameter " + p.Name + " has no generator at " + cap.String() + ": " + why
				break
			}
		}
	}
	return lc, true
}
