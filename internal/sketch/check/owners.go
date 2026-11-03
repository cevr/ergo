package check

import "ergo.local/prototype/internal/sketch/syntax"

// Fact ownership (196l; ledger row C20; stage E3a round 2 M2). Every fact
// the checker records comes from one kind of body, its owner, and the owner
// decides which bodies see the fact and which build reads it:
//
//   - OwnerProduction: a declaration of the program (a function, a type, a
//     package let, a context), with the function literals in it. Every body
//     sees its facts, and both builds read them: the run build (lower.Lower)
//     and the test build (lower.LowerTests).
//   - OwnerTest: a test body, with the function literals in it. Only test
//     bodies see its facts, and only the test build reads them.
//
// So a test changes neither the verdict on the rest of the program nor its
// run Go: a struct type that only a test's error row names is an error type
// in the tests and an ordinary value type everywhere else (types.md, Error
// types 1), and the run build emits no Error method for it. A function
// named Error on such a type is valid in the program but conflicts with the
// method the test build generates, so the conflict is the test's: S108 at
// the test's first row that names the type, never at the function
// (types.md, Methods 6; checkErrorMethods).
//
// Two kinds of fact carry an owner. Package.ErrorTypes are kept by owner.
// Every fact Info records is keyed by a syntax node, so its owner is the
// owner of that node (Package.OwnerOf), and a build that scans an Info map
// package-wide skips the nodes of owners it does not read.
//
// The owners are a closed set that later stages extend additively: a law
// body (stage E3b) is one more owner, whose facts only its own bodies see
// and neither build reads.
//
// Prior art: Go keeps a package's _test.go files out of its build, so no
// test declaration reaches the program's binary; Rust compiles #[cfg(test)]
// items only in the test configuration; Elixir compiles test/ only under
// mix test; Gleam and ReScript keep test modules out of the application's
// output. None of them lets a test change the program's own typing.
type FactOwner uint8

const (
	// OwnerProduction owns the facts of the program's declarations.
	OwnerProduction FactOwner = iota
	// OwnerTest owns the facts of test bodies.
	OwnerTest
)

// Sees reports whether a body of owner o sees a fact of owner f: every body
// sees the program's facts, and a body sees its own owner's.
func (o FactOwner) Sees(f FactOwner) bool {
	return f == OwnerProduction || f == o
}

// IsErrorType reports whether n, a struct type, is an error type in a body
// of this package of owner o: an error row of a body whose facts o sees
// names n's generic origin (types.md, Error types 1). Another package reads
// only the production facts of the package that declares a type, since a
// test of that package is not part of its program.
func (p *Package) IsErrorType(n *Named, o FactOwner) bool {
	n = n.origin()
	for f, set := range p.ErrorTypes {
		if set[n] && o.Sees(f) {
			return true
		}
	}
	return false
}

// OwnerOf is the owner of the fact Info records at n: OwnerTest for a node
// of a test declaration of this package, OwnerProduction for every other.
func (p *Package) OwnerOf(n syntax.Node) FactOwner {
	if o, ok := p.owners[n]; ok {
		return o
	}
	return OwnerProduction
}

// ownBody records o as the owner of every node of root, a declaration whose
// facts the program does not own.
func (c *checker) ownBody(root syntax.Node, o FactOwner) {
	if c.pkg.owners == nil {
		c.pkg.owners = map[syntax.Node]FactOwner{}
	}
	syntax.Inspect(root, func(n syntax.Node) bool {
		c.pkg.owners[n] = o
		return true
	})
}

// bodyOwner is the owner of a body check's facts: a test's, or the
// program's for a function or a package let.
func bodyOwner(body any) FactOwner {
	if _, ok := body.(*Test); ok {
		return OwnerTest
	}
	return OwnerProduction
}

// addErrorType records that the error row element at, of a body of owner o,
// names n's origin. The first element recorded stays: collectBodyErrorRows
// records every row in source order before any body is checked.
func (c *checker) addErrorType(n *Named, o FactOwner, at syntax.Expr) {
	n = n.origin()
	set := c.errorTypes[o]
	if set == nil {
		set = map[*Named]syntax.Expr{}
		c.errorTypes[o] = set
	}
	if _, seen := set[n]; !seen {
		set[n] = at
	}
}

// errorTypeIn reports whether n's origin is an error type in a body of
// this package of owner o (IsErrorType, before the facts are published).
func (c *checker) errorTypeIn(n *Named, o FactOwner) bool {
	n = n.origin()
	for f, set := range c.errorTypes {
		if _, ok := set[n]; ok && o.Sees(f) {
			return true
		}
	}
	return false
}

// publishErrorTypes publishes the error types by owner (Package.ErrorTypes).
func (c *checker) publishErrorTypes() {
	c.pkg.ErrorTypes = make(map[FactOwner]map[*Named]bool, len(c.errorTypes))
	for o, set := range c.errorTypes {
		out := make(map[*Named]bool, len(set))
		for n := range set {
			out[n] = true
		}
		c.pkg.ErrorTypes[o] = out
	}
}
