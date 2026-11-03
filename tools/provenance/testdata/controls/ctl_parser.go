package check

// The parser, the caller-text entry and the trusted helpers are pinned to
// their sites (E2b round 4 review M2, E2c round 1 review M1, m1, m3): a call
// in a pass, a value, a closure at a listed site, a syntax method off the
// manifest called directly or through an interface, and made or clone used
// as a value. The test lists parseValueAtSite, closureAtSite,
// closureReturnedAtSite, closureNestedAtSite$1 and
// (*checker).closureInMethodAtSite as Parse sites, so each control fails on
// its flow, not on its site.

import "ergo.local/prototype/internal/sketch/syntax"

// The closureHook mutant (check.go patch): CheckSource sets snippetParse,
// and a pass parses a snippet through it.
var snippetParse func(string) (*syntax.File, error)

func snippetParsed() syntax.Node {
	f, _ := snippetParse("type T int\n")
	return f.Decls[0]
}

func (c *checker) passParsed() syntax.Node {
	f, _ := syntax.Parse("type T int\n") // want `syntax callee Parse is called in \(\*checker\).passParsed, which the manifest does not list for it`
	return f.Decls[0]
}

var parseText = syntax.Parse // want `syntax callee Parse is used as a value in init; its listed sites may only call it`

var parseHook func(string) (*syntax.File, error)

func parseValueAtSite() {
	syntax.Parse("")
	parseHook = syntax.Parse // want `syntax callee Parse is used as a value in parseValueAtSite`
}

func (c *checker) reparsed() *Package {
	p, _ := CheckSource("type T int\n") // want `CheckSource parses caller text and is referenced in \(\*checker\).reparsed, which the manifest does not list for it`
	return p
}

func closureAtSite() {
	syntax.Parse("")
	parseHook = func(s string) (*syntax.File, error) { return syntax.Parse(s) } // want `syntax callee Parse is called in closureAtSite\$1, which the manifest does not list` `closureAtSite\$1 makes a node outside the manifest and becomes a function value in closureAtSite`
}

func closureReturnedAtSite() func(string) (*syntax.File, error) {
	syntax.Parse("")
	_ = func() {}
	return func(s string) (*syntax.File, error) { return syntax.Parse(s) } // want `syntax callee Parse is called in closureReturnedAtSite\$2` `closureReturnedAtSite\$2 makes a node outside the manifest and becomes a function value`
}

func closureNestedAtSite() {
	func() {
		syntax.Parse("")
		_ = func() (*syntax.File, error) { return syntax.Parse("") } // want `syntax callee Parse is called in closureNestedAtSite\$1\$1, which the manifest does not list`
	}()
}

func (c *checker) closureInMethodAtSite() func() {
	syntax.Parse("")
	return func() { syntax.Parse("") } // want `syntax callee Parse is called in \(\*checker\).closureInMethodAtSite\$1` `\(\*checker\).closureInMethodAtSite\$1 makes a node outside the manifest`
}

func closureCaptures(s string) func() {
	return func() { syntax.Parse(s) } // want `syntax callee Parse is called in closureCaptures\$1` `closureCaptures\$1 makes a node outside the manifest and becomes a closure in closureCaptures`
}

func closureCheckSource() func() {
	return func() { CheckSource("") } // want `CheckSource parses caller text and is referenced in closureCheckSource\$1` `closureCheckSource\$1 makes a node outside the manifest`
}

// SSA calls a local function value of clone directly, so the construction
// is a site the manifest must list.
func cloneValue(c *checker, p *syntax.Ident) syntax.Node {
	f := clone[syntax.Ident, *syntax.Ident]
	return f(c, p) // want `construction site cloneValue makes a Ident and is not in the manifest`
}

var cloneIdent func(*checker, *syntax.Ident) *syntax.Ident = clone // want `clone is used as a value in init; it may only be called`

func madeValue() func(*checker, *syntax.Ident, syntax.Node) *syntax.Ident {
	return made[*syntax.Ident] // want `made is used as a value in madeValue; it may only be called`
}

func syntaxMethodCalled(id *syntax.Ident) *syntax.Ident { return id.E2dCopy() } // want `syntax callee \(\*Ident\).E2dCopy yields a node and is not in the manifest`

func syntaxMethodValue(id *syntax.Ident) func() *syntax.Ident { return id.E2dCopy } // want `syntax callee \(\*Ident\).E2dCopy is used as a value in syntaxMethodValue`

type identCopier interface{ E2dCopy() *syntax.Ident }

// An implicit conversion has no position of its own; it is reported at its
// function.
func syntaxMethodThroughInterface(id *syntax.Ident) *syntax.Ident { // want `syntax method \(\*Ident\).E2dCopy yields a node through interface identCopier in syntaxMethodThroughInterface`
	var c identCopier = id
	return c.E2dCopy()
}

func syntaxMethodThroughNode(n syntax.Node) *syntax.Ident {
	return n.(identCopier).E2dCopy() // want `syntax method \(\*Ident\).E2dCopy yields a node through interface identCopier in syntaxMethodThroughNode`
}
