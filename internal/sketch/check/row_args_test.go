package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A row of a row parameter is written as needs and fails write a row,
// `A | B`, in a type's type arguments and in an expression's index alike
// (types.md, Generics 7; lane MB F8). Each fixture kills a mutant no other
// test catches: the parser reads only the first element of a type
// argument (row-in-type), the checker keeps only the first element of a
// row argument (row-keeps-every-element), the checker refuses the
// expression index's row (row-in-index), a capability row where a type
// is expected, which has no value type (row-for-a-type; owner q47), and the parenthesised row's message, as
// needs (A | B) has one (row-in-parentheses).
func TestRowArgFixtures(t *testing.T) {
	const prelude = `type A { Ping() int }
type B { Pong() int }

type Job[R] { Run func() int needs R }

func run[R](j Job[R]) int needs R { j.Run() }

func sum() int needs A | B { A.Ping() + B.Pong() }
`
	cases := []struct{ name, src, text string }{
		{"row-in-type", prelude + `
func held() Option[Job[A | B]] { Some{Job[A | B]{Run: sum}} }
`, ""},
		{"row-keeps-every-element", prelude + `
func call(j Job[A | B]) int needs A { j.Run() } // want S102
`, "requires B,"},
		{"row-in-index", prelude + `
func call(j Job[A | B]) int needs A | B { run[A | B](j) }
`, ""},
		{"row-for-a-type", prelude + `
func f(o Option[A | B]) {} // want S104
`, "A is not an error type"},
		{"row-in-parentheses", prelude + `
func f(j Job[(A | B)]) {} // want S001
`, "a row takes no parentheses"},
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
