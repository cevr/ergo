package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestExternRowCoversCallbacks (CK-EXTROW; rows.md, Capability rows 8): an
// extern whose callback's capability row names a row parameter lists it in
// its own row, or holds it in a result or another parameter's type; one
// that drops it is S102 at the callback parameter, with the fix. A
// function the host hands to the callback charges nothing.
//
// Mutants (check ledger): the result is not looked at
// (ck-extrow-results-ignored); the rows of a function the callback
// receives are charged (ck-extrow-received-charged); the diagnostic is at
// the declaration, not at the parameter (ck-extrow-at-declaration). The
// rule itself is guarded by std: dropping R from http's serve binding
// fails every check that imports http (http-serve-row).
func TestExternRowCoversCallbacks(t *testing.T) {
	for _, c := range []struct{ name, src, at, text string }{
		{"dropped", "extern go example.com/x.Serve func serve[R](n int, handle func() needs R)\n", "func() needs R", "the host may call handle, whose capability row has R, which serve does not list: write needs R"},
		{"listed", "extern go example.com/x.Serve func serve[R](handle func() needs R) needs R\n", "", ""},
		{"returned", "type Box[R] { Run func() needs R }\n\nextern go example.com/x.Wrap func wrap[R](f func() needs R) Box[R]\n", "", ""},
		{"received", "extern go example.com/x.Feed func feed[R](run func(send func() needs R))\n", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, diags := check.CheckSource(c.src)
			var s102 []check.Diagnostic
			for _, d := range diags {
				if d.Code == "S102" {
					s102 = append(s102, d)
				}
			}
			if c.at == "" {
				if len(s102) > 0 {
					t.Fatalf("want no S102, got %s", describe(s102))
				}
				return
			}
			if len(s102) != 1 || !strings.Contains(s102[0].Message, c.text) {
				t.Fatalf("want one S102 %q, got %s", c.text, describe(diags))
			}
			if got := c.src[s102[0].Offset : s102[0].Offset+s102[0].Length]; got != c.at {
				t.Errorf("the S102 spans %q, want %q", got, c.at)
			}
		})
	}
}
