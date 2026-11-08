package closure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fu-lower-flake gate: a run of internal/sketch/lower failed, and the
// sweep's report showed only the last 4000 bytes of its output, the
// lowered Go that the failing test's message prints last. The name of the
// failing test and its message were cut off. The guard stands in for that
// run with one whose failure message comes before a long dump.

// longFailureVariable makes TestLongFailureTarget fail.
const longFailureVariable = "ERGO_CLOSURE_LONG_FAILURE"

// longFailureMessage is the message the target fails with, before its dump.
const longFailureMessage = "seed 7731: the property failed"

// TestLongFailureTarget fails with a message, a seed in it, followed by a
// dump longer than the sweep's tail. It runs only as the target of
// TestFailedRunNamesItsFailure.
func TestLongFailureTarget(t *testing.T) {
	if os.Getenv(longFailureVariable) == "" {
		t.Skip("the target of TestFailedRunNamesItsFailure")
	}
	t.Fatalf("%s\n%s", longFailureMessage, strings.Repeat("\tthe lowered program, line by line\n", 400))
}

// TestFailedRunNamesItsFailure: the error of a failed run names the
// failing test and its message, seed included, before the output's tail,
// wherever they are in the output.
func TestFailedRunNamesItsFailure(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	scratchDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "long.out")
	runErr := runProtected(sandbox{Root: f.root, Scratch: []string{scratchDir}}, "internal/long", f.pkg, testEnv(f.pkg, longFailureVariable+"=1"), output, self, "-test.run=^TestLongFailureTarget$", "-test.count=1")
	if runErr == nil {
		t.Fatal("the failing target's run succeeded")
	}
	out, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	msg := runErr.Error()
	head, tail, ok := strings.Cut(msg, "ends:\n")
	if !ok {
		t.Fatalf("no output tail in the error: %s", msg)
	}
	if strings.Contains(tail, longFailureMessage) {
		t.Fatalf("the output's tail holds the message, so this guard tests nothing; make the dump longer than the tail:\n%s", out)
	}
	for _, want := range []string{"--- FAIL: TestLongFailureTarget", longFailureMessage} {
		if !strings.Contains(head, want) {
			t.Errorf("the error does not name %q before the output's tail:\n%s", want, head)
		}
	}
}

// TestFailureLines: each failure line and the line after it, in order,
// and every running test a timeout lists; lines of a passing test and of
// a dump are not.
func TestFailureLines(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestA",
		"--- PASS: TestA (0.00s)",
		"--- FAIL: TestB (1.00s)",
		"    --- FAIL: TestB/sub (1.00s)",
		"        b_test.go:9: native: exit status 1",
		"            the first line of the native output",
		"            the second line, dropped",
		// A native test's timeout, nested in its parent's message.
		"            panic: test timed out after 30s",
		"            \trunning tests:",
		"            \t\tTestC (30s)",
		"            \t\tTestC/leaf (30s)",
		"            ",
		"            goroutine 1 [chan receive]:",
		"            \t\tthe stuck frame, dropped",
	}, "\n")
	want := strings.Join([]string{
		"  --- FAIL: TestB (1.00s)",
		"      --- FAIL: TestB/sub (1.00s)",
		"          b_test.go:9: native: exit status 1",
		"              the first line of the native output",
		"              panic: test timed out after 30s",
		"              \trunning tests:",
		"              \t\tTestC (30s)",
		"              \t\tTestC/leaf (30s)",
	}, "\n")
	if got := failureLines([]byte(out)); got != want {
		t.Errorf("failureLines:\n%s\nwant:\n%s", got, want)
	}
	if got := failureLines([]byte("ok\nPASS\n")); !strings.Contains(got, "none") {
		t.Errorf("an output with no failure gave %q", got)
	}
}
