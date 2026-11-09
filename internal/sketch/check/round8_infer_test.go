package check

import (
	"strings"
	"testing"
)

// TestInferenceOrder: D6 rules 2 and 3 as amended in round 8, and 196u
// rule 6.
//   - A type parameter only literals bind takes their default, even when
//     the constraint of another parameter mentions it: there is no
//     inference through a constraint before the default, so G(1, s) binds
//     T to int and is the one S108 constraint error, where go/types infers
//     int8 from s's method (round-7 M2). Its lowering, G(int(1), s), is
//     invalid Go too, so the differential counts it as a 196u refusal.
//   - A function literal whose parameter and result types are all written
//     is a typed argument: typed arguments unify first, in source order,
//     then the literals default (round-7 M3). A failed call reports
//     one diagnostic and its result is invalid, so nothing cascades.
func TestInferenceOrder(t *testing.T) {
	const decls = `type S struct{}

func (S) M(int8) {}

type R struct{}

func (R) M(int) {}

type I[T any] interface{ M(T) }

type IM[T any] interface{ M(T) }

func G[T comparable, P IM[T]](v T, p P) {}

func GN[T comparable, P I[T]](v T, p P) {}

type Q struct{}

func (Q) Get() int8 { return 0 }

type IG[T any] interface{ Get() T }

func GR[T comparable, P IG[T]](v T, p P) {}

func Apply[T any](f func(T) T, x T) T { return f(x) }

func Rev[T any](x T, f func(T) T) T { return f(x) }
`
	const ergoDecls = `type S {}

func M(S, int8) {}

type R {}

func M(R, int) {}

type I[T] { M(T) }

type IM[T] { M(T) }

func G[T comparable, P IM[T]](v T, p P) {}

func GN[T comparable, P I[T]](v T, p P) {}

type Q {}

func Get(_ Q) int8 { return 0 }

type IG[T] { Get() T }

func GR[T comparable, P IG[T]](v T, p P) {}

func Apply[T](f func(T) T, x T) T { return f(x) }

func Rev[T](x T, f func(T) T) T { return f(x) }
`
	type verdict int
	const (
		valid verdict = iota
		invalid
		defaulted
	)
	cases := []struct {
		name, body string
		want       verdict
	}{
		{"constraint-constant", `func F(s S) { G(1, s) }`, defaulted},
		{"constraint-rune", `func F(r R) { G('a', r) }`, defaulted},
		{"constraint-named", `func F(s S) { GN(1, s) }`, defaulted},
		{"constraint-result", `func F(q Q) { GR(1, q) }`, defaulted},
		{"constraint-explicit", `func F(s S) { G[int8](1, s) }`, valid},
		{"constraint-typed", `func F(s S, v int8) { G(v, s) }`, valid},
		{"constraint-typed-mismatch", `func F(s S, v int) { G(v, s) }`, invalid},
		{"constraint-explicit-mismatch", `func F(s S) { G[int](1, s) }`, invalid},
		{"typed-literal-before-constant", `func F() int8 { return Apply(func(v int8) int8 { return v }, 1) }`, valid},
		{"typed-literal-after-constant", `func F() int8 { return Rev(1, func(v int8) int8 { return v }) }`, valid},
		{"typed-literal-explicit", `func F() int8 { return Apply[int8](func(v int8) int8 { return v }, 1) }`, valid},
		{"typed-literal-string", `func F() string { return Apply(func(v string) string { return v }, "a") }`, valid},
		{"typed-literal-mismatch", `func F() { _ = Apply(func(v int8) int8 { return v }, "a") }`, invalid},
		{"typed-literal-shape-mismatch", `func F() { _ = Apply(func(v int8) string { return "" }, 1) }`, invalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newTwin("package p\n\n"+decls+"\n"+tc.body+"\n", ergoDecls+"\n"+tc.body+"\n")
			g := goErrors(src.goSrc)
			if goValid := len(g) == 0; goValid != (tc.want != invalid) {
				t.Fatalf("go/types disagrees with the case: %q", g)
			}
			_, diags := CheckSource(src.ergoSrc)
			var ds []string
			for _, d := range diags {
				ds = append(ds, d.String())
			}
			switch tc.want {
			case valid:
				if len(ds) > 0 {
					t.Fatalf("want valid, got %q", ds)
				}
				if d := diffOne(src); len(d) > 0 {
					t.Fatalf("Info differs from go/types:\n%s", strings.Join(d, "\n"))
				}
			case invalid:
				if len(ds) != 1 || !strings.Contains(ds[0], "S108") {
					t.Fatalf("want one S108, got %q", ds)
				}
			case defaulted:
				if len(ds) != 1 || !strings.Contains(ds[0], "S108") || !strings.Contains(ds[0], "does not satisfy the constraint") {
					t.Fatalf("want exactly the constraint error, got %q", ds)
				}
				if d, refusal := diffCount(src, true); len(d) > 0 || !refusal {
					t.Fatalf("want a 196u refusal, got %q (refusal %v)", d, refusal)
				}
			}
		})
	}
}
