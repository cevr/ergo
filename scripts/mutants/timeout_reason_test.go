//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestTimeoutReasonIsOneEvent: a child whose failure is only a timeout
// has one reason, whichever clock fired first: its own -test.timeout
// (its panic line, then its exit) or the runner's deadline (the group
// stop). Under the 2026-11-22 IO stall the runner's deadline won the race
// and TestRunnerMatchesModel/seed2 read "deadline" where the model said
// "fail" (MUTANTS-MODEL-TIMING, /tmp/opencode/b22/h/g25zr.log). Each
// order is forced here, with no sleep: (a) the child prints the panic and
// exits under an hour's deadline; (b) the deadline is 0, so the runner's
// timer fires at once, while the child blocks opening a FIFO nobody
// writes, until the group stop ends it. The child is sh, so it builds no
// Go. Mutant (testdata/mutants.json): mut-timeout-reason-by-clock.
func TestTimeoutReasonIsOneEvent(t *testing.T) {
	work := t.TempDir()
	e := &engine{cfg: Config{Work: work}}
	ownClock := e.exec(context.Background(), work, []string{"sh", "-c", "echo '" + timeoutPanic + "1s'; exit 2"}, os.Environ(), time.Hour)
	fifo := filepath.Join(work, "never-written")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	runnerClock := e.exec(context.Background(), work, []string{"sh", "-c", "read x < " + fifo}, os.Environ(), 0)
	for name, o := range map[string]outcome{"the test's own timeout": ownClock, "the runner's deadline": runnerClock} {
		if !o.timeout {
			t.Errorf("%s: reason %q, not a timeout", name, o.reason)
		}
	}
	if ownClock.reason != runnerClock.reason || ownClock.reason != "timeout" {
		t.Errorf("the reason depends on which clock fired: the test's own %q, the runner's %q; want \"timeout\" for both", ownClock.reason, runnerClock.reason)
	}
}
