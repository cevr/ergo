//go:build linux

package v1

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestExecSpawnStarter is the starter process of
// TestExecSpawnOutlivesItsStarter: run only with ERGO_SPAWN_STARTER set, it
// spawns a program that ignores SIGTERM, prints its Detached, and exits.
func TestExecSpawnStarter(t *testing.T) {
	if os.Getenv("ERGO_SPAWN_STARTER") == "" {
		t.Skip("the starter of TestExecSpawnOutlivesItsStarter")
	}
	d, err := ExecSpawn(rt.Root("spawn starter"), "sh", []string{"-c", `trap "" TERM; sleep 120`}, nil, "", "", execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("spawned %d %d\n", d.Pid, d.Start)
}

// TestExecSpawnOutlivesItsStarter: a program that Spawn started in a
// process which then exited is still Alive from another process; a
// Detached whose start time is not the program's names no process; and
// Kill ends the program's whole group, a group that ignores SIGTERM
// included (SIGKILL after the grace), after which it is not Alive and a
// second Kill is no failure. Mutants (runtime/v1 ledger): spawn-no-setsid
// (the program stays in the starter's group, so Kill's signal to its own
// group reaches nothing), alive-ignores-start, kill-no-sigkill.
//
// It runs its own test binary (no build) as the starter because a program
// outliving the process that spawned it is what it checks, and no process
// but a second one can exit under it.
func TestExecSpawnOutlivesItsStarter(t *testing.T) {
	starter := exec.Command(os.Args[0], "-test.run=^TestExecSpawnStarter$", "-test.count=1")
	starter.Env = append(os.Environ(), "ERGO_SPAWN_STARTER=1")
	got, err := starter.Output()
	if err != nil {
		t.Fatalf("the starter failed: %v\n%s", err, got)
	}
	var pid int
	var start int64
	for line := range strings.SplitSeq(string(got), "\n") {
		if _, err := fmt.Sscanf(line, "spawned %d %d", &pid, &start); err == nil {
			break
		}
	}
	if pid <= 0 {
		t.Fatalf("the starter printed no Detached:\n%s", got)
	}
	// The program is this test's to end, whatever the outcome: SIGKILL to
	// the pid the starter, a process of this test, started.
	t.Cleanup(func() {
		if execRunning(pid, start) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !ExecAlive(pid, start) {
		t.Fatalf("the spawned program %d is not alive after its starter exited", pid)
	}
	if ExecAlive(pid, start+1) {
		t.Errorf("a Detached with another start time names the program %d", pid)
	}
	ExecKill(rt.Root("spawn kill"), pid, start, 50*time.Millisecond)
	if ExecAlive(pid, start) {
		t.Fatalf("the program %d, which ignores SIGTERM, is alive after Kill", pid)
	}
	if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
		t.Errorf("a process of the group %d is left after Kill (signal 0: %v)", pid, err)
	}
	ExecKill(rt.Root("spawn kill again"), pid, start, 0)
	if ExecAlive(pid, start) {
		t.Errorf("the program %d is alive after a second Kill", pid)
	}
}

// TestExecSpawnRefusesToCaller: a ToCaller sink is Failed, as no caller
// outlives the program to read it. Mutant spawn-tocaller-admitted (the
// ToCaller sink taken as Discard).
func TestExecSpawnRefusesToCaller(t *testing.T) {
	task := rt.Root("spawn refuse")
	_, err := ExecSpawn(task, "true", nil, nil, "", "", execToCaller, "", false, execDiscard, "", false)
	want := ExecFailed{Name: "true", Reason: "stdout: a detached program has no caller to read it (ToCaller)"}
	if err != want {
		t.Fatalf("ExecSpawn with a ToCaller sink failed %#v, want %#v", err, want)
	}
}
