package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdLocateNamesItsOwnFile: a std body's defect site names the std
// file that holds it when another file of the package has a declaration
// over the same offsets (offsets count from each file's start). It kills
// lw1-std-locate-by-span (stdFileOf takes the first declaration whose span
// holds the node's, without looking for the node in it): the one-file
// fixture of TestStdGapPrelude cannot tell the files apart. In process:
// the location is text of the lowered Go.
func TestStdLocateNamesItsOwnFile(t *testing.T) {
	// a.ergo's declaration spans every offset of b.ergo's Pick.
	files := map[string]string{
		"a.ergo": "func Pad(n int) int {\n\tlet a = n + 1\n\tlet b = a + 1\n\tlet c = b + 1\n\tlet d = c + 1\n\td\n}\n",
		"b.ergo": "func Pick(n int) int {\n\tif n > 1 {\n\t\tUnreachable(\"statement\")\n\t}\n\tn\n}\n",
	}
	var parsed []*syntax.File
	for _, name := range []string{"a.ergo", "b.ergo"} {
		f, err := syntax.Parse(files[name])
		if err != nil {
			t.Fatal(err)
		}
		f.Name = name
		parsed = append(parsed, f)
	}
	pkg, ds := check.StdFixture("twofiles", parsed)
	if len(ds) != 0 {
		t.Fatalf("std fixture: %v", ds)
	}
	const src = "import twofiles\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(twofiles.Pick(0)) \\(twofiles.Pad(0))\")\n}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	root, ds := check.Check("main", []*syntax.File{file}, pkgImporter{"twofiles": pkg})
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	code, err := lowerPackage(Unit{Package: root, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, false)
	if err != nil {
		t.Fatalf("lowering: %v", err)
	}
	if want := "std/twofiles/b.ergo:3:3"; !strings.Contains(string(code), want) {
		t.Errorf("the lowered Go does not name %s; its std locations: %v", want, stdLocations(string(code)))
	}
}

// stdLocations are the std/ file locations a lowered text spells.
func stdLocations(code string) []string {
	var out []string
	for _, f := range strings.Fields(code) {
		if i := strings.Index(f, "std/"); i >= 0 {
			out = append(out, f[i:])
		}
	}
	return out
}
