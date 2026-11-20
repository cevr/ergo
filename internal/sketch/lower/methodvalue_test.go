package lower

import (
	"os"
	"strings"
	"testing"
)

// methodValueProbe is eval's method-value probe (types.md, Methods 2;
// CV1), read once for the program table.
var methodValueProbe = func() string {
	src, err := os.ReadFile("../eval/testdata/methodvalue.ergo")
	if err != nil {
		panic(err)
	}
	return string(src)
}()

// TestMethodValueRuns runs the method-value probe's seeded tests built: a
// method value is the closure that calls x.f, with x's value copied where
// x.f is evaluated (calls.go methodValue196), so a later write to x, to a
// field of x or through a borrow of x does not reach it. It catches
// cv-method-value-late-binding (the receiver not spilled, so the closure
// reads the variable at the call) in the package ledger; eval's
// TestMethodValueAgrees checks the same probe at check time.
//
// It builds Go because the tests run in the built test binary, where the
// closure's capture is Go's.
func TestMethodValueRuns(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "methodvalue"), "--seed", "1")
	if code != 0 {
		t.Errorf("the method-value probe failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, name := range []string{"MethodValueIsTheClosure", "MethodValueBindsAtTheTake", "MethodValueOfAnInstance"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("the probe did not report %s:\n%s", name, stdout)
		}
	}
}
