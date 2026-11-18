package check_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ctrGo embeds Go's atomic.Int64 twice: Counter's Add is the one Go
// promotes from the field; Own declares its own Add, which hides it.
const ctrGo = `package ctr

import "sync/atomic"

type Counter struct{ atomic.Int64 }

type Own struct{ atomic.Int64 }

func (o *Own) Add(d int64) int64 { return d }
`

// TestPromotedMethodTakesItsRow is GB's acceptance: a method Go promotes
// from an embedded field is the field's method, so ergo bind go binds
// Counter.Add with atomic.Int64.Add's memory-only row (`// ergo:effect
// none from sync/atomic.Int64.Add`), and a user's function calls it with
// no capability row; Own.Add, which hides the promoted one, stays
// foreign. A binding edited to borrow the row of a method of another name
// is S108 at the binding. Red at base: Counter.Add needs go.Foreign.
//
// Mutants it alone catches (check and gobind ledgers): gb-promoted-own-row
// (the binding looks up the bound type's row), gb-from-name-unchecked
// (the checker takes a from row of any name).
func TestPromotedMethodTakesItsRow(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "ctr.go", ctrGo, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{IgnoreFuncBodies: true, Importer: importer.ForCompiler(fs, "source", nil)}
	pkg, err := conf.Check("example.test/ctr", fs, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := gobind.File([]*types.Package{pkg}, []gobind.Platform{{GOOS: "linux", GOARCH: "amd64"}}, "go1.27")
	if err != nil {
		t.Fatal(err)
	}
	from := "// ergo:effect none from sync/atomic.Int64.Add (vouched: "
	if strings.Count(binding, from) != 1 {
		t.Fatalf("the binding has %d lines %q, want 1 (Counter.Add):\n%s", strings.Count(binding, from), from, binding)
	}
	use := func(binding, src string) []string {
		t.Helper()
		set := check.NewGoBindings(func(path string) (string, bool) {
			if path == "example.test/ctr" {
				return binding, true
			}
			return gostd.Binding(path)
		})
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("example.test/use", []*syntax.File{file}, drvImporter{set})
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+d.Message)
		}
		return got
	}
	call := func(typ string) string {
		return "import go example.test/ctr\n\nfunc Bump(c go.Ref[ctr." + typ + "]) int64 {\n\tc.Add(1)\n}\n"
	}
	if got := use(binding, call("Counter")); len(got) != 0 {
		t.Errorf("Counter.Add, promoted from atomic.Int64: %q; want clean", got)
	}
	if got := use(binding, call("Own")); len(got) != 1 || !strings.HasPrefix(got[0], "S102 ") || !strings.Contains(got[0], "go.Foreign") {
		t.Errorf("Own.Add, its own method: %q; want one S102 naming go.Foreign", got)
	}
	forged := strings.Replace(binding, "from sync/atomic.Int64.Add", "from sync/atomic.Int64.Load", 1)
	if got := strings.Join(use(forged, call("Counter")), "\n"); !strings.Contains(got, "names sync/atomic.Int64.Load, which is not a method of the same name") {
		t.Errorf("a from line naming Load above Add: %q; want the binding refused", got)
	}
}
