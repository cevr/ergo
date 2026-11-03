package check_test

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Fact ownership (owners.go; stage E3a round 2 M2): a struct type that only
// a test's error row names is an error type in the tests alone, so a test
// never changes the verdict on the rest of the program. The property: for
// every production fragment × every test that names the fragment's type in
// an error row, the program's diagnostics are the diagnostics of the same
// source with its tests blanked out, and a valid test adds none.

// ownerProductions use the struct type Bang in the ways an error type and
// a value type differ: as a returned value, as an argument, returned from
// a function of another result, behind a catch that needs a failure, and
// named in a production row (the control, where Bang is an error type
// everywhere).
var ownerProductions = []struct{ name, src string }{
	{"value", "type Bang {}\n\nfunc g() Bang { Bang }\n\nfunc main() {\n\tlet _ = g()\n}\n"},
	{"argument", "type Bang {}\n\nfunc h(b Bang) int { 1 }\n\nfunc main() {\n\tlet _ = h(Bang)\n}\n"},
	{"returned-from-int", "type Bang {}\n\nfunc g(b bool) int {\n\tif b {\n\t\treturn Bang\n\t}\n\t1\n}\n\nfunc main() {}\n"},
	{"caught", "type Bang {}\n\nfunc g(b bool) int {\n\tif b {\n\t\treturn Bang\n\t}\n\t1\n}\n\nfunc main() {\n\tlet v = g(true) catch {\n\tcase Bang:\n\t\t0\n\t}\n\tlet _ = v\n}\n"},
	{"production-row", "type Bang {}\n\nfunc g() int fails Bang { return Bang }\n\nfunc main() {\n\tlet v = g() catch {\n\tcase Bang:\n\t\t0\n\t}\n\tlet _ = v\n}\n"},
}

// ownerTests name Bang in an error row: of a literal in a test, of a
// literal nested in a literal in a test, and of a literal in a property.
var ownerTests = []struct{ name, src string }{
	{"none", ""},
	{"literal", "\ntest T {\n\tlet f = func() int fails Bang { return Bang }\n\tlet v = f() catch {\n\tcase Bang:\n\t\t1\n\t}\n\tassert v == 1\n}\n"},
	{"nested-literal", "\ntest T {\n\tlet f = func() int {\n\t\tlet g = func() int fails Bang { return Bang }\n\t\tg() catch {\n\t\tcase Bang:\n\t\t\t2\n\t\t}\n\t}\n\tassert f() == 2\n}\n"},
	{"property", "\ntest P(n int) {\n\tlet f = func() int fails Bang {\n\t\tif n > 0 {\n\t\t\treturn Bang\n\t\t}\n\t\tn\n\t}\n\tlet v = f() catch {\n\tcase Bang:\n\t\t0\n\t}\n\tassert v <= 0\n}\n"},
}

// blankTestDecls replaces every test declaration of src with spaces,
// keeping its line feeds, so every other position stays where it was.
func blankTestDecls(t *testing.T, src string) string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	b := []byte(src)
	for _, d := range f.Decls {
		if td, ok := d.(*syntax.TestDecl); ok {
			for i := int(td.Pos()); i < int(td.End()); i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return string(b)
}

func checkDiags(t *testing.T, src string) string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	_, ds := check.Check("main", []*syntax.File{f}, nil)
	return fmt.Sprint(ds)
}

func TestTestErrorRowsStayInTests(t *testing.T) {
	for _, p := range ownerProductions {
		for _, tc := range ownerTests {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				src := p.src + tc.src
				full, blank := checkDiags(t, src), checkDiags(t, blankTestDecls(t, src))
				if full != blank {
					t.Errorf("the tests change the verdict on the program:\nwith tests:    %s\nwithout tests: %s\n%s", full, blank, src)
				}
			})
		}
	}
}
