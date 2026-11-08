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

// viewOf is the build's view of the facts of package p.
func (l *lowerer) viewOf(p *check.Package) check.FactOwner {
	if p == l.pkg {
		return l.buildView()
	}
	return check.OwnerProduction
}

// readsFact reports whether this build reads the fact p's Info records at
// n.
func (l *lowerer) readsFact(p *check.Package, n syntax.Node) bool {
	return l.viewOf(p).Sees(p.OwnerOf(n))
}

// errorType reports whether named is an error type in a body of this
// package of owner o (types.md, Error types 1): a row of this package, or of
// the package that declares named, that o sees names it. A declaration's
// shape (an Error method, a text helper) reads the build's view, since the
// build lowers every body of it; a return reads its own body's owner.
func (l *lowerer) errorType(named *check.Named, o check.FactOwner) bool {
	if named.Orig != nil {
		named = named.Orig
	}
	if l.pkg.IsErrorType(named, o) {
		return true
	}
	p := named.Obj.Pkg
	return p != nil && p != l.pkg && p.IsErrorType(named, check.OwnerProduction)
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
