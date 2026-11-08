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
	v1 "ergo.local/prototype/runtime/v1"
)

// TestStdRecordTable holds the binding table of std records (v1.StdRecord;
// docs/spec/stdlib.md, Types 4; std-align S3b). Each row names an exported
// record of its std package and an exported struct of runtime/v1 with the
// record's fields in order; no two rows name one struct; and every struct
// of runtime/v1 with an Error method, a failure a body can return, is the
// struct of a row, so a catch arm can name it.
func TestStdRecordTable(t *testing.T) {
	rows := v1.StdRecords()
	if len(rows) == 0 {
		t.Fatal("the binding table is empty")
	}
	owner := map[string]string{}
	for key, goName := range rows {
		path, name, ok := strings.Cut(key, ".")
		if !ok || path == "" || name == "" || strings.Contains(name, ".") {
			t.Errorf("row %q is not pkg.Name", key)
			continue
		}
		if got, ok := v1.StdRecord(path, name); !ok || got != goName {
			t.Errorf("StdRecord(%q, %q) = %q, %v; the table says %q", path, name, got, ok, goName)
		}
		if other, dup := owner[goName]; dup {
			t.Errorf("v1.%s is the struct of %s and of %s; a Go type is one record", goName, other, key)
		}
		owner[goName] = key
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Errorf("row %s: %v", key, err)
			continue
		}
		var want []string
		found := false
		for _, tn := range pkg.Types {
			if tn.Name != name {
				continue
			}
			var st *check.Struct
			if n, ok := tn.Type.(*check.Named); ok {
				st, _ = n.Underlying().(*check.Struct)
			}
			if st == nil || !isExportedName(name) {
				t.Errorf("row %s: %s.%s is not an exported record", key, path, name)
				break
			}
			found = true
			for _, f := range st.Fields {
				want = append(want, f.Name)
			}
		}
		if !found {
			t.Errorf("row %s names no record of std %s", key, path)
			continue
		}
		got, err := v1.StructFields(goName)
		if err != nil {
			t.Errorf("row %s: %v", key, err)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("row %s: the record has fields %v, v1.%s has %v", key, want, goName, got)
		}
	}
	for _, name := range errorStructs(t) {
		if _, ok := owner[name]; !ok {
			t.Errorf("v1.%s has an Error method, so a body can fail with it, but no row of the binding table names it", name)
		}
	}
}

// TestStdRecordBindsByRow: the table is asked with the record's package
// and name. A name that is a runtime struct's binds nothing in a package
// with no row for it.
func TestStdRecordBindsByRow(t *testing.T) {
	if got, ok := v1.StdRecord("fs", "NotFound"); !ok || got != "NotFound" {
		t.Errorf("StdRecord(fs, NotFound) = %q, %v", got, ok)
	}
	for _, key := range [][2]string{{"fixturea", "NotFound"}, {"strings", "Halves"}, {"exec", "NotFound"}, {"queue", "NotFound"}, {"fs", "Closed"}, {"fs.NotFound", ""}, {"", "fs.NotFound"}} {
		if got, ok := v1.StdRecord(key[0], key[1]); ok {
			t.Errorf("StdRecord(%q, %q) = %q; want no row", key[0], key[1], got)
		}
	}
	// The copy is a copy.
	rows := v1.StdRecords()
	rows["fs.NotFound"] = "Closed"
	if got, _ := v1.StdRecord("fs", "NotFound"); got != "NotFound" {
		t.Error("StdRecords returned the table itself")
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
