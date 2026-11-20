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
// it, so a binding no case of its own feeds is an error only once no
// package of its module feeds it either: reportUnfedProviders prepares
// the diagnostic and leaves the binding to ReportUnfed, which the loader
// calls over the module's packages. A package of no module is a program
// of its own and reports at once.

// unfedBinding is a binding of law to provider that no case of its
// package feeds (Package.unfed), and the diagnostic its package reports
// when no package of the module feeds it either; nil when the package
// declares neither the law nor the provider, and the binding then keeps
// its skip.
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

// ReportUnfed judges the law bindings that the checks of pkgs, the
// packages of one module one load checked, left to their module: a
// binding is fed when a named context of any of pkgs runs its law
// against a provider of its type, and that context's run is then the
// binding's, so it leaves its package's law runs (as builtByContext drops
// one its own package feeds); else it is the error its package prepared,
// returned by package. Only pkgs feed: a context of another module's
// package feeds nothing, so the caller passes its own module's packages
// and no others.
func ReportUnfed(pkgs []*Package) map[*Package][]Diagnostic {
	fed := map[*Law][]Type{}
	for _, p := range pkgs {
		for _, f := range p.fed {
			fed[f.law] = append(fed[f.law], f.provider)
		}
	}
	out := map[*Package][]Diagnostic{}
	for _, p := range pkgs {
		for _, u := range p.unfed {
			if slices.ContainsFunc(fed[u.law], func(t Type) bool { return sameProvider(t, u.provider) }) {
				p.dropBinding(u)
				continue
			}
			if u.diag != nil {
				out[p] = append(out[p], *u.diag)
			}
		}
		p.unfed, p.fed = nil, nil
	}
	return out
}

// sameProvider reports whether the type a context's entry builds is the
// provider type n: the one declaration at the same type arguments.
func sameProvider(t Type, n *Named) bool {
	m, ok := t.(*Named)
	return ok && m.Obj == n.Obj && m.String() == n.String()
}

// dropBinding removes from p's run of u's law each one-slot binding of
// u's provider: a context of the module runs the law against it.
func (p *Package) dropBinding(u unfedBinding) {
	for i := range p.LawRuns {
		r := &p.LawRuns[i]
		if r.Law != u.law {
			continue
		}
		r.Bindings = slices.DeleteFunc(r.Bindings, func(b []LawProvider) bool {
			return len(b) == 1 && b[0].Type.Obj == u.provider.Obj && b[0].Type.String() == u.provider.String()
		})
	}
}
