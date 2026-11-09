package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestArrayDiagnostics: the fixed arrays' diagnostics (196y; types.md,
// Arrays 1 to 4), each its text and line. A row's mutant: identity that
// ignores the length (array-len-ignored-in-identity), a constant index
// checked one past the end (array-const-index-off-by-one), a literal whose
// count goes unchecked (array-literal-count-unchecked), an empty literal
// that invents elements of a type with no zero value
// (array-empty-literal-invents), and a slice bound past the length
// (array-slice-bound-unchecked).
func TestArrayDiagnostics(t *testing.T) {
	type row struct {
		name, src string
		want      string // code:line message part; "" is clean
	}
	rows := []row{
		{"clean", "func f(a [3]int) [2]int {\n\tlet mut b [2]int\n\tb[1] = a[2] + len(a)\n\tb\n}\n", ""},
		{"length is the type", "func f(a [3]int) [4]int {\n\ta\n}\n", "S108:2 [3]int"},
		{"constant index at the length", "func f(a [3]int) int {\n\ta[3]\n}\n", "S108:2 index 3 is out of bounds of [3]int, whose length is 3"},
		{"literal lists all", "func f() [3]int {\n\t[3]int{1, 2}\n}\n", "S108:2 a literal of [3]int lists all 3 elements, found 2"},
		{"empty literal of a union", "type A {}\ntype B {}\ntype U = A | B\nfunc f() [2]U {\n\t[2]U{}\n}\n", "S108:5 a literal of [2]U lists its 2 elements: U has no zero value"},
		{"keyed literal", "func f() [2]int {\n\t[2]int{0: 1, 1: 2}\n}\n", "S108:2 an array literal lists its elements in order, with no index"},
		{"slice bound past the length", "func f(a [3]int) []int {\n\ta[1:4]\n}\n", "S108:2 the slice bound 4 is out of bounds of length 3"},
		{"length too large", "func f(a [4294967296]int) {}\n", "S108:1 the array length 4294967296 is larger than 2147483647"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			if r.want == "" {
				if len(d) != 0 {
					t.Fatalf("want clean: %s", describe(d))
				}
				return
			}
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
