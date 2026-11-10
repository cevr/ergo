package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The facts each build reads (check/owners.go; Test mode rule 1). The run
// build reads the program's facts only; the test build reads the tests'
// and the laws' facts as well (#162 law-run-1). Another package's facts are always its program's: its
// tests are not part of this one. So every package-wide scan of the
// checker's facts goes through readsFact, errorType or errorShape, and a test adds
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

// errorType reports whether named is an error type in the body being
// lowered, of owner o of the body's package (types.md, Error types 1): a
// row of the body's package that o sees names it, or a row of the program
// of the package that declares named. The body's package is the one whose
// facts the lowering reads (bodyPkg): a body of an imported package reads
// its own rows, not the root's, so a type that only an importer raises is
// a failure there (gap-fail, G-L5-2). A return reads it (retExpr).
func (l *lowerer) errorType(named *check.Named, o check.FactOwner) bool {
	if named.Orig != nil {
		named = named.Orig
	}
	p := l.bodyPkg()
	if p.IsErrorType(named, o) {
		return true
	}
	d := named.Obj.Pkg
	return d != nil && d != p && d.IsErrorType(named, check.OwnerProduction)
}

// errorShape reports whether named is an error type in some body of the
// program the build lowers: a row of the root, or of a package it reaches,
// that the build's view of that package sees names it (errorTypesRead).
// A Go type has one shape in every package, so its Error method, its text
// helper and its union form read this, never one package's rows: a type
// that a.ergo declares and only b.ergo raises is a Go error in a's
// declaration too (gap-fail).
func (l *lowerer) errorShape(named *check.Named) bool {
	if named.Orig != nil {
		named = named.Orig
	}
	if l.pkg.IsErrorType(named, l.buildView()) {
		return true
	}
	for _, p := range l.libraries() {
		if p.IsErrorType(named, l.viewOf(p)) {
			return true
		}
	}
	return false
}

// bodyPkg is the package whose facts the lowering reads now (l.info): the
// package of the body being lowered (sources.go, enter).
func (l *lowerer) bodyPkg() *check.Package {
	if l.info == nil || l.info == l.pkg.Info {
		return l.pkg
	}
	for _, p := range l.libraries() {
		if p.Info == l.info {
			return p
		}
	}
	panic("lower: the facts being read are of no package the program reaches")
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
