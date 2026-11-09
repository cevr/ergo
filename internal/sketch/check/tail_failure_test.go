package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A value of an error type that ends a function is a failure (rows.md,
// Tail results 3). When the function's one result is a union that has
// the error type as a variant, and its row does not declare it, the
// reading is ambiguous: S101 at the value says what to write for each
// reading, and the value is read on as the result, so no caller gets a
// failure (lane MB F3). Each row kills a mutant no other test catches:
// tail-reported (no report: the failure is inferred silently and the
// caller gets S105; a report that still escapes gives the caller S105
// too), return-reported (the return path), declared-is-quiet
// (a declared row that lists it is reported), error-union-stays (a union
// of error types only is reported, where the advice cannot work).
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
	const advice = "to return it as a value, write let v Signal = WrongCode and end with v"
	cases := []struct{ name, src, text string }{
		{"tail-reported", prelude + `
func pick(k int) Signal {
	match k {
	case 0:
		WrongCode // want S101
	case _:
		Ok
	}
}

func use() int {
	let s = pick(1)
	_ = s
	1
}
`, advice},
		{"return-reported", prelude + `
func early() Signal {
	return WrongCode // want S101
}
`, advice},
		{"declared-is-quiet", prelude + `
func fail() Signal fails WrongCode { WrongCode }
`, ""},
		{"error-union-stays", prelude + `
func errs() Errs { WrongCode }

func use() int {
	let x = errs() // want S105
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
