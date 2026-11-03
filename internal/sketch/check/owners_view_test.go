package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestFactOwnerViews reads the published ownership (owners.go): every node
// of a test is test-owned and every other node is the program's; a type
// that only a test's row names is an error type in the test view alone;
// one a production row names is one in every view.
func TestFactOwnerViews(t *testing.T) {
	src := "type Bang {}\n\ntype Oops {}\n\nfunc g() int fails Oops { return Oops }\n\nfunc main() {}\n" + ownerTests[2].src
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	named := map[string]*check.Named{}
	for _, tn := range p.Types {
		named[tn.Name] = tn.Type.(*check.Named)
	}
	for _, c := range []struct {
		typ        string
		owner      check.FactOwner
		want       bool
		ownerLabel string
	}{
		{"Bang", check.OwnerProduction, false, "production"},
		{"Bang", check.OwnerTest, true, "test"},
		{"Oops", check.OwnerProduction, true, "production"},
		{"Oops", check.OwnerTest, true, "test"},
	} {
		if got := p.IsErrorType(named[c.typ], c.owner); got != c.want {
			t.Errorf("IsErrorType(%s, %s) = %v, want %v", c.typ, c.ownerLabel, got, c.want)
		}
	}
	var inTest, outside int
	for _, d := range f.Decls {
		_, isTest := d.(*syntax.TestDecl)
		syntax.Inspect(d, func(n syntax.Node) bool {
			want := check.OwnerProduction
			if isTest {
				want = check.OwnerTest
				inTest++
			} else {
				outside++
			}
			if got := p.OwnerOf(n); got != want {
				t.Errorf("OwnerOf(%T at %d) = %d, want %d", n, n.Pos(), got, want)
			}
			return true
		})
	}
	if inTest == 0 || outside == 0 {
		t.Fatalf("the program has %d test nodes and %d others", inTest, outside)
	}
}
