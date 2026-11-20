package eval

import (
	"net/url"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestParseQueryReadsGODEBUG: std http's ParseQuery gives no fields past
// Go's limit on query pairs, which Go's url.ParseQuery reads from GODEBUG
// urlmaxqueryparams (NP4f-URL), run here over Go's own ParseQuery
// (gohost.go). Each query's field count is held to Go's under the default
// and two custom limits. Its mutant row left with NP4f-URL's body when SM2
// moved ParseQuery to std url (the b24 merge, I-FLIP).
func TestParseQueryReadsGODEBUG(t *testing.T) {
	pkg := checked(t, "import http\n\ntest T {\n\tassert http.AnySuccess == http.AnySuccess\n}\n")
	var fn *check.Func
	for _, p := range imports(pkg) {
		for _, f := range p.Funcs {
			if p.Path+"."+f.Name == "http.ParseQuery" {
				fn = f
			}
		}
	}
	in := &interp{budget: DefaultBudget(), host: &host{}}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	queries := []string{"", "a=1", "a=1&b=2", "a=1&b=2&c=3", "a=1&b=2&c=3&d=4&e=5"}
	for _, debug := range []string{"", "urlmaxqueryparams=3", "urlmaxqueryparams=5"} {
		t.Setenv("GODEBUG", debug)
		for _, q := range queries {
			in.steps, in.bytes = 0, 0
			v, ab := in.call(fr, nil, fn, nil, []Value{q})
			if ab != nil {
				t.Fatalf("GODEBUG=%q ParseQuery(%q): %s", debug, q, outcome(func() (Value, *abrupt) { return v, ab }))
			}
			values, err := url.ParseQuery(q)
			want := 0
			if err == nil {
				for _, vs := range values {
					want += len(vs)
				}
			}
			if got := v.(sliceV).n; got != want {
				t.Errorf("GODEBUG=%q ParseQuery(%q): %d fields, Go's url.ParseQuery %d (%v)", debug, q, got, want, strings.TrimSpace(describe(v)))
			}
		}
	}
}
