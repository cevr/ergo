package lower

import (
	"strings"
	"testing"
)

// TestLoweringIsTheSameInAnyDirectory: the emitted Go names each source
// relative to the unit's Root (binsize BS6), so one program of two files
// and an imported package, lowered from two directories, gives the same
// module bytes, and no byte names either directory. Then the same source
// builds the same executable in any checkout. Mutant bs6-abs-path.
func TestLoweringIsTheSameInAnyDirectory(t *testing.T) {
	lower := func(root string) string {
		lib := checkPackage(t, "app/lib", []pkgSource{{root + "/lib/lib.ergo", "let Base = twice(2)\n\nfunc twice(n int) int { n * 2 }\n\nfunc Get() int { Base }\n"}}, nil)
		u := checkPackage(t, "app", []pkgSource{
			{root + "/main.ergo", "import app/lib\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(other() + lib.Get())\")\n}\n"},
			{root + "/other.ergo", "func other() int { 1 }\n"},
		}, pkgImporter{"app/lib": lib.Package})
		u.Imports = []Unit{lib}
		u.Root = root
		files := mustLowerUnit(t, "paths", u, false).files
		var b strings.Builder
		for _, f := range files {
			b.WriteString("== " + f.Path + "\n" + string(f.Data))
		}
		return b.String()
	}
	a, b := lower("/checkout/one"), lower("/elsewhere/two/deeper")
	if a != b {
		t.Fatalf("the module differs between two directories:\n%s\n----\n%s", a, b)
	}
	if strings.Contains(a, "/checkout") || !strings.Contains(a, "//line lib/lib.ergo:") {
		t.Fatalf("the module names the build directory, or not lib/lib.ergo:\n%s", a)
	}
}
