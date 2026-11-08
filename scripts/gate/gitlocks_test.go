package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStepsTakeNoOptionalLocks is the guard of b9-fix-gate round 3: a step
// runs with GIT_OPTIONAL_LOCKS=0 whatever the caller sets, so a `git status`
// in a step (or the go command's VCS stamp, which runs it) does not write
// the index of a checkout whose .git is a directory inside it, as in the
// merge tier's export. At c863b89b such a write in one step failed the
// closure sweeps of five others ("changed during .git/index.lock").
func TestStepsTakeNoOptionalLocks(t *testing.T) {
	t.Setenv(gitOptionalLocks, "1")
	index := func(root string) os.FileInfo {
		t.Helper()
		info, err := os.Stat(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}

	// The control: on this host a plain git status in a fresh fixture
	// replaces its index, so the check below can see a write.
	control := fixture(t)
	before := index(control)
	status := exec.Command("git", "--no-optional-locks", "status", "--porcelain")
	status.Dir = control
	status.Env = append(os.Environ(), gitOptionalLocks+"=1")
	if out, err := status.CombinedOutput(); err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if !os.SameFile(before, index(control)) {
		t.Fatal("git status --no-optional-locks replaced the index; the control is wrong")
	}
	locking := exec.Command("sh", "-c", "git status --porcelain")
	locking.Dir = control
	locking.Env = append(os.Environ(), gitOptionalLocks+"=1")
	if out, err := locking.CombinedOutput(); err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if os.SameFile(before, index(control)) {
		t.Skip("git status with optional locks left a fresh index in place on this host, so no write can be seen")
	}

	root := fixture(t)
	before = index(root)
	steps := []*Step{{Name: "git status", Argv: []string{"sh", "-c", `echo "locks=$GIT_OPTIONAL_LOCKS"; git status --porcelain`}}}
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 || !strings.Contains(out, "locks=0\n") {
		t.Fatalf("exit %d; the step did not run with %s=0:\n%s", code, gitOptionalLocks, out)
	}
	if !os.SameFile(before, index(root)) {
		t.Fatalf("a git status in a step replaced the checkout's index:\n%s", out)
	}
	for _, environment := range [][]string{stepEnvironment(), keyedEnvironment(), toolQueryEnvironment()} {
		if value, _ := environmentVariable(environment, gitOptionalLocks); value != "0" {
			t.Errorf("an environment the gate gives a step or a tool holds %s=%q, want 0", gitOptionalLocks, value)
		}
	}
}
