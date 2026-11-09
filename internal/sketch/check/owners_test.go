package check_test

import (
	"fmt"
	"strings"
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

// errorMethodProductions declare a function Error on Bang: where only a
// test's row makes Bang an error type, the function is valid in the
// program; where a production row does, it is S108 at the function
// (types.md, Methods 6).
var errorMethodProductions = []struct {
	name, src  string
	productRow bool
}{
	{"value", "type Bang {}\n\nfunc Error(b Bang) string { \"bang\" }\n\nfunc main() {\n\tlet _ = Error(Bang)\n}\n", false},
	{"production-row", "type Bang {}\n\nfunc Error(b Bang) string { \"bang\" }\n\nfunc g() int fails Bang { return Bang }\n\nfunc main() {\n\tlet v = g() catch {\n\tcase Bang:\n\t\t0\n\t}\n\tlet _ = v\n}\n", true},
}

// TestTestErrorRowBlamesErrorMethod (E3a round 2, the M2 blame site): a
// function Error on a type that only a test's row makes an error type is
// reported once, S108 at the test's first row that names the type in source
// order, and the program keeps its own diagnostics; a production row keeps
// S108 at the function, and the tests add nothing.
func TestTestErrorRowBlamesErrorMethod(t *testing.T) {
	tests := append(append([]struct{ name, src string }{}, ownerTests...),
		struct{ name, src string }{"two-tests", ownerTests[1].src + "\ntest U {\n\tlet f = func() int fails Bang { return Bang }\n\tlet v = f() catch {\n\tcase Bang:\n\t\t3\n\t}\n\tassert v == 3\n}\n"})
	for _, p := range errorMethodProductions {
		for _, tc := range tests {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				src := p.src + tc.src
				full, blank := checkDiagList(t, src), checkDiagList(t, blankTestDecls(t, src))
				var prod, inTests []check.Diagnostic
				for _, d := range full {
					if d.Offset < len(p.src) {
						prod = append(prod, d)
					} else {
						inTests = append(inTests, d)
					}
				}
				if fmt.Sprint(prod) != fmt.Sprint(blank) {
					t.Errorf("the tests change the program's diagnostics:\nwith tests:    %s\nwithout tests: %s\n%s", prod, blank, src)
				}
				method := strings.Index(p.src, "func Error") + len("func ")
				var wantProd []int
				if p.productRow {
					wantProd = []int{method}
				}
				if got := s108Offsets(blank); fmt.Sprint(got) != fmt.Sprint(wantProd) {
					t.Errorf("program diagnostics %s: want S108 at offsets %v\n%s", blank, wantProd, src)
				}
				var wantTests []int
				if !p.productRow && tc.src != "" {
					wantTests = []int{len(p.src) + strings.Index(tc.src, "fails Bang") + len("fails ")}
				}
				if got := s108Offsets(inTests); fmt.Sprint(got) != fmt.Sprint(wantTests) || len(inTests) != len(wantTests) {
					t.Errorf("test diagnostics %s: want only S108 at offsets %v\n%s", inTests, wantTests, src)
				}
				for _, d := range inTests {
					if !strings.Contains(d.Message, "this row makes Bang an error type in the tests") {
						t.Errorf("the test's S108 does not name the row: %s", d)
					}
				}
			})
		}
	}
}

func checkDiagList(t *testing.T, src string) []check.Diagnostic {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	_, ds := check.Check("main", []*syntax.File{f}, nil)
	return ds
}

func s108Offsets(ds []check.Diagnostic) []int {
	var out []int
	for _, d := range ds {
		if d.Code == "S108" {
			out = append(out, d.Offset)
		}
	}
	return out
}
