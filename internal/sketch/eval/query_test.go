package eval

import "testing"

// TestQueryParsesAtCheckTime: std http.ParseQuery runs at check time as
// the build's (runtime/v1/http HTTPParseQuery: Go's url.ParseQuery): "+"
// is a space and %XX an escape, a pair with a malformed escape is
// dropped, the fields come in key order and one key's values in the
// order they came. Before MX-EVQUERY it was skipped ("a Go function
// (ParseQuery)"), so domain-search' 41 obligations never ran at check time.
// No example kills its mutants (testdata/mutants.json): without the
// native domain-search' rows only turn back into skips, and its queries
// carry neither a "+" nor keys out of order, so evquery-unsorted and
// evquery-plus-literal leave its verdicts as they are; this test kills
// evquery-missing, evquery-unsorted and evquery-plus-literal.
func TestQueryParsesAtCheckTime(t *testing.T) {
	pkg := checked(t, `import http

test Parses {
	let fs = http.ParseQuery("b=2&a=x+y&b=1&c=%zz&a=%21")
	assert len(fs) == 4
	assert fs[0].Name == "a" && fs[0].Value == "x y"
	assert fs[1].Name == "a" && fs[1].Value == "!"
	assert fs[2].Name == "b" && fs[2].Value == "2"
	assert fs[3].Name == "b" && fs[3].Value == "1"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range rep.Results {
		if r.Name != "Parses" {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("Parses: %s (%s), want passed", r.Status, r.Reason)
		}
	}
	if seen != 1 {
		t.Errorf("Parses did not report: %+v", rep.Results)
	}
}
