package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestInferErrorRowNamesTheCause is INFER-IMPORT-ROW's repro: ui.Await's F
// is an error row parameter (`func Await[T, F](a Async[T, F]) T fails F |
// Pending`), so it binds only to an error type (types.md, Error types 1).
// A type that no error row of the caller's package names is no error type
// there, whichever package declares Await: the import is not the cause.
// The diagnostic names the cause, where it used to say only "does not
// match ui.Async[int, F]". The controls check clean: the type named in a
// row of the package, or the type arguments written (a row argument names
// the type in a row, Error types 4).
//
// Mutant: inf-row-not-error-unnamed (the bare mismatch message).
func TestInferErrorRowNamesTheCause(t *testing.T) {
	const caught = "\tlet x = ui.Await(a) catch {\n\tcase Oops:\n\t\t-1\n\tcase ui.Pending:\n\t\t-2\n\t}\n\tx\n}\n"
	refused := "import ui\n\ntype Oops { V int }\n\nfunc f(a ui.Async[int, Oops]) int {\n" + caught
	_, d := check.CheckSource(refused)
	want := "Oops binds an error row parameter, and no error row of this package names Oops, so it is no error type here"
	if len(d) != 1 || !strings.Contains(d[0].Message, want) {
		t.Errorf("want one diagnostic holding %q, got %v", want, d)
	}
	for _, ok := range []string{
		"import ui\n\ntype Oops { V int }\n\nfunc g() fails Oops { Oops{V: 1} }\n\nfunc f(a ui.Async[int, Oops]) int {\n" + caught,
		"import ui\n\ntype Oops { V int }\n\nfunc f(a ui.Async[int, Oops]) int {\n" + strings.Replace(caught, "ui.Await(a)", "ui.Await[int, Oops](a)", 1),
	} {
		if _, d := check.CheckSource(ok); len(d) > 0 {
			t.Errorf("want no diagnostic, got %v\n%s", d, ok)
		}
	}
}
