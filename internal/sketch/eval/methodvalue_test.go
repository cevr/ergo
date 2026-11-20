package eval

import (
	"os"
	"testing"
)

// TestMethodValueAgrees evaluates the method-value probe at check time
// (types.md, Methods 2; CV1): each seeded test passes, so a method value
// is the closure that calls x.f, binds x where it is evaluated, and binds
// an instance's type arguments. It catches cv-method-value-eval-borrow
// (a borrow's receiver bound as the borrow, not its value) in the
// package ledger; lower's
// TestMethodValueRuns runs the same probe built.
func TestMethodValueAgrees(t *testing.T) {
	src, err := os.ReadFile("testdata/methodvalue.ergo")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"MethodValueIsTheClosure": true, "MethodValueBindsAtTheTake": true, "MethodValueOfAnInstance": true, "MethodValueBindsBeforeLaterArguments": true}
	for _, r := range evalSeeded(t, string(src), 1).Results {
		if r.Status != Passed {
			t.Errorf("%s: %s %s", r.Name, r.Status, r.Reason)
		}
		delete(want, r.Name)
	}
	for name := range want {
		t.Errorf("%s did not run", name)
	}
}
