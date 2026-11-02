package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// stageE1Case is one program and the diagnostics it gets, "CODE:line".
type stageE1Case struct {
	name, src string
	want      []string
}

func runStageE1(t *testing.T, cases []stageE1Case) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s:%d", d.Code, d.Line))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("got %v, want %v\n%v", got, tc.want, diags)
			}
		})
	}
}

const untypedDecls = `func Each[T](xs []T, f func(T) int) int { f(xs[0]) }
func MapOf[T, U](xs []T, f func(T) U) []U { []U{f(xs[0])} }
func Fold[T, A](xs []T, start A, f func(A, T) A) A { f(start, xs[0]) }
func apply(f func(int) int) int { f(1) }
func Pick[T](f func(T) int) int { 0 }
`

// TestUntypedLiteralParameters: an untyped function-literal parameter
// takes its type from the expected function type once the other arguments
// and the literals' defaults fix it; with none it is S108 "cannot infer the
// type of x", once, with no cascade (D-E1, #214, ledger row C6).
func TestUntypedLiteralParameters(t *testing.T) {
	runStageE1(t, []stageE1Case{
		{"generic-argument", untypedDecls + "func F(xs []int) int { Each(xs, func(x) { x * 2 }) }\n", nil},
		{"after-literal-default", untypedDecls + "func F(xs []int) int { Fold(xs, 0, func(acc, x) { acc + x }) }\n", nil},
		{"explicit-instance", untypedDecls + "func F(xs []int) int { Each[int](xs, func(x) { x * 2 }) }\n", nil},
		{"plain-parameter", untypedDecls + "func F() int { apply(func(x) { x + 1 }) }\n", nil},
		{"let-type", untypedDecls + "func F() int {\n\tlet f func(int) int = func(x) { x + 1 }\n\tf(2)\n}\n", nil},
		{"result-from-tail", untypedDecls + "func F(xs []int) []string { MapOf(xs, func(x) { \"s\" }) }\n", nil},
		{"nested", untypedDecls + "func F(xs []int) int { Each(xs, func(x) { Each(xs, func(y) { x + y }) }) }\n", nil},
		{"bound-type-checks-body", untypedDecls + "func F(xs []string) int { Each(xs, func(x) { x * 2 }) }\n", []string{"S108:6"}},
		{"no-expected-type", "func F() int {\n\tlet double = func(x) { x * 2 }\n\tdouble(2)\n}\n", []string{"S108:2"}},
		{"only-the-literal-binds", untypedDecls + "func F() int { Pick(func(x) { 1 }) }\n", []string{"S108:6"}},
		{"extra-parameter", untypedDecls + "func F() int { apply(func(x, y) { x }) }\n", []string{"S108:6"}},
		{"one-per-literal", "func F() {\n\tlet g = func(a, b) { a }\n\t_ = g\n}\n", []string{"S108:2"}},
	})
}
