package std_test

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// pkgImporter serves the packages of one test by path, and std.
type pkgImporter map[string]*check.Package

func (m pkgImporter) Import(path string) (*check.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

// TestStdHostHandOutsAcrossPackages closes fu-ambient-ctor's first known
// gap (r4): a record a host provider needs may be declared in another
// package, which hands it out. The guard asks the exported names of every
// package of its surface (TestStdHostProvidersHaveAuthority passes every
// std package), so capkg.NewCap with no row, which mentions capkg.Cap, is
// reported for fixture.Host; with only fixture as the surface it is not
// (the gap before r4). Another package cannot hand out what it got under
// Ambient without listing Ambient (S102 at the call), and a generic
// function cannot make a value of its type parameter (no zero value, no
// literal), so the exported names of the surface are every way in.
func TestStdHostHandOutsAcrossPackages(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	checkPkg := func(path, src string, imp pkgImporter) (*check.Package, []check.Diagnostic) {
		t.Helper()
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("%s: %v\n%s", path, err, src)
		}
		return check.Check(path, []*syntax.File{f}, imp)
	}
	const fixture = `import capkg

type Host { C capkg.Cap }

func NewHost() Host needs Ambient { NewHost() }

type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
	// The laundering probe across packages: capkg hands out a Tok, and
	// fixture.Wrap builds a Host from one.
	const wrapFixture = `import capkg

type Host { root string }

func Wrap(t capkg.Tok) Host { Wrap(t) }

func NewHost() Host needs Ambient { NewHost() }

type Gone { Path string }

func Read(_ Host) string fails Gone { try read() }

func read() string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`
	const reported = "capkg.NewCap mentions capkg.Cap (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"
	// Wrap mentions Host and takes no Host, so the r5 rule reports it
	// whatever capkg does with Tok, with either surface.
	wrapped := []string{"fixture.Wrap mentions fixture.Host (fixture.Host or a record it needs) and neither lists Ambient in its written row nor takes a parameter of one"}
	for _, row := range []struct {
		name, capSrc, fixture string
		alone, all            []string // the violations with surface fixture, and capkg too
	}{
		{"Cap handed out with no row", "type Cap { root string }\n\nfunc NewCap() Cap { NewCap() }\n", fixture, nil, []string{reported}},
		{"Cap handed out under Ambient", "type Cap { root string }\n\nfunc NewCap() Cap needs Ambient { NewCap() }\n", fixture, nil, nil},
		// fu-ambient-ctor r6: the mint rule asks capkg's builds of Cap too.
		{"Cap built with no row", "type Cap { root string }\n\nfunc NewCap() Cap { Cap{root: \"/\"} }\n", fixture, nil, []string{reported, "capkg.NewCap builds capkg.Cap (a literal) and neither lists Ambient in its written row nor takes a parameter of a held type"}},
		{"Cap built under Ambient", "type Cap { root string }\n\nfunc NewCap() Cap needs Ambient { Cap{root: \"/\"} }\n", fixture, nil, nil},
		{"Tok handed out with no row, Host built from it", "type Tok { v int }\n\nfunc MakeTok() Tok { MakeTok() }\n", wrapFixture, wrapped, wrapped},
		{"Tok handed out under Ambient, Host built from it", "type Tok { v int }\n\nfunc MakeTok() Tok needs Ambient { MakeTok() }\n", wrapFixture, wrapped, wrapped},
	} {
		capkg, ds := checkPkg("capkg", row.capSrc, nil)
		if len(ds) != 0 {
			t.Fatalf("%s: capkg: %v", row.name, ds)
		}
		fake, ds := checkPkg("fixture", row.fixture, pkgImporter{"capkg": capkg})
		for _, d := range ds {
			if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
				t.Fatalf("%s: fixture: %v", row.name, ds)
			}
		}
		hosts := hostProviderTypes(fake)
		if len(hosts) != 1 || hosts[0].Name != "Host" {
			t.Fatalf("%s: host providers %v", row.name, hosts)
		}
		if got := hostAuthorityViolations(fake, hosts[0], outside); !slices.Equal(got, row.alone) {
			t.Errorf("%s, surface fixture: the violations are %q, want %q", row.name, got, row.alone)
		}
		if got := hostAuthorityViolationsIn([]*check.Package{capkg, fake}, fake, hosts[0], outside); !slices.Equal(got, row.all) {
			t.Errorf("%s, surface capkg and fixture: the violations are %q, want %q", row.name, got, row.all)
		}
	}

	// What the guard relies on: another package that gets a Cap under
	// Ambient lists Ambient to hand it on, and a generic function makes no
	// value of its type parameter.
	capkg, ds := checkPkg("capkg", "type Cap { root string }\n\nfunc NewCap() Cap needs Ambient { NewCap() }\n", nil)
	if len(ds) != 0 {
		t.Fatalf("capkg: %v", ds)
	}
	for _, row := range []struct{ name, src, want string }{
		{"function", "import capkg\n\nfunc Get() capkg.Cap { capkg.NewCap() }\n", "this call requires Ambient"},
		{"let", "import capkg\n\nlet C = capkg.NewCap()\n", "this call requires Ambient"},
		{"callback", "import capkg\n\nfunc Get(f func(c capkg.Cap)) { f(capkg.NewCap()) }\n", "this call requires Ambient"},
		{"zero value of a type parameter", "func Make[T]() T { let x T\n x }\n", "T has no zero value"},
		{"literal of a type parameter", "func Make[T]() T { T{} }\n", "cannot write a composite literal of T"},
	} {
		_, ds := checkPkg("other", row.src, pkgImporter{"capkg": capkg})
		if len(ds) == 0 || !strings.Contains(ds[0].Message, row.want) {
			t.Errorf("%s: diagnostics %v, want one saying %q", row.name, ds, row.want)
		}
	}
}
