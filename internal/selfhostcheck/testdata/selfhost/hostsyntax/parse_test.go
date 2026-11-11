package hostsyntax

import (
	"go/importer"
	"go/token"
	"go/types"
	"testing"
)

// TestFamiliesCoverSyntax holds the encoding's family table to the syntax
// package: every struct type whose pointer is a syntax.Node is a family,
// and every family is one. The corpus of TestSelfhostBridgeAgrees reaches
// only the constructs some source writes, so a node type added to the
// parser with no source yet would be a U document there only once a
// source uses it; this check names it at once.
//
// Mutant it alone catches (internal/selfhostcheck/testdata/mutants.json):
// sh-family-table-misses-node.
func TestFamiliesCoverSyntax(t *testing.T) {
	const path = "ergo.local/prototype/internal/sketch/syntax"
	pkg, err := importer.ForCompiler(token.NewFileSet(), "source", nil).Import(path)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := pkg.Scope().Lookup("Node").Type().Underlying().(*types.Interface)
	if node == nil {
		t.Fatal("syntax.Node is not an interface")
	}
	want := map[string]bool{}
	for _, name := range pkg.Scope().Names() {
		tn, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !tn.Exported() {
			continue
		}
		if _, ok := tn.Type().Underlying().(*types.Struct); ok && types.Implements(types.NewPointer(tn.Type()), node) {
			want[name] = true
		}
	}
	have := map[string]bool{}
	for f := range families {
		have[f.Name()] = true
		if f.PkgPath() != path {
			t.Errorf("family %s is not a syntax type", f)
		}
	}
	for name := range want {
		if !have[name] {
			t.Errorf("syntax.%s is a node the encoding does not carry: add it to families", name)
		}
	}
	for name := range have {
		if !want[name] {
			t.Errorf("family %s is not a syntax node", name)
		}
	}
}
