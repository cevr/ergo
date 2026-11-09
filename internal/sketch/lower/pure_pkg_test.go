package lower

import (
	"strings"
	"testing"
)

// The packages of TestPurityAcrossPackages, a chain main → a → b → c. In
// c, Double is pure and Leaf is not (its row fails Bad). b.Twice calls
// c.Double and b.Mid catches c.Leaf: no row, an empty footprint, so only
// the callee's own purity decides. a.Quad and a.Top call those, so the
// fact crosses three packages. a.Say needs Console.
const (
	purePkgC = `type Bad {}

func Double(n int) int { n * 2 }

func Leaf(n int) int fails Bad {
	if n < 0 {
		return Bad
	}
	n + 1
}
`
	purePkgB = `import c

func Twice(n int) int { c.Double(c.Double(n)) }

func Mid(n int) int {
	c.Leaf(n) catch {
	case c.Bad:
		0
	}
}
`
	purePkgA = `import b

func Quad(n int) int { b.Twice(n) + 1 }

func Top(n int) int { b.Mid(n) * 10 }

func Say(s string) needs Console { Console.Println(s) }
`
	purePkgMain = `import a
import strings

func halves(s string) string {
	match strings.Cut(s, ",") {
	case Some[strings.Halves]:
		"halves"
	case None:
		"none"
	}
}

func quad(n int) int { a.Quad(n) }

func top(n int) int { a.Top(n) }

func say(s string) { a.Say(s) }

func main() needs Console {
	Console.Println(halves("x,y") + " " + halves("xy"))
	Console.Println("\(quad(5)) \(top(4)) \(top(-1))")
	say("said")
}
`
)

// TestPurityAcrossPackages (batch-9 C3): a function is pure by its own
// package's facts wherever it is declared. A caller of a std function with
// an Ergo body (strings.Cut) and of a pure function of an imported user
// package lowers with no environment, and so does that callee; a caller of
// an impure one, at any depth of the import chain, keeps the environment.
// The program prints the same either way.
func TestPurityAcrossPackages(t *testing.T) {
	needsBuild(t)
	c := checkPackage(t, "c", []pkgSource{{"/ergo-test/c/c.ergo", purePkgC}}, nil)
	b := checkPackage(t, "b", []pkgSource{{"/ergo-test/b/b.ergo", purePkgB}}, pkgImporter{"c": c.Package})
	a := checkPackage(t, "a", []pkgSource{{"/ergo-test/a/a.ergo", purePkgA}}, pkgImporter{"b": b.Package, "c": c.Package})
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/purepkg/main.ergo", purePkgMain}},
		pkgImporter{"a": a.Package, "b": b.Package, "c": c.Package})
	main.Imports = []Unit{a, b, c}
	o := mustLowerUnit(t, "purepkg", main, false)
	code := o.mainGo()
	checkEnvParams(t, pureModel{
		name:   "purepkg",
		pure:   []string{"halves", "Cut", "quad", "Quad", "Twice", "Double"},
		impure: []string{"top", "Top", "Mid", "Leaf", "say", "Say", "main"},
	}, code)
	if t.Failed() {
		t.Logf("emitted Go:\n%s", code)
	}
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code2 := runExe(t, exes[o.name])
	want := strings.Join([]string{"halves none", "21 50 0", "said", ""}, "\n")
	if stdout != want || code2 != 0 {
		t.Fatalf("stdout %q, exit %d, want %q, 0\nstderr: %s", stdout, code2, want, stderr)
	}
}
