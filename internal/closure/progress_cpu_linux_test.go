package closure

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestRunThatComputesSilentlyIsNotQuiet: a run that writes nothing but
// keeps computing is not stopped as quiet (runWithin counts its CPU,
// treeCPU), also when the computing process is a child of the run's
// leader, which waits. The worker computes for more CPU time than the
// quiet window, so it cannot end inside one window: a sweep that judged
// progress by output alone stops it as quiet (the SWEEP-HANG risk: a test
// that computes for its window with no output and no file access). This
// is the rule that lets the sweep drop its fixed per-run bound (3 hours on
// main, after cmd/ergo ran 73-82 minutes and sketchfront 77-101 under load
// 50-80). Mutants: the run's CPU left out of its progress
// (sweep-deadline-cpu-ignored), treeCPU counting the leader only
// (sweep-deadline-cpu-leader-only).
func TestRunThatComputesSilentlyIsNotQuiet(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const quiet = 3 * time.Second
	cmd := exec.Command("sh", "-c", `"$0"; status=$?; exit $status`, self)
	cmd.Env = append(os.Environ(), spinVariable+"="+(quiet+time.Second).String())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := filepath.Join(t.TempDir(), "output")
	outcome, left, err := runWithin(cmd, quiet, time.Time{}, fileProgress(output))
	if outcome != watchExited || err != nil {
		t.Fatalf("a run that computes without output: outcome %d (%v), processes %q; want it to end by itself", outcome, err, left)
	}
}
