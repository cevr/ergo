//go:build linux

package opcount_test

import (
	"os"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// count runs this test binary as the child program mode under Count.
func count(t *testing.T, mode string) opcount.Run {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "OPCOUNT_TEST_CHILD="+mode, "GOMAXPROCS=1")
	r, err := opcount.Count(cmd, 1<<30)
	if err != nil {
		t.Fatalf("%s: %v", mode, err)
	}
	return r
}

// whole is the run's count, or the test fails with its refusal.
func whole(t *testing.T, mode string) float64 {
	t.Helper()
	r := count(t, mode)
	if id, why := r.Refusal(); id != "" {
		t.Fatalf("%s: refused %s: %s", mode, id, why)
	}
	return float64(r.Instructions)
}

// Count counts the program's whole process tree from its exec: k more
// steps cost the same in the program and in a child it waits for. A known
// answer on real processes, because the claim is about the kernel's
// counter of a real process tree, which no in-process check shows; it
// builds no Go (the children are this test binary). Each child is bounded
// by proclimit and the test waits for its exit. A host with no counter
// skips it and says why.
//
// Mutants: count-no-inherit and count-not-enabled-at-exec (the program is
// not counted: NoCounter).
func TestCountsTheProcessTree(t *testing.T) {
	if err := opcount.Available(); err != nil {
		t.Skipf("no counter: %v", err)
	}
	const k = 20_000_000
	one, two := whole(t, "loop:1000000"), whole(t, "loop:21000000")
	steps := two - one // k steps
	if steps < k {
		t.Fatalf("%v instructions for %d more steps: fewer than one per step", steps, k)
	}
	// The child waits for a grandchild of k steps, then runs k steps.
	spawn := whole(t, "spawn:20000000")
	if grand := spawn - two; grand < 0.9*steps {
		t.Errorf("a child of %d steps counted %v instructions; %d steps alone count %v: the grandchild is not counted", k, grand, k, steps)
	}
}

// A run that does not finish has no number: a nonzero exit, and a process
// left in the program's group (which Count kills), are RunIncomplete,
// whatever the counter read. Real processes, because the claim is about
// exit statuses and process groups; this test binary is the child.
//
// Mutants: count-exit-ignored, count-group-left-admitted.
func TestRefusesARunThatDidNotFinish(t *testing.T) {
	for _, mode := range []string{"exit:3", "orphan"} {
		if id, why := count(t, mode).Refusal(); id != opcount.RunIncomplete {
			t.Errorf("%s: refusal %q (%s), want RunIncomplete", mode, id, why)
		}
	}
	if id, why := count(t, "loop:1000").Refusal(); id == opcount.RunIncomplete {
		t.Errorf("a run that exits 0 and leaves nothing: RunIncomplete (%s)", why)
	}
}
