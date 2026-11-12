package check

import (
	"strings"
	"testing"
)

// TestConflictedSlotKinds: a function passes its bracket parameters to a
// declaration whose E has two kinds. The conflict is reported once, at the
// declaration; the parameter in E's slot takes no kind from it, and only
// if it is unconstrained (A, constrained, keeps its own kind, so its
// misuse is still reported); the other slots still give their parameters
// their kinds (B a capability row, so no misuse at J[A, B]). Mutants
// (ledger): t6-6 marks a constrained parameter conflicted (A's misuse
// goes silent); t6-23 skips the whole index expression (B is left a type
// and J[A, B] reports it).
func TestConflictedSlotKinds(t *testing.T) {
	const src = "type J[E, R] { f func() fails E, k func() needs E, h func() needs R }\nfunc run[A comparable, B](j J[A, B]) A { 1 }\n"
	_, ds := checkFor(src)
	var got []string
	for _, d := range ds {
		got = append(got, d.String())
	}
	want := []string{
		"1:8: S108 type parameter E is used both as a type and as a row (types.md, Generics 1)",
		"2:42: S108 cannot use int as A in the body's tail value",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
