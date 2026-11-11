package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The facts each build reads (check/owners.go; Test mode rule 1). The run
// build reads the program's facts only; the test build reads the tests'
// and the laws' facts as well (#162 law-run-1). Another package's facts are always its program's: its
// tests are not part of this one. So every package-wide scan of the
// checker's facts goes through readsFact or errorType, and a test adds
// nothing to the run build: no Error method, no text helper, no adapter,
// default, context builder or library name, and no read of a variable.

// buildView is the owner whose view of this package's facts the build
// lowers: the test build lowers the test and law bodies (ViewTestBuild),
// which see the program's facts and their own; the run build lowers the
// program's bodies only.
func (l *lowerer) buildView() check.FactOwner {
	if l.tests != nil {
		return check.ViewTestBuild
	}
	return check.OwnerProduction
}

// viewOf is the build's view of the facts of package p. A test build that
// lowers a law of an imported package (check.Package.LawRuns) reads that
// package's law facts too, never its tests'.
func (l *lowerer) viewOf(p *check.Package) check.FactOwner {
	if p == l.pkg {
		return l.buildView()
	}
	if l.tests != nil {
		if l.tests.lawPkgs == nil {
			l.tests.lawPkgs = map[*check.Package]bool{}
			for _, r := range l.pkg.LawRuns {
				if r.Law.Pkg != l.pkg && lawLowered(r) {
					l.tests.lawPkgs[r.Law.Pkg] = true
				}
			}
		}
		if l.tests.lawPkgs[p] {
			return check.OwnerLaw
		}
	}
	return check.OwnerProduction
}

// readsFact reports whether this build reads the fact p's Info records at
// n.
func (l *lowerer) readsFact(p *check.Package, n syntax.Node) bool {
	return l.viewOf(p).Sees(p.OwnerOf(n))
}

// errorTypeIn reports whether named is an error type in a body of package
// p of owner o (types.md, Error types 1): a row of p, or of the package
// that declares named, that o sees names it. Each package keeps its own
// view: a row of the root that names http.NotFound makes it an error type
// in the root's bodies, never in http's, whose Match returns it as a value
// (wf10 D1).
func errorTypeIn(p *check.Package, named *check.Named, o check.FactOwner) bool {
	if named.Orig != nil {
		named = named.Orig
	}
	if p.IsErrorType(named, o) {
		return true
	}
	d := named.Obj.Pkg
	return d != nil && d != p && d.IsErrorType(named, check.OwnerProduction)
}

// errorType reports whether named is an error type in the body of some
// package the build lowers, each read in the build's view of it (viewOf).
// It decides a declaration's shape (an Error method, a text helper, no flat
// form): the program's packages share one Go package, so a type that one
// package's row names needs its Error method in every one, while a return
// reads its own body's package and owner (errorTypeIn).
func (l *lowerer) errorType(named *check.Named) bool {
	if errorTypeIn(l.pkg, named, l.buildView()) {
		return true
	}
	for _, p := range l.libraries() {
		if errorTypeIn(p, named, l.viewOf(p)) {
			return true
		}
	}
	return false
}

// bodyPkg is the package whose body the lowering is in: the package of the
// Info it reads (sources.go, decls.go swap it per body).
func (l *lowerer) bodyPkg() *check.Package {
	if l.info == l.pkg.Info {
		return l.pkg
	}
	for _, p := range l.libraries() {
		if p.Info == l.info {
			return p
		}
	}
	return l.pkg
}

// errorTypesRead are the error types of package p this build reads.
func (l *lowerer) errorTypesRead(p *check.Package) []*check.Named {
	view := l.viewOf(p)
	var out []*check.Named
	for o, set := range p.ErrorTypes {
		if !view.Sees(o) {
			continue
		}
		for n := range set {
			out = append(out, n)
		}
	}
	return out
}
