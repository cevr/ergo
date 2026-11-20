package snapshot

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestNodePathSurvivesSameShapeInsert (CS5b-4): a node path names the node
// of the same declaration after a declaration of the same shape is
// inserted before it in its file. Mutant: the declaration key is its shape
// alone (cs5b4-key-shape-only), so the path resolves to the inserted
// declaration's node.
func TestNodePathSurvivesSameShapeInsert(t *testing.T) {
	checked := func(src string) *check.Package {
		t.Helper()
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = "a.ergo"
		pkg, ds := check.Check("a", []*syntax.File{f}, nil)
		if len(ds) > 0 {
			t.Fatal(ds)
		}
		return pkg
	}
	before := checked("func Two(n int) int {\n\tn * 2\n}\n")
	after := checked("func One(n int) int {\n\tn * 1\n}\n\nfunc Two(n int) int {\n\tn * 2\n}\n")
	name := before.Files()[0].Decls[0].(*syntax.FuncDecl).Name
	p, ok := nodePaths(before)[name]
	if !ok {
		t.Fatal("Two's name has no node path")
	}
	x, ok := ResolveNodePath(after, string(p))
	if id, _ := x.(*syntax.Ident); !ok || id == nil || id.Name != "Two" {
		t.Errorf("Two's name resolves to %#v after One is inserted before it", x)
	}
}
