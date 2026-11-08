//go:build linux

package closure

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reapEnded runs on each SIGCHLD in the run's reaper. It must reap each
// ended child except the helper, whose exec.Cmd keeps the run's exit
// status, and it must never block on a child that still runs, or the
// reaper stops reading the end of the helper and SIGTERM. The test calls
// reapEnded directly in a re-executed process of its own, so that
// reapEnded sees only the children that the test starts. No timing race
// is needed: both ended children are zombies before the call.

// reapStageVariable is "run", "exit" or "stay".
const (
	reapStageVariable = "ERGO_CLOSURE_REAP_STAGE"
	// reapHelperCode is the helper's exit status, which must reach Wait.
	reapHelperCode = 7
	// reapCallBound is how long reapEnded may take. A reapEnded that
	// blocks on the running child blocks until the stage closes its input,
	// so any bound tells it apart; 30 s and not 5 s, for a loaded host.
	reapCallBound = 30 * time.Second
	// reapChecked is printed when every check ran, so a skip cannot pass.
	reapChecked = "reap-probe: every check ran"
)

// procState gives the state letter of pid from /proc/<pid>/stat, or "gone".
func procState(pid int) string {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "gone"
	}
	end := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) == 0 {
		return "gone"
	}
	return fields[0]
}

// reapChild starts this test binary as a child at the given stage. Its
// argv names only the harmless probe path besides the test flags.
func reapChild(self, stage string) *exec.Cmd {
	cmd := exec.Command(self, "-test.run=^TestReapEndedTarget$", "-test.count=1", escapeProbeArgument)
	cmd.Env = append(os.Environ(), reapStageVariable+"="+stage)
	return cmd
}

// TestReapEndedTarget runs only as a stage of
// TestReapEndedLeavesTheHelperAndRunningChildren.
func TestReapEndedTarget(t *testing.T) {
	switch os.Getenv(reapStageVariable) {
	case "":
		t.Skip("a stage of TestReapEndedLeavesTheHelperAndRunningChildren")
	case "exit":
		os.Exit(reapHelperCode)
	case "stay":
		// Runs until its stdin, held open by the "run" stage, closes.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "run":
		runReapStage(t)
	default:
		t.Fatalf("unknown stage %q", os.Getenv(reapStageVariable))
	}
}

func runReapStage(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := reapChild(self, "exit")
	other := reapChild(self, "exit")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(20 * time.Second)
	for procState(helper.Process.Pid) != "Z" || procState(other.Process.Pid) != "Z" {
		if time.Now().After(until) {
			t.Fatalf("the ended children are not zombies within 20s: helper %s, other %s",
				procState(helper.Process.Pid), procState(other.Process.Pid))
		}
		time.Sleep(5 * time.Millisecond)
	}
	stay := reapChild(self, "stay")
	stayIn, err := stay.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := stay.Start(); err != nil {
		t.Fatal(err)
	}
	// Closing stdin ends the running child; this stage then waits for it.
	defer func() {
		_ = stayIn.Close()
		_ = stay.Wait()
	}()

	reaped := make(chan error, 1)
	go func() { reaped <- reapEnded(helper.Process.Pid) }()
	select {
	case err := <-reaped:
		if err != nil {
			t.Fatalf("reapEnded: %v", err)
		}
	case <-time.After(reapCallBound):
		t.Fatalf("reapEnded did not return within %v: it blocks on a child that has not ended", reapCallBound)
	}

	if s := procState(stay.Process.Pid); s == "gone" || s == "Z" {
		t.Errorf("the running child is not left running: state %s", s)
	}
	if s := procState(other.Process.Pid); s != "gone" {
		t.Errorf("an ended child that is not the helper is not reaped: state %s", s)
	}
	if s := procState(helper.Process.Pid); s != "Z" {
		t.Errorf("reapEnded reaped the helper: state %s", s)
	}
	var exit *exec.ExitError
	if err := helper.Wait(); !errors.As(err, &exit) || exit.ExitCode() != reapHelperCode {
		t.Errorf("the helper's exit status is lost: Wait gives %v, want exit status %d", err, reapHelperCode)
	}
	if !t.Failed() {
		t.Log(reapChecked)
	}
}

// TestReapEndedLeavesTheHelperAndRunningChildren checks reapEnded directly:
// it reaps an ended child, leaves the ended helper for its exec.Cmd (whose
// Wait then gives the helper's status), and returns at once while another
// child still runs.
func TestReapEndedLeavesTheHelperAndRunningChildren(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestReapEndedTarget$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), reapStageVariable+"=run")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the reap stage did not end within 60s:\n%s", out)
	}
	if err != nil || !strings.Contains(string(out), reapChecked) {
		t.Fatalf("the reap stage failed: %v\n%s", err, out)
	}
}
