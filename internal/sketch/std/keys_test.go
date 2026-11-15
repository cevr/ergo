package std_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestStdRecordTable holds the Go types std declarations name (docs/spec/stdlib.md,
// The body contract; STD-NOPRIV NP4c), read through check.GoTypeOf. Each
// record that names one is an exported struct of runtime/v1 with the
// record's fields in order; no two declarations name one Go type; and
// every struct of runtime/v1 with an Error method, a failure a body can
// return, is named by a record, so a catch arm can name it.
func TestStdRecordTable(t *testing.T) {
	owner := map[string]string{}
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, tn := range pkg.Types {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			goName, ok := check.GoTypeOf(n)
			if !ok {
				continue
			}
			key := path + "." + tn.Name
			if other, dup := owner[goName]; dup {
				t.Errorf("v1.%s is the Go type of %s and of %s; a Go type is one declaration's", goName, other, key)
			}
			owner[goName] = key
			st, isRecord := n.Underlying().(*check.Struct)
			if !isRecord {
				if !check.Sealed(n) {
					t.Errorf("%s names v1.%s and is neither a record nor a sealed operation type", key, goName)
				}
				continue
			}
			var want []string
			for _, f := range st.Fields {
				want = append(want, f.Name)
			}
			got, err := v1.StructFieldsOf(goName, len(n.TParams))
			if err != nil {
				t.Errorf("%s: %v", key, err)
				continue
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: the record has fields %v, v1.%s has %v", key, want, goName, got)
			}
		}
	}
	if len(owner) == 0 {
		t.Fatal("no std declaration names a Go type")
	}
	for _, name := range errorStructs(t) {
		// GoError is the predeclared go.Error (G4), which no std package
		// declares; the lowering binds it by name (lower, goErrorDecl).
		if _, ok := owner[name]; !ok && name != "GoError" {
			t.Errorf("v1.%s has an Error method, so a body can fail with it, but no std declaration names it", name)
		}
	}
}

// errorStructs are the exported struct types of runtime/v1's embedded
// files that have a method Error, read with go/parser.
func errorStructs(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(v1.Source, "*.go")
	if err != nil {
		t.Fatal(err)
	}
	structs, errs := map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(v1.Source, name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() {
						if _, ok := ts.Type.(*ast.StructType); ok {
							structs[ts.Name.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil && d.Name.Name == "Error" && len(d.Recv.List) == 1 {
					if id, ok := d.Recv.List[0].Type.(*ast.Ident); ok {
						errs[id.Name] = true
					}
				}
			}
		}
	}
	var out []string
	for name := range errs {
		if structs[name] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	if len(out) < 5 {
		t.Fatalf("found %d error structs in runtime/v1 (%v); the walk reads no body file", len(out), out)
	}
	return out
}
