package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestIncludeRefusesNonRecords holds the refusals of types.md, Structs 7
// that the include-graph property (lower, TestIncludedRecordFlattens) does
// not generate: `..R` where R is not a record of this package with fields.
// Each is one S108 at the member's line, with its text; the record still
// checks with its own fields, so nothing cascades.
func TestIncludeRefusesNonRecords(t *testing.T) {
	rows := []struct {
		name, src, text string
	}{
		{"a union", `type A {}
type B { N int }
type U = A | B
type R { ..U, X int }
func f() R { R{X: 1} }
func main() {}
`, "U is a union"},
		{"an operation type", `type Log { Write(s string) }
type R { ..Log, X int }
func f() R { R{X: 1} }
func main() {}
`, "Log is an operation type"},
		{"a type parameter", `type R[T] { ..T, X int }
func f() R[int] { R[int]{X: 1} }
func main() {}
`, "T is not a record"},
		{"a basic type", `type R { ..int, X int }
func f() R { R{X: 1} }
func main() {}
`, "int is not a record"},
		{"a fieldless record", `type E {}
type R { ..E, X int }
func f() R { R{X: 1} }
func main() {}
`, "..E includes no field"},
	}
	for _, r := range rows {
		_, ds := check.CheckSource(r.src)
		if len(ds) != 1 || ds[0].Code != "S108" || !strings.Contains(ds[0].Message, r.text) {
			t.Errorf("%s: want one S108 at the member containing %q, got %v", r.name, r.text, ds)
			continue
		}
		if want := memberLine(r.src); ds[0].Line != want {
			t.Errorf("%s: the S108 is at line %d, want the member's line %d", r.name, ds[0].Line, want)
		}
	}

	lib := checkedLib(t)
	ds := outside(t, lib, `type R { ..lib.Open, X int }
func f() R { R{X: 1} }
func main() {}
`)
	if len(ds) != 1 || ds[0].Code != "S108" || ds[0].Line != 2 || !strings.Contains(ds[0].Message, "Open is declared in package lib: write a field") {
		t.Errorf("another package's record: want one S108 at line 2 naming the field fix, got %v", ds)
	}
}

// memberLine is the 1-based line of the first `..` in src.
func memberLine(src string) int {
	return strings.Count(src[:strings.Index(src, "..")], "\n") + 1
}
