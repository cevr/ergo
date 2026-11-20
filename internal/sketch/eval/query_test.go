package eval

import "testing"

// TestQueryParsesAtCheckTime: std http.ParseQuery runs at check time as
// the build's: its Ergo body (std/http/query.ergo, NP4f-15) over Go's
// url.ParseQuery for the limit, where "+" is a space and %XX an escape, a
// pair with a malformed escape is dropped, the fields come in key order
// and one key's values in the order they came. Before MX-EVQUERY it was
// skipped ("a Go function (ParseQuery)"), so fk-domains' 41 obligations
// never ran at check time; the Ergo body needs eval's bs.Grow native, and
// this test kills evgrow-unmapped (testdata/mutants.json). The order and
// escape rules are the std ledger's np4f15 rows, against Go's.
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
