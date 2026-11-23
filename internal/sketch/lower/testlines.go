package lower

// TestLines is what the test binary of a unit prints when it runs in full
// (no --law, --skip-law or --run): the name of each verdict line, in the
// order the runner prints them, and the count its summary line gives.
// `ergo test` answers a package from the check-time evaluator (TESTSPEED
// FT1) only when it can write every line the native runner would.
type TestLines struct {
	// Names are the tests' names in canonical order (runTests), then each
	// law run's label (BindingLabel, or "law L under C" for a run under a
	// context), the law runs in canonical order (runLaws), a law's runs
	// under contexts after its bindings, as the runner defers them.
	Names []string
	// Plain is false when the runner prints a line that is not a verdict
	// or the summary: a skip line (a law, a binding or a context that does
	// not run), a provider note (a law no provider binds), or a machine's
	// reachability law (its own report). It is false as well when two
	// runs share a label, since a line then names no one run.
	Plain bool
}

// TestLinesOf is the test binary's lines of u, as LowerTests and
// LowerIRTests lower it (no rewrite instances, posts or imported laws).
func TestLinesOf(u Unit) TestLines {
	c := canonicalize(u)
	pkg := u.Package
	out := TestLines{Plain: len(u.Instances) == 0 && len(u.Posts) == 0 && len(u.ImportLaws) == 0}
	seen := map[string]bool{}
	add := func(name string) {
		if seen[name] {
			out.Plain = false
		}
		seen[name] = true
		out.Names = append(out.Names, name)
	}
	for _, t := range c.tests(pkg) {
		add(t.Name)
	}
	for _, r := range c.lawRuns(pkg) {
		law := r.Law
		switch {
		case law.Unrun != "":
			out.Plain = false
			continue
		case law.Machine != nil && law.Decl != nil && law.Decl.Reach != nil:
			out.Plain = false
			continue
		case len(r.Bindings) == 0 && len(r.Contexts) == 0:
			out.Plain = false // the provider note
			continue
		}
		for _, b := range r.Bindings {
			if bindingUnrun(b) != "" {
				out.Plain = false
				continue
			}
			add(BindingLabel(pkg, law, b))
		}
		for _, lc := range r.Contexts {
			if lc.Unrun != "" {
				out.Plain = false
				continue
			}
			add("law " + law.Title(pkg) + " under " + lc.Title())
		}
	}
	return out
}
