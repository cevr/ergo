//go:build linux

package closure

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A process of a run whose parent ends becomes the child of the run's
// reaper (the subreaper). When it ends, it stays a zombie, still a member
// of its process group, until its parent reaps it. A test that waits for
// such a process group to go away (kill(-group, 0) gives ESRCH), as
// scripts/examples does for its fixture's loop, sees it go only when the
// reaper reaps it while the run goes on.

// orphanVariable names the scratch directory where the run's test writes
// "gone" once the group has gone; orphanStageVariable is "run", "parent"
// or "orphan".
const (
	orphanVariable      = "ERGO_CLOSURE_ORPHAN"
	orphanStageVariable = "ERGO_CLOSURE_ORPHAN_STAGE"
	// orphanBound is how long the run's test waits for the group to go.
	orphanBound = 20 * time.Second
)

// TestOrphanRunTarget is the run's test: it runs only as the target of
// TestRunReapsAnOrphanThatEndsDuringTheRun.
func TestOrphanRunTarget(t *testing.T) {
	dir := os.Getenv(orphanVariable)
	if dir == "" {
		t.Skip("the target of TestRunReapsAnOrphanThatEndsDuringTheRun")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(stage string) *exec.Cmd {
		child := exec.Command(self, "-test.run=^TestOrphanRunTarget$", "-test.count=1", escapeProbeArgument)
		child.Env = append(os.Environ(), orphanStageVariable+"="+stage)
		return child
	}
	switch os.Getenv(orphanStageVariable) {
	case "orphan":
		// It ends when its parent has ended: the parent holds the only
		// write end of its stdin.
		io.Copy(io.Discard, os.Stdin)
	case "parent":
		// It starts the orphan in its own process group and ends without
		// waiting for it, so the orphan loses its parent.
		orphan := command("orphan")
		if _, err := orphan.StdinPipe(); err != nil {
			t.Fatal(err)
		}
		if err := orphan.Start(); err != nil {
			t.Fatal(err)
		}
	default:
		// The run's test: the parent leads a process group of its own.
		// The test waits for the parent, then for the group to go: once
		// the orphan has ended and has been reaped, no process is in it.
		parent := command("parent")
		parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		parent.Stderr = os.Stderr
		if err := parent.Run(); err != nil {
			t.Fatalf("the parent: %v", err)
		}
		// The group is the one this test started: its id is the parent's.
		group := parent.Process.Pid
		end := time.Now().Add(orphanBound)
		for {
			err := syscall.Kill(-group, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("the orphan's process group %d did not go within %v (kill(-group, 0): %v): the run keeps an ended orphan as a zombie", group, orphanBound, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(dir, "gone"), []byte("gone\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRunReapsAnOrphanThatEndsDuringTheRun: a process of a run that loses
// its parent and then ends is reaped while the run goes on, so the run's
// test sees its process group go. The probe runs TestOrphanRunTarget under
// the sweep's runner (runProtected).
func TestRunReapsAnOrphanThatEndsDuringTheRun(t *testing.T) {
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
	env := testEnv(f.pkg, orphanVariable+"="+scratchDir, orphanStageVariable+"=run")
	output := filepath.Join(t.TempDir(), "orphan.out")
	if err := runProtected(sandbox{Root: f.root, Scratch: []string{scratchDir}}, "internal/orphan", f.pkg, env, output, self, "-test.run=^TestOrphanRunTarget$", "-test.count=1", "-test.v"); err != nil {
		out, _ := os.ReadFile(output)
		t.Fatalf("the run failed: %v\n%s", err, out)
	}
	if _, err := os.ReadFile(filepath.Join(scratchDir, "gone")); err != nil {
		out, _ := os.ReadFile(output)
		t.Fatalf("the run's test did not see the group go: %v\n%s", err, out)
	}
}
