package lower

import (
	"strings"
	"testing"
)

// Round 1 verify of 196b U1 (pkg-lower): keys and locations in a program
// of several files and packages. A syntax position is an offset into one
// file, and a display string names a package by its last path element, so
// neither is an identity once a program has two files or two packages
// whose paths end alike. The cases below are the verifier's probes and the
// properties that generalise them.

// buildAndRun builds the programs and checks each one's stdout and exit 0.
func buildAndRun(t *testing.T, programs []*lowered, want map[string]string) {
	t.Helper()
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the lowered programs do not build")
	}
	for _, p := range programs {
		out, stderr, code := runExe(t, exes[p.name])
		if out != want[p.name] || code != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q; stderr:\n%s", p.name, code, out, want[p.name], stderr)
		}
	}
}

func TestVerifyR1ImportedRefusal(t *testing.T) {
	lib := checkPackage(t, "lib", []pkgSource{{"/probe/lib/a.ergo", "func X() []int { []int{2: 5} }\n"}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/main.ergo", "import lib\nfunc main() { let _ = lib.X() }\n"}}, pkgImporter{"lib": lib.Package})
	root.Imports = []Unit{lib}
	o := lowerUnit("ref", root, false)
	if o.err != nil || o.panicked != nil || len(o.refused) != 1 || o.refused[0].Path != "/probe/lib/a.ergo" || o.refused[0].Source != 1 {
		t.Fatalf("err %v panic %v refusals %+v", o.err, o.panicked, o.refused)
	}
}

// opsLib is a user package that exports the operation type Read, the
// record Q with the Get operation of Read, and Make, which converts a Q
// into a Read.
func opsLib(n string) string {
	return "type Read { Get() int }\ntype Q { N int }\nfunc Get(q Q) int { q.N }\nfunc Make() Read { Q{N: " + n + "} }\n"
}

// The text of an error type's instances over two packages' Q, whose
// displays are both Oops[lib.Q]: each instance has its own text function.
func TestErrorTextInstancesAcrossPackages(t *testing.T) {
	needsBuild(t)
	a := checkPackage(t, "x/lib", []pkgSource{{"/probe/x/lib.ergo", opsLib("1")}}, nil)
	b := checkPackage(t, "y/lib", []pkgSource{{"/probe/y/lib.ergo", "type Q { M string }\n"}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/text.ergo", `import a x/lib
import b y/lib
type lib {}
type Oops[T] { v T }
func f() fails Oops[a.Q] { Oops[a.Q]{v: a.Q{N: 1}} }
func g() fails Oops[b.Q] { Oops[b.Q]{v: b.Q{M: "two"}} }
func main() {}
test F { try f() }
test G { try g() }
`}}, pkgImporter{"x/lib": a.Package, "y/lib": b.Package})
	root.Imports = []Unit{a, b}
	p := mustLowerUnit(t, "errtext", root, true)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	out, stderr, _ := runExe(t, exes[p.name])
	if !strings.Contains(out, "Oops{v:{N:1}}") || !strings.Contains(out, "Oops{v:{M:two}}") {
		t.Fatalf("the reports do not show both instances:\n%s%s", out, stderr)
	}
}
