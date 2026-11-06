//go:build linux

package closure

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A process of a run that leaves the run's process group and session
// (setsid) is out of reach of the group kill that ends a run; before the
// run's reaper, it survived the sweep. The probe stands in for such a
// process: the run's test starts a leader in a session of its own, the
// leader starts a holder, and both wait until the probe's test process
// ends, so no run of the probe leaves them for good.

// escapeVariable names the scratch directory where the leader writes its
// process id and the holder's, and marks each process of the escape (its
// environment holds the entry). escapeStageVariable is "leader" or
// "holder". escapeWatchVariable is the probe test's process id.
const (
	escapeVariable      = "ERGO_CLOSURE_ESCAPE"
	escapeStageVariable = "ERGO_CLOSURE_ESCAPE_STAGE"
	escapeWatchVariable = "ERGO_CLOSURE_ESCAPE_WATCH"
	// escapeProbeArgument is the only path in the escaped processes' argv.
	escapeProbeArgument = "/nonexistent/loop-probe-x"
)

// sysPidfdSendSignal is pidfd_send_signal(2), the same number on amd64 and
// arm64.
const sysPidfdSendSignal = 424

// TestEscapeRunTarget is the run's test: it runs only as the target of
// TestRunCollectsADescendantThatLeftItsSession.
func TestEscapeRunTarget(t *testing.T) {
	dir := os.Getenv(escapeVariable)
	if dir == "" {
		t.Skip("the target of TestRunCollectsADescendantThatLeftItsSession")
	}
	watch, err := strconv.Atoi(os.Getenv(escapeWatchVariable))
	if err != nil {
		t.Fatal(err)
	}
	fd, ok, err := openProcess(watch)
	if err != nil || !ok {
		t.Fatalf("the probe test %d: %v", watch, err)
	}
	hold := func() {
		if _, err := readableWithin(fd, forever); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := func(stage string, setsid bool) (*exec.Cmd, *bufio.Reader) {
		t.Helper()
		child := exec.Command(self, "-test.run=^TestEscapeRunTarget$", "-test.count=1", escapeProbeArgument)
		child.Env = append(os.Environ(), escapeStageVariable+"="+stage)
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: setsid}
		child.Stderr = os.Stderr
		out, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		return child, bufio.NewReader(out)
	}
	switch os.Getenv(escapeStageVariable) {
	case "holder":
		fmt.Println("ready")
		hold()
	case "leader":
		holder, holderOut := start("holder", false)
		if line, _ := holderOut.ReadString('\n'); line != "ready\n" {
			t.Fatalf("the holder did not start: %q", line)
		}
		pids := fmt.Sprintf("%d %d", os.Getpid(), holder.Process.Pid)
		if err := os.WriteFile(filepath.Join(dir, "pids"), []byte(pids), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ready")
		hold()
	default:
		// The run's test: it starts the leader in a session of its own,
		// waits until the leader says ready, and ends without waiting for
		// it, so the leader outlives it.
		_, leaderOut := start("leader", true)
		if line, _ := leaderOut.ReadString('\n'); line != "ready\n" {
			t.Fatalf("the leader did not start: %q", line)
		}
	}
}

// TestRunCollectsADescendantThatLeftItsSession: no process started under a
// run survives the run, one that called setsid included. The probe runs
// TestEscapeRunTarget under the sweep's runner (runProtected), then finds
// every process of the run by the marker in its environment
// (processesWith) and checks, by its pidfd, that none still runs. A
// survivor is reported and then ended by its pidfd, which names that one
// process; the test signals no other.
func TestRunCollectsADescendantThatLeftItsSession(t *testing.T) {
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
	marker := escapeVariable + "=" + scratchDir
	env := testEnv(f.pkg, marker, escapeWatchVariable+"="+strconv.Itoa(os.Getpid()))
	output := filepath.Join(t.TempDir(), "escape.out")
	runErr := runProtected(sandbox{Root: f.root, Scratch: []string{scratchDir}}, "internal/escape", f.pkg, env, output, self, "-test.run=^TestEscapeRunTarget$", "-test.count=1")
	var survivors []string
	for _, p := range processesWith(t, marker) {
		ended, err := readableWithin(p.fd, 0)
		if err == nil && !ended {
			survivors = append(survivors, fmt.Sprintf("%d (%s)", p.pid, strings.TrimSpace(p.cmdline)))
			if _, _, e := syscall.Syscall6(sysPidfdSendSignal, uintptr(p.fd), uintptr(syscall.SIGKILL), 0, 0, 0, 0); e != 0 {
				t.Errorf("end the survivor %d by its pidfd: %v", p.pid, e)
			} else if ended, _ := readableWithin(p.fd, 10*time.Second); !ended {
				t.Errorf("the survivor %d did not end after SIGKILL", p.pid)
			}
		}
		syscall.Close(p.fd)
	}
	if runErr != nil {
		out, _ := os.ReadFile(output)
		t.Fatalf("the run failed: %v\n%s", runErr, out)
	}
	pids, err := os.ReadFile(filepath.Join(scratchDir, "pids"))
	if err != nil {
		t.Fatalf("the run did not start its leader and holder: %v", err)
	}
	t.Logf("the run's leader and holder: pids %s", pids)
	if len(survivors) > 0 {
		t.Errorf("processes started under the run survived it: %s", strings.Join(survivors, ", "))
	}
}
