package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Reach of a test's and a law run's verdict (`ergo test --affected`,
// CI-SELECT CS-AFFECTED). A test block's digest follows TargetDigest's
// rule: the test's declaration, every function, type and package let it
// reaches at any depth and in any package, the types of its parameters,
// and the generators of every type reached, each by its tokens (no
// position, space or comment). Reach lists those declarations, so a
// reader can name the one whose edit changed a digest.

// ReachedDecl is one declaration a verdict reads: its key (kind, package
// path and name, "func ergo.local/m/lib.tally"), its token text, its
// package and its node.
type ReachedDecl struct {
	Key  string
	Text string
	Pkg  *check.Package
	Node syntax.Node
}

// Position is the declaration's file name and 1-based line, from the file
// of its package that holds it; "" and 0 when no file does.
func (r ReachedDecl) Position() (string, int) {
	if r.Pkg == nil || r.Node == nil {
		return "", 0
	}
	for _, f := range r.Pkg.Files() {
		for _, decl := range f.Decls {
			if syntax.Node(decl) == r.Node && int(r.Node.Pos()) <= len(f.Src) && r.Node.Pos() >= 0 {
				return f.Name, strings.Count(f.Src[:r.Node.Pos()], "\n") + 1
			}
		}
	}
	return "", 0
}

// TestLabel is the name the report gives the test t: `test NAME`.
func TestLabel(t *check.Test) string { return "test " + t.Name }

// TestDigest is the digest of every input of the test t's verdict in the
// test build of pkg.
func TestDigest(pkg *check.Package, t *check.Test) string {
	return testWalk(pkg, t).sum(pkg, nil)
}

// TestReach is the declarations TestDigest reads, in walk order, each
// once.
func TestReach(pkg *check.Package, t *check.Test) []ReachedDecl {
	d := testWalk(pkg, t)
	d.generators(pkg)
	return d.nodes
}

// Reach is the declarations t.Digest reads, in walk order, each once.
func (t RunTarget) Reach(pkg *check.Package) []ReachedDecl {
	var d *digester
	if t.Context != nil {
		d, _ = contextWalk(t.Law, *t.Context)
	} else {
		d, _ = targetWalk(t.Law, t.Binding)
	}
	d.generators(pkg)
	return d.nodes
}

func testWalk(pkg *check.Package, t *check.Test) *digester {
	d := newDigester()
	if t.Decl != nil {
		d.decl("test "+d.names.Of(pkg)+"."+t.Name, pkg, t.Decl)
	} else {
		d.decls["test "+t.Name] = true
	}
	for _, p := range t.Params {
		if n, ok := p.Type.(*check.Named); ok {
			d.typ(n.Obj)
		}
	}
	return d
}
