package eval

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// licenceFusionSrc is std slices' Transform, Filter and the one-loop
// helpers, with the four fusion laws as std states them, and three false
// laws over functions.
const licenceFusionSrc = `func Transform[T, U, E, R](xs []T, f func(x T) U fails E needs R) []U fails E needs R {
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

func TransformFilter[T, U, E, R](xs []T, f func(x T) U fails E needs R, keep func(x U) bool fails E needs R) []U fails E needs R {
	let mut out = []U{}
	for x in xs {
		let y = try f(x)
		if try keep(y) {
			out = out.Append(y)
		}
	}
	out
}

func FilterTransform[T, U, E, R](xs []T, keep func(x T) bool fails E needs R, f func(x T) U fails E needs R) []U fails E needs R {
	let mut out = []U{}
	for x in xs {
		if try keep(x) {
			out = out.Append(try f(x))
		}
	}
	out
}

law TransformFuses[T, U, V comparable](xs []T, f func(x T) U, g func(x U) V) { Transform(Transform(xs, f), g) } == { Transform(xs, func(x T) V { g(f(x)) }) }

law FilterFuses[T comparable](xs []T, p func(x T) bool, q func(x T) bool) { Filter(Filter(xs, p), q) } == { Filter(xs, func(x T) bool { p(x) && q(x) }) }

law TransformThenFilter[T, U comparable](xs []T, f func(x T) U, p func(x U) bool) { Filter(Transform(xs, f), p) } == { TransformFilter(xs, f, p) }

law FilterThenTransform[T, U comparable](xs []T, p func(x T) bool, f func(x T) U) { Transform(Filter(xs, p), f) } == { FilterTransform(xs, p, f) }

law TwiceIsOnce[T comparable](xs []T, f func(x T) T) { Transform(Transform(xs, f), f) } == { Transform(xs, f) }

law FilterBeforeTransform[T comparable](xs []T, f func(x T) T, p func(x T) bool) { Filter(Transform(xs, f), p) } == { FilterTransform(xs, p, f) }

law LenKept(xs []int, f func(x int) int) { len(Transform(xs, f)) } == { len(xs) }
`

// TestLicenceOverFunctions: the licence run of a law over function
// parameters (FUSION-EVIDENCE; types.md, Laws 14) runs it at int, each
// function parameter over the family of pure functions, and reads the
// right side's defect as the left side's outcome (v1.FuseOr's rule). The
// four std fusion laws are validated, every class and pair reached; a
// false law is refuted, at a family member it names.
//
// Mutants: fusion-licence-fuseor-ignored (the right side's defect is
// compared as it is: TransformFuses at f = defect at odd x meets the
// defect of g first, and is refuted), fusion-licence-no-defect-member (the
// int family has no function that raises a defect, so no case asks which
// defect comes first), fusion-licence-unrun (a law over functions is
// skipped by its Unrun, testplan's reason), fusion-licence-defect-both-ways (a left side's defect is read
// as the right side's outcome too, so LenKept holds).
func TestLicenceOverFunctions(t *testing.T) {
	pkg, diags := check.CheckSource(licenceFusionSrc)
	if pkg == nil || len(diags) > 0 {
		t.Fatalf("the fixture does not check: %v", diags)
	}
	byName := map[string]*check.Law{}
	for _, law := range pkg.Laws {
		byName[law.Name] = law
	}
	run := func(name string) Licence {
		t.Helper()
		law := byName[name]
		if law == nil || law.FuncsRewrite == nil {
			t.Fatalf("%s is no law over function parameters", name)
		}
		l, err := LicenceRun(pkg, law, LicenceBudget(), nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return l
	}
	for _, name := range []string{"TransformFuses", "FilterFuses", "TransformThenFilter", "FilterThenTransform"} {
		l := run(name)
		if l.Verdict != "validated" || len(l.Missed) > 0 {
			t.Errorf("%s: %s; want validated, every class reached", name, l.Line())
		}
		if !strings.Contains(l.Line(), "func defect at") {
			t.Errorf("%s: %s; want the family's defect member among its classes", name, l.Line())
		}
	}
	if l := run("TwiceIsOnce"); l.Verdict != "refuted" || !strings.Contains(l.Reason, "the law is false") {
		t.Errorf("TwiceIsOnce: %s; want refuted, the law false", l.Line())
	}
	// p reads f(x) on the left and x on the right: refuted at a case whose
	// arguments name family members.
	l := run("FilterBeforeTransform")
	if l.Verdict != "refuted" || !strings.Contains(l.Line(), "f = func") {
		t.Errorf("FilterBeforeTransform: %s; want refuted at named family members", l.Line())
	}
	// LenKept holds for every total f, but its left side runs f and its
	// right side never does: at f = func defect at odd x the staged form
	// defects and the rewritten one returns, which is a refutation.
	if l := run("LenKept"); l.Verdict != "refuted" || !strings.Contains(l.Line(), "f = func defect at odd x") {
		t.Errorf("LenKept: %s; want refuted where only the left side defects", l.Line())
	}
}
