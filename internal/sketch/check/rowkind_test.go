package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestRowKindThroughUse holds a type parameter that holds a failure only
// because a function of its package takes it as a Slot row (std ui's
// Async[T, F] and Await, ROW-PARAM option A, AC3 and AC5; slotbound.go):
// a non-error argument is S104 at the written argument, and a type written
// there is an error type of the writing package (owner-scoped: only in the
// tests when only a test writes it). Each row is the smallest program for
// one rule, with the one diagnostic it gets, or none. Mutants it alone
// catches (testdata/mutants.json): slot-bound-not-observed,
// row-arg-names-no-error-type, row-arg-owner-not-scoped,
// slot-arg-not-refused.
func TestRowKindThroughUse(t *testing.T) {
	parse := func(t *testing.T, src string) *syntax.File {
		t.Helper()
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, src)
		}
		return f
	}
	render := func(ds []check.Diagnostic) []string {
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		return got
	}
	const oops = "import ui\n\ntype Oops { code int }\n\n"
	const await = "func main() needs Console {\n\tlet a ui.Async[int, Oops] = ui.Failed{Error: Oops{code: 3}}\n\tlet v = ui.Await(a) catch {\n\tcase Oops as o: o.code\n\tcase ui.Pending: 0\n\t}\n\tConsole.Println(\"\\(v)\")\n}\n"
	for _, row := range []struct {
		name, src string
		want      string // "CODE line:column message prefix", or "" for none
	}{
		{"AC5 a type that is no error type is refused at the argument", "import ui\n\nfunc main() {\n\tlet a ui.Async[int, string] = ui.Loading\n\tlet _ = a\n}\n", "S104 4:22 "},
		{"AC3 a written row argument names an error type", oops + await, ""},
		// No catch arm names Oops here, so only the row argument makes it
		// an error type: a catch arm's type is one too (INFER-CATCH), which
		// hid mutant row-arg-names-no-error-type in the rows around it.
		{"AC3 a row argument no catch arm names makes an error type", oops + "func main() {\n\tlet a ui.Async[int, Oops] = ui.Loading\n\tlet _ = a\n}\n", ""},
		{"AC3 a row argument in a parameter's type names an error type", oops + "func show(a ui.Async[int, Oops]) int {\n\tui.Await(a) catch {\n\tcase Oops as o: o.code\n\tcase ui.Pending: 0\n\t}\n}\n\nfunc main() {}\n", ""},
	} {
		pkg, ds := check.Check("main", []*syntax.File{parse(t, row.src)}, check.Std)
		got := render(ds)
		switch {
		case row.want == "" && len(got) > 0:
			t.Errorf("%s: want no diagnostic, got %q\n%s", row.name, got, row.src)
		case row.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], row.want)):
			t.Errorf("%s: want one diagnostic %q…, got %q\n%s", row.name, row.want, got, row.src)
		}
		if row.want == "" && pkg != nil {
			if n := named(pkg, "Oops"); n != nil && !pkg.IsErrorType(n, check.OwnerProduction) {
				t.Errorf("%s: Oops is no error type of the run build", row.name)
			}
		}
	}
	// AC3b: written only in a test, Oops is an error type of the tests and
	// a value type of the run build (owners.go).
	src := oops + "func main() {}\n\ntest Awaits() {\n\tlet a ui.Async[int, Oops] = ui.Failed{Error: Oops{code: 3}}\n\tlet v = ui.Await(a) catch {\n\tcase Oops as o: o.code\n\tcase ui.Pending: 0\n\t}\n\tassert v == 3\n}\n"
	tpkg, ds := check.Check("main", []*syntax.File{parse(t, src)}, check.Std)
	if len(ds) > 0 {
		t.Fatalf("AC3b: %q\n%s", render(ds), src)
	}
	n := named(tpkg, "Oops")
	if n == nil {
		t.Fatal("AC3b: no Oops")
	}
	if tpkg.IsErrorType(n, check.OwnerProduction) {
		t.Errorf("AC3b: Oops, written as a row argument only in a test, is an error type of the run build")
	}
	if !tpkg.IsErrorType(n, check.OwnerTest) {
		t.Errorf("AC3b: Oops, written as a row argument in a test, is no error type of the tests")
	}
}

// named is the package's named type called name, or nil.
func named(p *check.Package, name string) *check.Named {
	for _, tn := range p.Types {
		if tn.Name == name {
			if n, ok := tn.Type.(*check.Named); ok {
				return n
			}
		}
	}
	return nil
}
