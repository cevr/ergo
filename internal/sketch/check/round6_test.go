package check

import (
	"testing"
)

// TestInferenceUnions: Ergo's departure from Go's unifier. A variant is
// assignable to its union (types.md, Unions), so a named union parameter
// unifies with the variant its argument names, and the variant's type
// arguments bind; a conflicting later argument is S108.
func TestInferenceUnions(t *testing.T) {
	const decls = `type Has[T] { V T }

type Empty {}

type Opt[T] = Has[T] | Empty

func Get[T](o Opt[T], d T) T { return d }

func Pair[T](o Opt[T], p Opt[T]) {}
`
	cases := []struct {
		name, body string
		accept     bool
	}{
		{"variant-binds", `func G() int { return Get(Has[int]{V: 1}, 2) }`, true},
		{"plain-variant", `func G() int { return Get(Empty, 2) }`, true},
		{"variant-then-conflict", `func G(s string) string { return Get(Has[int]{V: 1}, s) }`, false},
		{"two-variants-agree", `func G() { Pair(Has[int]{V: 1}, Has[int]{V: 2}) }`, true},
		{"two-variants-conflict", `func G() { Pair(Has[int]{V: 1}, Has[string]{V: "a"}) }`, false},
		{"not-a-variant", `func G() int { return Get(Has2, 2) }
type Has2 {}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + "\n" + tc.body + "\n"
			_, diags := CheckSource(src)
			var ds []string
			for _, d := range diags {
				ds = append(ds, d.String())
			}
			if (len(ds) == 0) != tc.accept {
				t.Fatalf("accept = %v, want %v: %q", len(ds) == 0, tc.accept, ds)
			}
		})
	}
}
