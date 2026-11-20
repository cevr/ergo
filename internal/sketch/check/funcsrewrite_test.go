package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
)

// fusionFixture is std slices' Transform and Filter (internal/sketch/std/
// slices) with the fusion laws over them (owner q234, "Extend laws";
// FUSION): a law with type parameters of its own, function parameters
// and a function literal on its right side.
const fusionFixture = `type Bad {}

func Transform[T, U, E, R](xs []T, f func(x T) U fails E needs R) []U fails E needs R {
	let mut out = []U{}
	for x in xs {
		out = out.Append(try f(x))
	}
	out
}

func Filter[T, E, R](xs []T, keep func(x T) bool fails E needs R) []T fails E needs R {
	let mut out = []T{}
	for x in xs {
		if try keep(x) {
			out = out.Append(x)
		}
	}
	out
}

law TransformFuses[T, U, V comparable](xs []T, f func(x T) U, g func(x U) V) { Transform(Transform(xs, f), g) } == { Transform(xs, func(x T) V { g(f(x)) }) }

law FilterFuses[T comparable](xs []T, p func(x T) bool, q func(x T) bool) { Filter(Filter(xs, p), q) } == { Filter(xs, func(x T) bool { p(x) && q(x) }) }
`

// TestRewriteOverFunctions: a rewrite law over function parameters checks
// with no diagnostic and takes Law.FuncsRewrite, never Law.FuncRewrite, so
// no build rewrites by it before its lowering and evidence exist; it does
// not run yet (Law.Unrun says why); and the formatter keeps its type
// parameters.
//
// Mutants: fusion-overfuncs-unmarked (a law over functions takes
// FuncRewrite), fusion-pure-args-refused (a function that only calls its
// function parameters is impure in such a law), fusion-tparam-value-class
// (the sides' type parameter fails the value class), fusion-generic-runs
// (a generic law has no Unrun), fusion-format-drops-tparams.
func TestRewriteOverFunctions(t *testing.T) {
	pkg, diags := check.CheckSource(fusionFixture)
	if pkg == nil || len(diags) > 0 {
		t.Fatalf("the fusion laws do not check:\n\t%s", describe(diags))
	}
	for _, name := range []string{"TransformFuses", "FilterFuses"} {
		var law *check.Law
		for _, l := range pkg.Laws {
			if l.Name == name {
				law = l
			}
		}
		switch {
		case law == nil:
			t.Errorf("no law %s", name)
		case law.FuncsRewrite == nil || !law.FuncsRewrite.OverFuncs || law.FuncRewrite != nil:
			t.Errorf("law %s: FuncsRewrite %v, FuncRewrite %v; want a rewrite over functions only", name, law.FuncsRewrite, law.FuncRewrite)
		case law.Unrun == "":
			t.Errorf("law %s runs, and no run instantiates its type parameters", name)
		}
	}
	out, err := hostsyntax.Source(fusionFixture)
	if err != nil || out != fusionFixture {
		t.Errorf("the formatter changes the fusion laws (%v):\n%s", err, out)
	}
}

// TestRewriteOverFunctionsRows pins the texts of the refusals a rewrite
// over functions adds (funcrewrite.go, rules 1 and 4).
//
// Mutants: fusion-impure-param-admitted, fusion-left-literal-admitted.
func TestRewriteOverFunctionsRows(t *testing.T) {
	head := fusionFixture[:strings.Index(fusionFixture, "law ")]
	for _, tc := range []struct{ law, msg string }{
		{"law F(xs []int, f func(x int) int fails Bad) { Transform(Transform(xs, f), f) } == { Transform(xs, func(x int) int { f(f(x)) }) }\n",
			"law F is no rewrite: parameter f is a function that fails or needs, and a rewrite's functions are pure: write its type with no fails and no needs; write { lhs == rhs } for a property"},
		{"law G(xs []int) { Transform(xs, func(x int) int { x }) } == { xs }\n",
			"law G is no rewrite: its left side is a call tree, and a function literal is not a call; write { lhs == rhs } for a property"},
	} {
		// A failing parameter's calls are also S105 in the law's body;
		// the row is the rewrite's own refusal.
		_, diags := check.CheckSource(head + tc.law)
		var got []string
		for _, d := range diags {
			if d.Code == check.CodeType && strings.Contains(d.Message, "is no rewrite") {
				got = append(got, d.Message)
			}
		}
		if len(got) != 1 || got[0] != tc.msg {
			t.Errorf("%s\nwant one refusal %q, got\n\t%s", tc.law, tc.msg, describe(diags))
		}
	}
}
