package closure

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The round-7 verifier's hang (round-7-findings.md M2): an observed test
// process installed its seccomp filter, and a thread it made next waited at
// clone3 for an answer from a listener it had not handed over yet, so the
// run never ended and the sweep waited until its own timeout, leaving the
// pair behind. The guard stands in for that run with one that never ends by
// itself while the guard runs: an observed test that starts a child and
// blocks until the guard's process ends.

// stuckVariable names the scratch directory where TestStuckRunTarget writes
// its process id and its child's, and marks each process of the stuck run
// (its environment holds the entry). stuckHoldVariable makes it the child.
// stuckWatchVariable is the guard's process id: the target and its child
// end when that process ends, and not before, so no run of the guard leaves
// them for good, and a process the deadline did not stop is still there
// for the guard to see.
const (
	stuckVariable      = "ERGO_CLOSURE_STUCK"
	stuckHoldVariable  = "ERGO_CLOSURE_STUCK_HOLD"
	stuckWatchVariable = "ERGO_CLOSURE_STUCK_WATCH"
)

// TestStuckRunTarget is the stuck run: it runs only as the target of
// TestRunDeadlineStopsAStuckRun.
func TestStuckRunTarget(t *testing.T) {
	dir := os.Getenv(stuckVariable)
	if dir == "" {
		t.Skip("the target of TestRunDeadlineStopsAStuckRun")
	}
	// Open the guard's pidfd first: the guard runs until the run is
	// stopped, so the id names it now.
	guard, err := strconv.Atoi(os.Getenv(stuckWatchVariable))
	if err != nil {
		t.Fatal(err)
	}
	fd, ok, err := openProcess(guard)
	if err != nil || !ok {
		t.Fatalf("the guard %d: %v", guard, err)
	}
	hold := func() {
		if _, err := readableWithin(fd, forever); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv(stuckHoldVariable) != "" {
		hold()
		return
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
	hold()
	child.Wait()
}

// forever is a wait with no bound in readableWithin.
const forever = 100 * 365 * 24 * time.Hour

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
	t.Setenv(runDeadlineVariable, "5s")
	if os.Getenv(binaryDeadlineVariable) != "" {
		// TestRunDeadlineFollowsTheTestDeadline: only this binary's own
		// -test.timeout can stop the run in time.
		t.Setenv(runDeadlineVariable, "20m")
	}
	sb := sandbox{Root: f.root, Scratch: []string{scratchDir, logDir}}
	env := testEnv(f.pkg, stuckVariable+"="+scratchDir, stuckWatchVariable+"="+strconv.Itoa(os.Getpid()))
	output := filepath.Join(t.TempDir(), "stuck.out")
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// Observed, as the sweep runs a package: the test log makes
		// testinputs.Main start its observer and child.
		done <- runProtected(sb, "internal/stuck", f.pkg, env, output, self, "-test.run=^TestStuckRunTarget$", "-test.testlogfile="+filepath.Join(logDir, "log"))
	}()
	// Only the test's own deadline bounds the waits (a hang is permanent,
	// so -test.timeout sets the bound); see testBounds.
	runBound, endBound := testBounds(t)
	var runErr error
	select {
	case runErr = <-done:
	case <-runBound:
		t.Fatalf("the stuck run was not stopped before 8/10 of the test's deadline (%s=%s); it ends when this test process ends", runDeadlineVariable, os.Getenv(runDeadlineVariable))
	}
	t.Logf("stopped after %v: %v", time.Since(start).Round(time.Millisecond), runErr)
	if runErr == nil || !strings.Contains(runErr.Error(), "internal/stuck") || !strings.Contains(runErr.Error(), "did not end within") {
		t.Fatalf("want a failure that names the package and the deadline; got %v", runErr)
	}
	// The failure lists the run's processes at the deadline, the run's
	// reaper (this test binary) first.
	if self := filepath.Base(self); !strings.Contains(runErr.Error(), "its processes then:\n  "+self[:min(len(self), 15)]+" (pid ") {
		t.Fatalf("want a failure that lists the run's processes, its reaper %s first; got %v", self, runErr)
	}
	// The run's start-up (sandbox, observer, target, child) takes a time
	// that the host's load sets, and the deadline can stop the run at any
	// point of it. So the test does not need the target's pids file: it
	// finds every process of the run by the marker that each one carries in
	// its environment (the target's child inherits it; a process forked
	// but not yet exec'd has it too), and waits for each to end.
	if data, err := os.ReadFile(filepath.Join(scratchDir, "pids")); err == nil {
		t.Logf("the deadline stopped the run after the target started its child (pids %s)", data)
	} else {
		t.Logf("the deadline stopped the run in its start-up, before the target started its child")
	}
	for _, p := range processesWith(t, stuckVariable+"="+scratchDir) {
		left := time.Until(endBound)
		if endBound.IsZero() {
			left = forever // no test deadline: -test.timeout=0 asked for no bound
		}
		ended, err := readableWithin(p.fd, max(left, 0))
		syscall.Close(p.fd)
		if err != nil {
			t.Fatal(err)
		}
		if !ended {
			t.Errorf("process %d of the stuck run (%s) is still running", p.pid, p.cmdline)
		}
	}
}

// binaryDeadlineVariable makes TestRunDeadlineStopsAStuckRun set a run
// deadline past its binary's -test.timeout, as the gate's sweep step had
// it: 20 minutes in a 15 minute test (batch 9).
const binaryDeadlineVariable = "ERGO_CLOSURE_STUCK_BINARY_DEADLINE"

// stuckBinaryTimeout is the -test.timeout of the binary that
// TestRunDeadlineFollowsTheTestDeadline runs: runReportMargin (a minute)
// and 30 s for the run.
const stuckBinaryTimeout = 90 * time.Second

// A stuck run whose run deadline lies past the test binary's own deadline
// is stopped by the binary's deadline (runDeadline): the stuck-run test,
// in a binary with a -test.timeout of stuckBinaryTimeout and a run
// deadline of 20 minutes, stops the run, names its package and processes,
// and passes, where without the bound its run would hold until the
// testing package's timeout panicked (the batch-9 sweep, b9-fix-gate).
func TestRunDeadlineFollowsTheTestDeadline(t *testing.T) {
	if os.Getenv(binaryDeadlineVariable) != "" {
		t.Skip("runs the stuck-run test; not in itself")
	}
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(self, "-test.run=^TestRunDeadlineStopsAStuckRun$", "-test.count=1", "-test.v", "-test.timeout="+stuckBinaryTimeout.String())
	command.Env = append(os.Environ(), binaryDeadlineVariable+"=1")
	start := time.Now()
	output, err := command.CombinedOutput()
	t.Logf("the stuck-run test with -test.timeout=%v ended after %v", stuckBinaryTimeout, time.Since(start).Round(time.Millisecond))
	if err != nil || !strings.Contains(string(output), "--- PASS: TestRunDeadlineStopsAStuckRun") {
		t.Fatalf("the stuck run was not stopped before its binary's deadline (%v):\n%s", err, output)
	}
}

// testBounds splits the time left before the test's deadline: a channel
// that fires at 8/10 of it (the run must have stopped), and the time at
// 9/10 (each process of the run must have ended); the last tenth is for the
// report, before the testing package's own timeout panics. Without a
// deadline (-test.timeout=0) the channel never fires and the time is zero.
func testBounds(t *testing.T) (<-chan time.Time, time.Time) {
	deadline, ok := t.Deadline()
	if !ok {
		return nil, time.Time{}
	}
	left := time.Until(deadline)
	return time.After(left * 8 / 10), time.Now().Add(left * 9 / 10)
}

// process is a live process found by processesWith: fd is a pidfd of it.
type process struct {
	pid     int
	fd      int
	cmdline string
}

// processesWith returns a pidfd of each live process whose environment
// holds entry. Each pidfd is opened before the environment is read, so it
// names the process whose environment matched: a process id reused after
// the pidfd was opened belongs to a process the pidfd does not name, and
// then the pidfd reads as ended, which is true of the matched one. A zombie
// shows an empty environment and is not returned: it has ended.
func processesWith(t *testing.T, entry string) []process {
	t.Helper()
	list, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var found []process
	for _, e := range list {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		fd, ok, err := openProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		environ, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if !slices.Contains(strings.Split(string(environ), "\x00"), entry) {
			syscall.Close(fd)
			continue
		}
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		found = append(found, process{pid: pid, fd: fd, cmdline: strings.ReplaceAll(string(cmdline), "\x00", " ")})
	}
	return found
}
