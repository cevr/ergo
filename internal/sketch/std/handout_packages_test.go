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
// std package), so capkg.NewCap with no row is reported for fixture.Host;
// with only fixture as the surface it is not (the gap before r4). Another
// package cannot hand out what it got under Ambient without listing
// Ambient (S102 at the call), and a generic function cannot make a value of
// its type parameter (no zero value, no literal), so the exported names of
// the surface are every way in.
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
	const reported = "capkg.NewCap hands out capkg.Cap, which fixture.Host needs, and does not list Ambient in its written row or take a value that needs authority"
	for _, row := range []struct {
		name, capSrc string
		alone, all   []string // the violations with surface fixture, and capkg too
	}{
		{"Cap handed out with no row", "type Cap { root string }\n\nfunc NewCap() Cap { NewCap() }\n", nil, []string{reported}},
		{"Cap handed out under Ambient", "type Cap { root string }\n\nfunc NewCap() Cap needs Ambient { NewCap() }\n", nil, nil},
	} {
		capkg, ds := checkPkg("capkg", row.capSrc, nil)
		if len(ds) != 0 {
			t.Fatalf("%s: capkg: %v", row.name, ds)
		}
		fake, ds := checkPkg("fixture", fixture, pkgImporter{"capkg": capkg})
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
