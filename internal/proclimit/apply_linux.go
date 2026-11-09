//go:build linux

package proclimit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// beforeBound, when set (a test), runs after the program is started and
// before its bound is set, with the program's pid: the window in which a
// loaded host may deschedule the caller.
var beforeBound func(pid int)

// start starts cmd stopped at its exec, sets its oom_score_adj and
// RLIMIT_DATA there, and only then lets it run, so the program's first
// instruction already runs under both. The program is started traced
// (SysProcAttr.Ptrace: the child calls ptrace(PTRACE_TRACEME) before its
// exec), and a traced process stops before the first instruction of the
// program it execs; the tracer, the thread that started it, sets the bound
// and detaches. RLIMIT_DATA (Linux 4.7 on) counts every private writable
// mapping, which is where the Go runtime maps its heap and goroutine
// stacks; its address space reservations are PROT_NONE and do not count,
// so the limit is close to the heap a program may hold.
//
// Setting the bound after a plain cmd.Start raced the program: one that
// ran to its end before the caller was scheduled again (a 5 ms delay is
// enough for a program that reserves 1 GiB) ran unbounded, and the bound
// was skipped with no error (batch-12 integration round 7: the merge
// tier's TestRunProgramBoundsAGrowingProgram). A stopped program cannot
// end first, so a bound that cannot be set is always an error.
//
// A host that forbids a process to trace its own child (Yama's
// ptrace_scope 3, a seccomp filter that refuses ptrace, or a caller that a
// tracer such as strace -f follows) fails here, with the error: a program
// never runs without its bound.
func start(cmd *exec.Cmd, limit uint64) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.Ptrace {
		return errors.New("SysProcAttr.Ptrace is set: proclimit traces the program itself to bound it")
	}
	// Every ptrace request comes from the thread that started the child,
	// its tracer, until the detach.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd.SysProcAttr.Ptrace = true
	err := cmd.Start()
	cmd.SysProcAttr.Ptrace = false
	if err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if beforeBound != nil {
		beforeBound(pid)
	}
	var status syscall.WaitStatus
	for {
		_, err = syscall.Wait4(pid, &status, syscall.WALL, nil)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("wait for the program's stop at its exec: %w", err)
	}
	if !status.Stopped() {
		// Only a signal from outside (SIGKILL) ends a stopped program, and
		// the wait has reaped it.
		return fmt.Errorf("the program ended before it ran (%v)", status)
	}
	if err := set(pid, limit); err != nil {
		return err
	}
	// The first stop is the exec's SIGTRAP, which the detach drops; a
	// signal that came before it is passed on.
	signal := 0
	if s := status.StopSignal(); s != syscall.SIGTRAP {
		signal = int(s)
	}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PTRACE, syscall.PTRACE_DETACH, uintptr(pid), 0, uintptr(signal), 0, 0); errno != 0 {
		return fmt.Errorf("let the bounded program run (ptrace detach): %w", errno)
	}
	return nil
}

func set(pid int, limit uint64) error {
	// A read-only /proc (a sandbox that mounts everything read-only, as the
	// closure sweep does) leaves the score as it is; the memory limit,
	// which needs no /proc, still holds.
	if err := writeProc("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", strconv.Itoa(OOMScore)); err != nil && !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("raise oom_score_adj: %w", err)
	}
	if limit == 0 {
		return nil
	}
	rl := syscall.Rlimit{Cur: limit, Max: limit}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), uintptr(syscall.RLIMIT_DATA),
		uintptr(unsafe.Pointer(&rl)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("prlimit(RLIMIT_DATA, %s): %w", Format(limit), errno)
	}
	return nil
}

// writeProc writes a /proc file with system calls, not package os, whose
// test log would make the file an input of Go's test cache: a /proc file
// is new on each run, so the result of a test that runs a program would
// never be cached.
func writeProc(path, value string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(value)); err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	return nil
}
