//go:build linux || darwin

package exec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestExecSpawnStarter is the starter process of
// TestExecSpawnOutlivesItsStarter: run only with ERGO_SPAWN_STARTER set, it
// spawns a program that ignores SIGTERM, prints its Detached, and exits.
func TestExecSpawnStarter(t *testing.T) {
	if os.Getenv("ERGO_SPAWN_STARTER") == "" {
		t.Skip("the starter of TestExecSpawnOutlivesItsStarter")
	}
	d, err := ExecSpawn(rt.Root("spawn starter"), "/bin/sh", []string{"-c", `trap "" TERM; sleep 120`}, nil, "", "", execDiscard, "", false, execDiscard, "", false)
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
	// The program leads a process group of its own, the one Kill signals
	// (mutant spawn-no-setsid: since Kill also signals the program itself,
	// only this check sees a program left in its starter's group).
	if group, err := syscall.Getpgid(pid); err != nil || group != pid {
		t.Errorf("the spawned program %d is in process group %d (%v), want its own", pid, group, err)
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
	_, err := ExecSpawn(task, "/usr/bin/true", nil, nil, "", "", execToCaller, "", false, execDiscard, "", false)
	want := ExecFailed{Name: "/usr/bin/true", Reason: "stdout: a detached program has no caller to read it (ToCaller)"}
	if err != want {
		t.Fatalf("ExecSpawn with a ToCaller sink failed %#v, want %#v", err, want)
	}
}

// TestExecSpawnToStdout: a spawned program whose standard error is
// ToStdout writes it to standard output's file, in the order written (a
// burst of lines to the two streams in turn, with no wait); a standard
// output of ToStdout is Failed and starts nothing. Mutants
// g5-spawn-tostdout-ignored (standard error goes to the null device) and
// g5-spawn-stdout-to-itself (the refusal dropped).
func TestExecSpawnToStdout(t *testing.T) {
	task := rt.Root("spawn tostdout")
	out := filepath.Join(t.TempDir(), "out")
	script := `k=0; while [ $k -lt 200 ]; do echo o$k; echo e$k >&2; k=$((k+1)); done`
	var want strings.Builder
	for k := range 200 {
		fmt.Fprintf(&want, "o%d\ne%d\n", k, k)
	}
	d, err := ExecSpawn(task, "/bin/sh", []string{"-c", script}, nil, "", "", execToFile, out, false, execToStdout, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// The program ends by itself; it is this test's to end if it does not.
	t.Cleanup(func() { ExecKill(rt.Root("spawn tostdout kill"), d.Pid, d.Start, 0) })
	testwait.Until(t, func() bool {
		data, _ := os.ReadFile(out)
		if len(data) > want.Len() || !strings.HasPrefix(want.String(), string(data)) {
			t.Fatalf("%s holds %d bytes that are not a prefix of the %d written in order", out, len(data), want.Len())
		}
		return string(data) == want.String()
	})
	_, err = ExecSpawn(task, "/usr/bin/true", nil, nil, "", "", execToStdout, "", false, execDiscard, "", false)
	if err != execToItself("/usr/bin/true") {
		t.Fatalf("ExecSpawn with a standard output of ToStdout failed %#v, want %#v", err, execToItself("/usr/bin/true"))
	}
}

// TestExecStartTimeReadsTheHost: the host gives this process a start time
// that is not 0, a program started later one no earlier, and the reads of
// one process agree. A start time that is always 0 (a layout read at the
// wrong offset) would let Alive and Kill act on a reused pid. Mutant
// darwin-start-zero (the Darwin read gives 0; run on the Mac, see
// mac-bulk-pending.txt) and linux-start-zero.
func TestExecStartTimeReadsTheHost(t *testing.T) {
	self := execStartTime(os.Getpid())
	if self == 0 {
		t.Fatalf("this process %d has no start time", os.Getpid())
	}
	if again := execStartTime(os.Getpid()); again != self {
		t.Errorf("two reads of this process's start time differ: %d, %d", self, again)
	}
	if !execRunning(os.Getpid(), self) {
		t.Errorf("this process is not running at its own start time %d", self)
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if c := execStartTime(child.Process.Pid); c < self {
		t.Errorf("a program started after this process has the start time %d, before this process's %d", c, self)
	}
}

// TestExecSpawnStopped: a stopped caller's Spawn starts nothing and raises
// the stop (lower TestRuntime199BlockingProperty's host-wait law for
// Spawn). Mutant spawn-stopped-starts (no t.Check before the start).
func TestExecSpawnStopped(t *testing.T) {
	stopped := rt.Root("stopped spawner")
	stopped.Interrupt()
	made := t.TempDir() + "/out"
	func() {
		defer func() {
			if q := recover(); q != rt.Interrupted {
				t.Errorf("a stopped Spawn raised %v, want rt.Interrupted", q)
			}
		}()
		_, _ = ExecSpawn(stopped, "/usr/bin/true", nil, nil, "", "", execToFile, made, false, execDiscard, "", false)
	}()
	if _, err := os.Stat(made); err == nil {
		t.Errorf("a stopped Spawn made its file sink")
	}
}

// TestExecKillStopped: a stop of the caller during Kill's grace ends the
// wait and goes on to SIGKILL, so a group that ignores SIGTERM ends
// although the grace is an hour (lower TestRuntime199BlockingProperty's
// host-wait law for Kill). Mutant kill-stop-waits-grace (the stop case
// dropped from the wait). The test waits on Kill's return; the test's
// deadline only names a hung Kill.
func TestExecKillStopped(t *testing.T) {
	// The program says it ignores SIGTERM through a FIFO, and Kill starts
	// only after: a SIGTERM before the trap ended the program, so the
	// mutant passed (I19 r10, a mutant run).
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := ExecSpawn(rt.Root("kill stopped spawn"), "/bin/sh", []string{"-c", `trap "" TERM; echo ready > "$0"; sleep 120`, ready}, nil, "", "", execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if execRunning(d.Pid, d.Start) {
			_ = syscall.Kill(-d.Pid, syscall.SIGKILL)
		}
	})
	said := make(chan error, 1)
	go func() {
		_, err := os.ReadFile(ready) // opens when the program opens its end
		said <- err
	}()
	select {
	case err := <-said:
		if err != nil {
			t.Fatal(err)
		}
	case <-testwait.After(t):
		t.Fatal("the program never said it ignores SIGTERM")
	}
	stopped := rt.Root("stopped killer")
	stopped.Interrupt()
	done := make(chan struct{})
	go func() {
		ExecKill(stopped, d.Pid, d.Start, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-testwait.After(t):
		t.Fatal("Kill with a stopped caller is hung in its hour of grace")
	}
	if ExecAlive(d.Pid, d.Start) {
		t.Errorf("the program %d, which ignores SIGTERM, is alive after a stopped caller's Kill", d.Pid)
	}
}
