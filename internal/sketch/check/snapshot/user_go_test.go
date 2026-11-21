package snapshot_test

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/check/snapshot"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goOnlyImporter resolves Go imports only, from Go's standard library.
type goOnlyImporter struct{ set *check.GoBindings }

func (goOnlyImporter) Import(path string) (*check.Package, error) {
	return nil, fmt.Errorf("no Ergo package %s", path)
}

func (g goOnlyImporter) GoBindings() *check.GoBindings { return g.set }

// TestUserExportCarriesGoImports is the smallest repro of B7e (batch-19
// full-r2-triage, TestPathsRoundTrip): a user package that imports a Go
// package did not export, because its segment reached the program's Go
// bindings (their lookup function) and a binding's importer (goImp, which
// had no codec). The program's GoBindings is an external of the export, as
// its loader is, and the segment decodes to the same interface.
func TestUserExportCarriesGoImports(t *testing.T) {
	f, err := syntax.Parse("import go strings\n\nfunc Up(s string) string {\n\tstrings.ToUpper(s)\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	imp := goOnlyImporter{check.NewGoBindings(nil)}
	pkg, ds := check.Check("lib", []*syntax.File{f}, imp)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	u, err := snapshot.NewUserExport(imp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Add(pkg); err != nil {
		t.Fatalf("export: %v", err)
	}
	got, err := u.Decode("lib", func(string) *check.Package { return nil }, imp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g, w := check.InterfaceText(got), check.InterfaceText(pkg); g != w {
		t.Fatalf("decoded interface\n%s\nwant\n%s", g, w)
	}
}
