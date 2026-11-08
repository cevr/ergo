package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A refused name still binds an invalid placeholder. Reading that name in
// the arm must not resolve the package object and add a second diagnostic.
func TestRefusedPatternNameBinds(t *testing.T) {
	for _, tc := range []struct{ name, decl string }{
		{"helper", `func helper() string { return "" }`},
		{"len", ""},
		{"timeout", `import timeout`},
		{"shared", `let shared = 1`},
		{"Finished", `type Finished { N int }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("%s\ntype R { Value string }\nfunc F(r R) string { match r { case R{Value: %s}: return %s } }\n", tc.decl, tc.name, tc.name)
			_, d := check.CheckSource(src)
			want := tc.name + " already names"
			switch tc.name {
			case "Finished":
				want = "Finished does not match"
			case "len":
				// D-D3 in every binder kind (stage D1 round 1 review m2).
				want = "len is a prelude name, which cannot be redeclared"
			}
			if len(d) != 1 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, want) {
				t.Fatalf("want one refused-name diagnostic, got %s", describe(d))
			}
		})
	}
}

// A let follows the binder rule (D-D2): package functions and values may
// be reused, and so may package types (types.md, Scope 1; L5); prelude
// names stay protected.
func TestSingleNameLetPattern(t *testing.T) {
	for _, tc := range []struct{ name, src, code string }{
		{"constant", `func F() { let true = false; _ = true }`, check.CodeType},
		{"variant", `type State = Pending{} | Finished{}
func F(s State) { let Pending = s; _ = Pending }`, ""},
		{"own-record", `type R { N int }
func F(r R) { let R = r; _ = R }`, ""},
		{"type-name-unread", `type R { N int }
func F(r R) { let R = r }`, check.CodeUnused},
		{"function", `func helper() int { return 1 }
func F() { let helper = 1; _ = helper }`, ""},
		{"package-value", `let shared = 1
func F() { let shared = 1; _ = shared }`, ""},
		{"builtin", `func F() { let len = 1; _ = len }`, check.CodeType},
		{"package", `import timeout
func G() fails timeout.Expired {}
func F() { let timeout = 1; _ = timeout }`, ""},
		{"nested-function-binding", `type R { N int }
func helper() int { return 1 }
func F(r R) int { let R{N: helper} = r; return helper }`, ""},
		{"prelude-builtin", `func F() { let len = 1; _ = len }`, check.CodeType},
		{"prelude-operation", `func F() { let Sleep = 1; _ = Sleep }`, ""},
		{"explicit-function-binding", `func helper() int { return 1 }
func F() { let _ as helper = func() {}; helper() }`, ""},
		{"explicit-type-binding", `type R { N int }
func F() int { let _ as R = 1; return R }`, ""},
		{"binding", `func F() int { let n = 1; return n }`, ""},
		{"local-shadowing", `func F() int { let n = 1; if true { let n = 2; return n }; return n }`, check.CodeType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src + "\n")
			if tc.code == "" {
				if len(d) > 0 {
					t.Fatalf("want clean, got %s", describe(d))
				}
			} else if len(d) != 1 || d[0].Code != tc.code {
				t.Fatalf("want one %s, got %s", tc.code, describe(d))
			}
		})
	}
}

// TestLetTypeNameBinds: a local let named like a package type binds, as in
// Go, so its reads are the local (types.md, Scope 1; L5). It was a refused
// type test before.
func TestLetTypeNameBinds(t *testing.T) {
	_, d := check.CheckSource("type Finished { N int }\nfunc F() int { let Finished = 3; return Finished + 1 }\n")
	if len(d) != 0 {
		t.Fatalf("want clean, got %s", describe(d))
	}
}

func TestPatternTestsOwnUnionType(t *testing.T) {
	for _, src := range []string{
		`type State = Pending{} | Finished{}
func F(s State) State { match s { case State as same: return same } }`,
		`type State[T] = Pending{Value T} | Finished{}
func F(s State[int]) State[int] { match s { case State[int] as same: return same } }`,
		`type State[T] = Pending{Value T} | Finished{}
func F(s State[int]) State[int] { match s { case State as same: return same } }`,
	} {
		if _, d := check.CheckSource(src + "\n"); len(d) > 0 {
			t.Fatalf("want own-type pattern to cover the union, got %s\n%s", describe(d), src)
		}
		if gaps := check.InfoGaps(src + "\n"); len(gaps) > 0 {
			t.Fatalf("own-type pattern Info gaps: %v\n%s", gaps, src)
		}
	}
}

func TestDuplicateConstantArmNamesEarlierCase(t *testing.T) {
	for _, tc := range []struct{ name, typ, first, second string }{
		{"integer", "int", "1", "1"},
		{"integer-16", "int", "16", "16"},
		{"rune-value", "rune", "'a'", "97"},
		{"string", "string", `"a"`, `"a"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("func F(x %s) int { match x { case %s: return 1\ncase %s: return 2\ncase _: return 0 } }\n", tc.typ, tc.first, tc.second)
			_, d := check.CheckSource(src)
			want := "this case is already covered by the earlier case `" + tc.first + "` (unreachable)"
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Message != want {
				t.Fatalf("want one S108 %q, got %s", want, describe(d))
			}
		})
	}
	_, d := check.CheckSource("func F(x string) int { match x { case \"a\", \"a\": return 1\ncase _: return 0 } }\n")
	if len(d) != 1 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, "the earlier case `\"a\"`") {
		t.Fatalf("duplicate alternative: want earlier-case diagnostic, got %s", describe(d))
	}
}

// Floats are refused before reachability: even two float32 constants that
// round to the same value must never form accepted match arms.
func TestFloatPatternRefusedBeforeReachability(t *testing.T) {
	_, d := check.CheckSource("func F(x float32) int { match x { case 0.1: return 1\ncase 0.1000000001: return 2\ncase _: return 0 } }\n")
	if len(d) != 2 {
		t.Fatalf("want a float-pattern refusal at each arm, got %s", describe(d))
	}
	for _, x := range d {
		if x.Code != check.CodeType || !strings.Contains(x.Message, "a float pattern compares for equality") {
			t.Fatalf("want a float-pattern refusal, got %s", x)
		}
	}
}

func TestPreludeUnionIsNotARecordVariant(t *testing.T) {
	_, d := check.CheckSource("type R { N int }\ntype E = Option[int] | R\n")
	if len(d) != 1 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, "a union variant is a record") {
		t.Fatalf("want a non-record variant refusal, got %s", describe(d))
	}
}
