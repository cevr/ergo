package v1

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"testing"
)

// TestPureErrorFuncs holds PureErrorFuncs to Go's own declarations (G4):
// for each package of PureImports, the exported top-level functions whose
// last result is error, read from the toolchain's sources for this GOOS
// and GOARCH, are exactly the table's. The checker trusts the table to
// demand or refuse a user extern's fails go.Error, so a missing name would
// let a target's error go unchecked to go build, and an extra one would
// refuse a correct extern.
func TestPureErrorFuncs(t *testing.T) {
	for _, path := range PureImports {
		p, err := build.Default.Import(path, "", 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var got []string
		fset := token.NewFileSet()
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil {
					continue
				}
				list := fd.Type.Results.List
				if id, ok := list[len(list)-1].Type.(*ast.Ident); ok && id.Name == "error" {
					got = append(got, fd.Name.Name)
				}
			}
		}
		slices.Sort(got)
		if want := PureErrorFuncs[path]; !slices.Equal(got, want) {
			t.Errorf("%s: Go's functions that return an error are %q; PureErrorFuncs has %q", path, got, want)
		}
	}
	for path := range PureErrorFuncs {
		if !slices.Contains(PureImports, path) {
			t.Errorf("PureErrorFuncs names %s, which is not in PureImports", path)
		}
	}
}
