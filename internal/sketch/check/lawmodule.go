package check

import (
	"fmt"
	"path"
	"slices"
)

// A law no case can feed, judged over its module (LAW-UNFED; types.md,
// Laws 10). A contract's laws live with the contract, in its domain
// package, and its providers in packages named after their dependency
// (memory, a file store), which import it: such a provider's laws run
// under a named context of the importing package (lawcontexts.go), and so
// does a provider of the domain package that only another package can
// build. The check of one package cannot see the packages that import
// it, so a binding no case of its own feeds leaves the package's law runs
// (reportUnfedProviders): it is neither run nor a skip there, in any
// load. It is an error only once no package of its module feeds it
// either, which ReportUnfed decides over a load that holds the whole
// module. One decision serves check and test, since the law runs a test
// build lowers are the checked LawRuns. Std is one module (StdModule). A
// package of no module is a program of its own and reports at once.

// unfedBinding is a binding of law to provider that no case of its
// package feeds (Package.unfed), and the diagnostic its package reports
// when no package of the module feeds it either. A binding of two other
// packages is never one: reportUnfedProviders drops it, and the
// provider's package judges it.
type unfedBinding struct {
	law      *Law
	provider *Named
	diag     *Diagnostic
}

// fedBinding is a law a named context of the package runs, against a
// provider of type provider (Package.fed).
type fedBinding struct {
	law      *Law
	provider Type
}

// StdModule names the standard library as one module for this rule:
// std's contracts are fed by contexts of its other packages (llm/memory
// feeds llm's Model laws) as a module's are, and StdSnapshotNow, which
// checks every std package, judges them (ReportUnfed).
const StdModule = "std"

// unfedModule is the module the package's law bindings are judged over:
// its loader's module, std for a std package, "" for a program of its
// own.
func (c *checker) unfedModule() string {
	if c.pkg.std {
		return StdModule
	}
	return c.pkg.module
}

// lawSite is where law is declared, as an unfed binding's error at its
// provider names it: FILE:LINE:COLUMN of the law's name, FILE the
// package-relative name and, for a law of another package, under that
// package's path; LINE:COLUMN alone for a file with no name.
func (c *checker) lawSite(law *Law) string {
	if law.file == nil || law.Decl == nil {
		return law.Pkg.Path
	}
	f := law.file.syntax
	line, col := f.Position(law.Decl.Name.Pos())
	site := fmt.Sprintf("%d:%d", line, col)
	name := f.Name
	if law.Pkg != c.pkg {
		name = path.Join(law.Pkg.Path, name)
	}
	if name != "" {
		site = name + ":" + site
	}
	return site
}

// prepared is the diagnostic report makes, run with no diagnostic kept:
// the check reports it later, or never (ReportUnfed).
func (c *checker) prepared(report func()) *Diagnostic {
	saved := c.diags
	c.diags = nil
	report()
	made := c.diags
	c.diags = saved
	if len(made) == 0 {
		return nil
	}
	return &made[0]
}

// ReportUnfed judges the law bindings that the checks of pkgs, every
// package of one module's own that one load checked, left to their
// module: a binding is fed when a named context of any of pkgs runs its
// law against a provider of its type, and that context's run is then the
// binding's; else it is the error its package prepared, returned by
// package. Only pkgs feed: a context of another module's package feeds
// nothing, so the caller passes its own module's packages and no others.
// A load of part of a module decides nothing and does not call it.
func ReportUnfed(pkgs []*Package) map[*Package][]Diagnostic {
	fed := map[*Law][]Type{}
	for _, p := range pkgs {
		for _, f := range p.fed {
			fed[f.law] = append(fed[f.law], f.provider)
		}
	}
	out := map[*Package][]Diagnostic{}
	// A member law some package runs, or binds to a provider that is
	// fed or reported, has a provider; any other is unfed at the law.
	provided := map[*Law]bool{}
	for _, p := range pkgs {
		for _, r := range p.LawRuns {
			if len(r.Bindings) > 0 || len(r.Contexts) > 0 {
				provided[r.Law] = true
			}
		}
		for _, u := range p.unfed {
			provided[u.law] = true
		}
	}
	for _, p := range pkgs {
		for _, u := range p.unprovided {
			if !provided[u.law] && u.diag != nil {
				out[p] = append(out[p], *u.diag)
			}
		}
	}
	for _, p := range pkgs {
		for _, u := range p.unfed {
			if u.diag == nil || slices.ContainsFunc(fed[u.law], func(t Type) bool { return sameProvider(t, u.provider) }) {
				continue
			}
			out[p] = append(out[p], *u.diag)
		}
	}
	return out
}

// sameProvider reports whether the type a context's entry builds is the
// provider type n: the one declaration at the same type arguments.
func sameProvider(t Type, n *Named) bool {
	m, ok := t.(*Named)
	return ok && m.Obj == n.Obj && m.String() == n.String()
}
