//go:build linux

package proclimit

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestBoundHoldsFromTheFirstInstruction: a program is bounded before it
// runs, however late its caller is scheduled after starting it. The hook
// holds the caller until the program has either ended or stopped, the
// window a loaded host gives it (the merge tier's
// TestRunProgramBoundsAGrowingProgram ran unbounded there, batch-12
// integration round 7). The probe is sh printing its RLIMIT_DATA in KiB,
// read first thing; a child process is needed, since the limit is a kernel
// property of the process Start starts. Mutant: start the program with a
// plain cmd.Start and set the bound after the hook (the old order); sh then
// ends first, prints "unlimited", and Run returns no error.
func TestBoundHoldsFromTheFirstInstruction(t *testing.T) {
	beforeBound = func(pid int) {
		if !testwait.Until(t, func() bool { return stoppedOrEnded(pid) }) {
			t.Errorf("program %d neither stopped nor ended by the test's deadline", pid)
		}
	}
	t.Cleanup(func() { beforeBound = nil })
	cmd := exec.Command("sh", "-c", "ulimit -d")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := Run(cmd, 256<<20); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "262144" {
		t.Fatalf("the program's RLIMIT_DATA is %q KiB; want 262144 (256 MiB) from its first instruction", got)
	}
}

// TestStartReturnsUnderSignals: a signal that reaches a program while
// Start starts it never holds Start. The programs are started into a
// process group of the test's own (a cat it leads), which a goroutine
// floods with SIGWINCH, a signal every program here ignores; a start that
// a signal could stop before its exec stops in that window within a few
// hundred starts. The wait is the test's deadline, an event, not a fixed
// time. Mutant: start the program traced (611ad7a3, batch-12 verifier M3):
// a signal between PTRACE_TRACEME and the exec stops the child where only
// the thread blocked in cmd.Start could resume it, and Start never returns.
func TestStartReturnsUnderSignals(t *testing.T) {
	leader := exec.Command("cat")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	hold, err := leader.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	group := leader.Process.Pid
	stop := make(chan struct{})
	stormed := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		<-stormed
		_ = syscall.Kill(-group, syscall.SIGKILL)
		_ = hold.Close()
		_ = leader.Wait()
	})
	go func() {
		defer close(stormed)
		for {
			select {
			case <-stop:
				return
			default:
				_ = syscall.Kill(-group, syscall.SIGWINCH)
				runtime.Gosched()
			}
		}
	}()
	const starts = 1000
	done := make(chan error, 1)
	started := atomic.Int64{}
	go func() {
		for range starts {
			cmd := exec.Command("true")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
			if err := Run(cmd, DefaultLimit); err != nil {
				done <- err
				return
			}
			started.Add(1)
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start %d under the signal stream: %v", started.Load()+1, err)
		}
	case <-testwait.After(t):
		t.Fatalf("Start did not return at the test's deadline, after %d of %d starts under the signal stream", started.Load(), starts)
	}
}

// TestStartReportsAProgramThatCannotRun: the bound helper reports a
// program it cannot exec, so Start fails as cmd.Start would, with the
// program's path, and leaves no process. Mutant: start ignores the
// helper's report (Start returns nil, Wait then sees exit 127).
func TestStartReportsAProgramThatCannotRun(t *testing.T) {
	const missing = "/nonexistent/loop-probe-x"
	cmd := exec.Command(missing)
	err := Start(cmd, DefaultLimit)
	if err == nil || !errors.Is(err, syscall.ENOENT) || !strings.Contains(err.Error(), "fork/exec "+missing) {
		t.Fatalf("Start of a missing program: %v; want fork/exec %s: ENOENT", err, missing)
	}
}

// stoppedOrEnded reports whether process pid is stopped (traced or not),
// a zombie, or gone. It reads /proc with system calls, not package os, so
// the file is no input of Go's test cache.
func stoppedOrEnded(pid int) bool {
	fd, err := syscall.Open("/proc/"+strconv.Itoa(pid)+"/stat", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return true
	}
	defer syscall.Close(fd)
	stat := make([]byte, 4<<10)
	n, err := syscall.Read(fd, stat)
	if err != nil {
		return true
	}
	// The state follows the command name's closing parenthesis.
	stat = stat[:n]
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return true
	}
	return strings.IndexByte("tTZX", stat[end+2]) >= 0
}
