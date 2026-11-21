package lower

import (
	"sort"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdSemverLaws is the statement of each law of std semver, by ledger key.
var stdSemverLaws = map[string]string{
	"semver CompareIsAntisymmetric": `CompareIsAntisymmetric(a Version, b Version) { Compare(nearby(a), nearby(b)) == -Compare(nearby(b), nearby(a)) }`,
	"semver CompareIsTransitive":    `CompareIsTransitive(a Version, b Version, c Version) { let x = nearby(a) let y = nearby(b) let z = nearby(c) Compare(x, y) > 0 || Compare(y, z) > 0 || Compare(x, z) <= 0 }`,
	"semver CompareIgnoresBuild":    `CompareIgnoresBuild(v Version, build []string) { Compare(v, Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch, Pre: v.Pre, Build: build}) == 0 }`,
	"semver ParseInvertsFormat":     `ParseInvertsFormat(major int, minor int, patch int, n int, word bool) { let pre = if n%3 == 0 { []string{} } else if word { []string{"rc", "\((n%7+7)%7)"} } else { []string{"\((n%100+100)%100)"} } let v = Version{Major: (major%1000+1000)%1000, Minor: (minor%1000+1000)%1000, Patch: (patch%1000+1000)%1000, Pre: pre, Build: []string{}} let back = Parse(Format(v)) catch { case Malformed: return false } Compare(back, v) == 0 && Format(back) == Format(v) }`,
	"semver OneComparatorHolds":     `OneComparatorHolds(c Comparator, v Version) { len(v.Pre) > 0 || Satisfies(Range{Sets: [][]Comparator{[]Comparator{c}}}, v) == Holds(c, v) }`,
}

// TestStdSemverLaws runs the laws of std semver in process: the package
// is checked from its embedded source as std checks it, and the evaluator
// runs each law; lawledger.Run holds that this runner restates exactly
// the ledger's semver laws and that each passed.
//
// Mutant only it kills (internal/sketch/stdlib/testdata/mutants.json):
// sq4-word-prefix-equal.
func TestStdSemverLaws(t *testing.T) {
	sources, ok := stdlib.Package("semver")
	if !ok {
		t.Fatal("no std package semver")
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	var files []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatal(err)
		}
		f.Name = name
		files = append(files, f)
	}
	pkg, diags := check.StdFixture("semver", files)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results[r.Name] = r
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range stdSemverLaws {
		name := "law " + key[len("semver "):]
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			r, ok := results[name]
			if !ok {
				t.Fatalf("the evaluator ran no %s", name)
			}
			if r.Status != eval.Passed {
				t.Errorf("%s: %s: %s", name, r.Status, r.Reason)
			}
		}}
	}
	lawledger.Run(t, laws)
}
