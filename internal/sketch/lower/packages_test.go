package lower

import (
	"errors"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A program of several packages and files (196b, plan U1 pkg-lower). The
// checker takes several files per package and imported user packages
// through its Importer; the lowering lowers every file of the root
// package, in file order, and every imported user package the program
// reaches, into one Go package (decision Q5): the imported packages' lets
// run first, in import dependency order, then the root's.

// pkgSource is one admitted file of a test package.
type pkgSource struct{ path, text string }

// pkgImporter serves the test packages by path, and std otherwise.
type pkgImporter map[string]*check.Package

func (i pkgImporter) Import(path string) (*check.Package, error) {
	if p, ok := i[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

// checkPackage parses and checks the files of package path against the
// packages imp serves, and returns its unit.
func checkPackage(t *testing.T, path string, srcs []pkgSource, imp pkgImporter) Unit {
	t.Helper()
	u := Unit{}
	for _, s := range srcs {
		f, err := syntax.Parse(s.text)
		if err != nil {
			t.Fatalf("%s: %v", s.path, err)
		}
		f.Name = s.path // a driver that reads files names them
		u.Files = append(u.Files, f)
		u.Sources = append(u.Sources, Source{Path: s.path, Text: s.text})
	}
	pkg, ds := check.Check(path, u.Files, imp)
	if len(ds) != 0 {
		t.Fatalf("package %s: checker diagnostics %v", path, ds)
	}
	u.Package = pkg
	return u
}

// libraryUnit is package lib of TestLowerPackages, over two files: Limit's
// initializer calls an unexported function that reads Base, which the
// second file declares; Make reads Limit; Twice is a 196m function, a Go
// method on Q; First is generic. Its test fails if it runs.
func libraryUnit(t *testing.T) Unit {
	return checkPackage(t, "lib", []pkgSource{
		{"/ergo-test/lib/a.ergo", `type Q { N int }

func compute() int { Base * 2 }

let Limit int = compute()

func Make(n int) Q { Q{N: n + Limit} }

func Twice(q Q) int { q.N * 2 }

func First[T](xs []T, d T) T {
	if len(xs) == 0 {
		return d
	}
	return xs[0]
}

test LibOnly {
	assert Limit == 0
}
`},
		{"/ergo-test/lib/b.ergo", "let Base int = 3\n"},
	}, nil)
}

// lowerUnit lowers u, its test build when tests is set, as the program
// name.
func lowerUnit(name string, u Unit, tests bool) *lowered {
	out := &lowered{name: name, pkg: u.Package}
	for _, s := range u.Sources {
		out.src += "// " + s.Path + "\n" + s.Text
	}
	func() {
		defer func() { out.panicked = recover() }()
		lowering := Lower
		if tests {
			lowering = LowerTests
		}
		files, err := lowering(u)
		var refused *Refused
		switch {
		case errors.As(err, &refused):
			out.refused = refused.Refusals
		case err != nil:
			out.err = err
		default:
			out.files = files
		}
	}()
	return out
}

func mustLowerUnit(t *testing.T, name string, u Unit, tests bool) *lowered {
	t.Helper()
	o := lowerUnit(name, u, tests)
	switch {
	case o.err != nil:
		t.Fatalf("%s: %v\n%s", name, o.err, o.src)
	case o.panicked != nil:
		t.Fatalf("%s: lowering panicked: %v\n%s", name, o.panicked, o.src)
	case len(o.refused) > 0:
		t.Fatalf("%s: refused %+v\n%s", name, o.refused, o.src)
	}
	return o
}

// TestLowerPackages builds Go because it checks what the built program
// prints and how it exits; no in-process check observes that, and Ergo has
// no reference interpreter yet (test-speed T8).
func TestLowerPackages(t *testing.T) {
	needsBuild(t)
	lib := libraryUnit(t)
	imp := pkgImporter{"lib": lib.Package}

	// Two files of one package: main in a.ergo calls fromB in b.ergo.
	twoFiles := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/two/a.ergo", "func main() needs Console {\n\tConsole.Println(fromB())\n}\n"},
		{"/ergo-test/two/b.ergo", "func fromB() string {\n\treturn \"b\"\n}\n"},
	}, nil)

	// An imported package's let, record, 196m function and generic
	// function. Doubled reads lib.Limit, so lib's initializers run first.
	imported := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/imp/main.ergo", `import lib

let Doubled int = lib.Limit + 1

func main() needs Console {
	Console.Println("\(lib.Limit) \(Doubled) \(lib.Make(1).Twice()) \(lib.First([]string{"x"}, "y")) \(lib.First([]int{}, 4))")
}
`},
	}, imp)
	imported.Imports = []Unit{lib}

	// lib and main both declare Q, Make and a let Limit: each is its own.
	sameNames := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/same/main.ergo", `import lib

type Q { S string }

let Limit string = "main"

func Make(s string) Q { Q{S: s} }

func main() needs Console {
	let q = Make("m")
	let p = lib.Make(2)
	Console.Println("\(q.S) \(p.N) \(Limit) \(lib.Limit)")
}
`},
	}, imp)
	sameNames.Imports = []Unit{lib}

	// The test build runs the root package's tests only, as go test runs
	// one package's: lib's failing test is not part of it.
	rootTests := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/rt/a.ergo", "import lib\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(lib.Limit)\")\n}\n"},
		{"/ergo-test/rt/b.ergo", "import lib\n\ntest Root {\n\tassert lib.Limit == 6\n}\n"},
	}, imp)
	rootTests.Imports = []Unit{lib}

	// f's T and g's T are at one offset of two files: each is its own type
	// parameter, so Box[T]'s default function is not shared (a generated
	// name keyed by a position alone would give g f's comparable T).
	paramKeys := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/keys/a.ergo", `func f[T comparable](x T) int {
	let b Box[T]
	let (_, m) = b
	len(m)
}

type Box[T] (int, Map[string, T])

func main() needs Console {
	Console.Println("\(f(1)) \(g([]int{}))")
}
`},
		{"/ergo-test/keys/b.ergo", `func g[T](x T) int {
	let b Box[T]
	let (_, m) = b
	len(m) + 1
}
`},
	}, nil)

	programs := []*lowered{
		mustLowerUnit(t, "pkg_param_keys", paramKeys, false),
		mustLowerUnit(t, "pkg_two_files", twoFiles, false),
		mustLowerUnit(t, "pkg_imported", imported, false),
		mustLowerUnit(t, "pkg_same_names", sameNames, false),
		mustLowerUnit(t, "pkg_root_tests", rootTests, true),
	}
	want := map[string]string{
		"pkg_param_keys": "0 1\n",
		"pkg_two_files":  "b\n",
		"pkg_imported":   "6 7 14 x 4\n",
		"pkg_same_names": "m 8 main 6\n",
		"pkg_root_tests": "ok Root\nPASS (1 tests)\n",
	}

	// Each file's declarations name their own file in //line directives
	// and in the declaration table, and so do an imported package's.
	anchors := map[string][]string{
		"pkg_two_files": {
			"//line /ergo-test/two/a.ergo:1:1", "//line /ergo-test/two/b.ergo:1:1",
			`{Name: "fromB", File: "/ergo-test/two/b.ergo", Line: 1, End: 3}`,
			`{Name: "main", File: "/ergo-test/two/a.ergo", Line: 1, End: 3}`,
		},
		"pkg_imported": {"//line /ergo-test/lib/a.ergo:", "//line /ergo-test/lib/b.ergo:1:5"},
	}
	for _, p := range programs {
		for _, s := range anchors[p.name] {
			if !strings.Contains(p.mainGo(), s) {
				t.Errorf("%s: main.go lacks %q:\n%s", p.name, s, p.mainGo())
			}
		}
	}

	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the lowered programs do not build")
	}
	for _, p := range programs {
		stdout, stderr, code := runExe(t, exes[p.name])
		if stdout != want[p.name] || code != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q; stderr:\n%s", p.name, code, stdout, want[p.name], stderr)
		}
	}

	// A refusal in the second file is located in that file.
	refusedTwo := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/ref/a.ergo", "func main() needs Console {\n\tConsole.Println(\"\\(len(xs()))\")\n}\n"},
		{"/ergo-test/ref/b.ergo", "\n\nfunc xs() []int {\n\t[]int{2: 5}\n}\n"},
	}, nil)
	o := lowerUnit("pkg_refused", refusedTwo, false)
	if o.err != nil || o.panicked != nil || len(o.refused) != 1 {
		t.Fatalf("pkg_refused: want one refusal, got err %v, panic %v, refusals %+v", o.err, o.panicked, o.refused)
	}
	// The refusal is at the element's key, 2.
	if r := o.refused[0]; r.Source != 1 || r.Path != "/ergo-test/ref/b.ergo" || r.Line != 4 || r.Column != 8 || r.Offset != 27 {
		t.Errorf("pkg_refused: refusal %+v, want source 1 (b.ergo) at 4:8, offset 27", r)
	}

	// A program that reaches a user package whose unit it does not hold is
	// an error, not a lowering without that package's lets.
	missing := imported
	missing.Imports = nil
	if o := lowerUnit("pkg_missing", missing, false); o.err == nil || !strings.Contains(o.err.Error(), `package "lib"`) {
		t.Errorf("pkg_missing: want an error naming package lib, got err %v, refusals %+v", o.err, o.refused)
	}
}
