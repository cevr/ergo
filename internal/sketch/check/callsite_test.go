package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestCallSiteDiagnostics: a call leaves out only a final parameter whose
// declared type is CallSite (types.md, Call sites 4); any other missing
// argument is S108 at the call's line. The rows' mutant: a final parameter
// of any type taken as the call's site (site-any-final-type). The rest of
// the rule is TestCallSitesAgree's (lower), which runs the calls.
func TestCallSiteDiagnostics(t *testing.T) {
	rows := []struct{ name, src, want string }{
		{"final parameter of another type", "func f(n int, m int) int {\n\tn + m\n}\nfunc g() int {\n\tf(1)\n}\n", "S108:5 not enough arguments: want 2, have 1"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
