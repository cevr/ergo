package check_test

import "testing"

// Stage D1 round 2: the round 1 review's findings (stage-D1-round-1-
// findings.md), each as the program that showed it. runD1 compares the
// exact diagnostics, as CODE:line, so a cascade or a lost diagnostic fails.

// A type of the package is a name a local may take (types.md, Scope 1; L5,
// replacing stage C answers m3): the let binds R, which the body reads; R
// in a type position is still the type. (Round 2's M1 rows for prelude
// names went with D-D3's refusal, owner q244; TestBinderNamesAgree holds
// the rule now.)
func TestLocalTakesPackageTypeName(t *testing.T) {
	runD1(t, []d1Case{
		{"package-type-name", "type R { N int }\nfunc f(r R) int {\n\tlet R = r\n\tlet s R = R\n\ts.N\n}\n", nil},
	})
}

// m1, m2: Sleep is a prelude operation, in no scope (prelude-scope S1): a
// match arm binds it with no diagnostic. (The prelude-function rows went
// with D-D3's refusal, owner q244; TestBinderNamesAgree holds the rule.)
func TestPreludeNamesInCatchAndMatchArms(t *testing.T) {
	runD1(t, []d1Case{
		{"match-bare-Sleep", "func f(n int) int {\n\tmatch n {\n\tcase Sleep:\n\t\tSleep\n\t}\n}\n", nil},
	})
}

// M2: an assignment target rooted at a call's result or a literal is S108;
// one through a pointer is not.
func TestAssignmentRoots(t *testing.T) {
	const decls = "type R { N int }\nfunc xs() []int { []int{1, 2} }\nfunc r() R { R{N: 1} }\n"
	runD1(t, []d1Case{
		{"call-element", decls + "func f() {\n\txs()[0] = 5\n}\n", []string{"S108:5"}},
		{"literal-element", decls + "func f() {\n\t[]int{1, 2}[0] = 3\n}\n", []string{"S108:5"}},
		{"call-op-assign", decls + "func f() {\n\txs()[0] += 1\n}\n", []string{"S108:5"}},
		// Not addressable: typing's one diagnostic.
		{"call-field", decls + "func f() {\n\tr().N = 5\n}\n", []string{"S108:5"}},
	})
}
