package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A value of an error type that ends a function is a failure (rows.md,
// Tail results 3), unless the declared result admits it. Since FK3-g1 a
// value of an error type that is a variant of the function's one result
// (a union with a variant that is no error type) is that result's value
// when the function's written row does not declare it; lowering's
// TestErrorValueRuleTable runs the rule (its variant targets). The rows
// left here are the quiet ones: declared-is-quiet (a written row that
// lists the type chose the failure, so a caller handles it), and
// error-union-stays (since fs1, q27, a result that is a union of error
// types makes the value the success value).
func TestAmbiguousTailFailure(t *testing.T) {
	const prelude = `type WrongCode {}
type Other {}

type Signal = WrongCode | Ok{}

type Errs = WrongCode | Other

func check(n int) int fails WrongCode | Other {
	if n == 0 {
		return WrongCode
	}
	n
}
`
	cases := []struct{ name, src, text string }{
		{"declared-is-quiet", prelude + `
func fail() Signal fails WrongCode { WrongCode }
`, ""},
		{"error-union-stays", prelude + `
func errs() Errs { WrongCode }

func use() int {
	let x = errs()
	_ = x
	1
}
`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			want, have := expected(tc.src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Fatalf("want %v, got %v\n\t%s\n%s", want, have, describe(diags), tc.src)
			}
			if tc.text != "" && !strings.Contains(describe(diags), tc.text) {
				t.Fatalf("want a message with %q, got %s", tc.text, describe(diags))
			}
		})
	}
}
