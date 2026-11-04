package closure

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The round-7 verifier's hang (round-7-findings.md M2): an observed test
// process installed its seccomp filter, and a thread it made next waited at
// clone3 for an answer from a listener it had not handed over yet, so the
// run never ended and the sweep waited until its own timeout, leaving the
// pair behind. The guard stands in for that run with one that never ends by
// itself (within the guard's time): an observed test that starts a child
// and blocks. It uses only what the round-7 head had, so it runs there too,
// where no deadline stops the run.

// stuckVariable names the scratch directory where TestStuckRunTarget writes
// its process id and its child's; stuckHoldVariable makes it the child.
const (
	stuckVariable     = "ERGO_CLOSURE_STUCK"
	stuckHoldVariable = "ERGO_CLOSURE_STUCK_HOLD"
	stuckFor          = 90 * time.Second // the target ends by itself after this, so no run of the guard leaves it for good
)

// TestStuckRunTarget is the stuck run: it runs only as the target of
// TestRunDeadlineStopsAStuckRun.
func TestStuckRunTarget(t *testing.T) {
	if os.Getenv(stuckHoldVariable) != "" {
		time.Sleep(stuckFor)
		return
	}
	dir := os.Getenv(stuckVariable)
	if dir == "" {
		t.Skip("the target of TestRunDeadlineStopsAStuckRun")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, "-test.run=^TestStuckRunTarget$")
	child.Env = append(os.Environ(), stuckHoldVariable+"=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pids := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
	if err := os.WriteFile(filepath.Join(dir, "pids"), []byte(pids), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(stuckFor)
	child.Wait()
}

// TestRunDeadlineStopsAStuckRun: a run that does not end within the run
// deadline is a failure that names its package, well before the deadline of
// the test that runs it, and no process of the run is left.
func TestRunDeadlineStopsAStuckRun(t *testing.T) {
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
	logDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERGO_CLOSURE_RUN_DEADLINE", "5s")
	sb := sandbox{Root: f.root, Scratch: []string{scratchDir, logDir}}
	env := testEnv(f.pkg, stuckVariable+"="+scratchDir)
	output := filepath.Join(t.TempDir(), "stuck.out")
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// Observed, as the sweep runs a package: the test log makes
		// testinputs.Main start its observer and child.
		done <- runProtected(sb, "internal/stuck", f.pkg, env, output, self, "-test.run=^TestStuckRunTarget$", "-test.testlogfile="+filepath.Join(logDir, "log"))
	}()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(45 * time.Second):
		t.Fatalf("the stuck run was not stopped within 45s (the run deadline is 5s); it ends by itself after %v", stuckFor)
	}
	t.Logf("stopped after %v: %v", time.Since(start).Round(time.Millisecond), runErr)
	if runErr == nil || !strings.Contains(runErr.Error(), "internal/stuck") || !strings.Contains(runErr.Error(), "did not end within") {
		t.Fatalf("want a failure that names the package and the deadline; got %v", runErr)
	}
	data, err := os.ReadFile(filepath.Join(scratchDir, "pids"))
	if err != nil {
		t.Fatalf("the target did not start its child: %v", err)
	}
	// Each process of the run is gone: no /proc entry runs this target any
	// more (a reused process id runs something else).
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		gone := false
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil || !strings.Contains(string(cmdline), "TestStuckRunTarget") || strings.Contains(string(stat), ") Z ") {
				gone = true
				break
			}
		}
		if !gone {
			t.Errorf("process %d of the stuck run is still running", pid)
		}
	}
}
