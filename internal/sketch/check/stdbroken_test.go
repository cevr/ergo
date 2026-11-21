package check

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdImportNamesBrokenPackage is the smallest repro of TM's bug: a std
// package with a check error made each importer report S104 "unknown
// package", which hid the error (actor's `unknown package "tasks"` while
// tasks had an S101). The import now names the package that does not check
// and its first diagnostic with its file, through every importer on the
// way. Mutants: std-broken-as-unknown, std-broken-no-file.
func TestStdImportNamesBrokenPackage(t *testing.T) {
	lib := map[string]map[string]string{
		"zzbroke": {"broke.ergo": "func Go() int { missing }\n"},
		"zzuser":  {"user.ergo": "import zzbroke\n\nfunc Start() int { zzbroke.Go() }\n"},
	}
	s := newStdImporter()
	s.sources = func(path string) (map[string]string, bool) {
		files, ok := lib[path]
		return files, ok
	}
	f, err := syntax.Parse("import zzuser\n\nfunc main() {\n\tlet _ = zzuser.Start()\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	_, diags := Check("main", []*syntax.File{f}, s)
	if len(diags) != 1 {
		t.Fatalf("want one diagnostic at the import, got %v", diags)
	}
	got := diags[0].String()
	for _, want := range []string{`1:8: S104 package "zzuser" does not check: user.ergo:1:8: S104 package "zzbroke" does not check: broke.ergo:1:17: S104`, "missing"} {
		if !strings.Contains(got, want) {
			t.Fatalf("got %q\nwant it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "unknown package") {
		t.Fatalf("got %q: a package that does not check is not unknown", got)
	}
}
