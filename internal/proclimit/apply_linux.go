//go:build linux

package proclimit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// boundMarker is argv[1] of the bound helper: the binary that called Start,
// run again as /proc/self/exe, which bounds itself and execs the program.
const boundMarker = "-ergo-proclimit-bound-exec"

// beforeBound, when set (a test), runs after the helper is started and
// before Start reads its report, with its pid: the window in which a
// loaded host may deschedule the caller.
var beforeBound func(pid int)

// The helper's report stages: the one that failed, then its errno.
const (
	stageScore = 's'
	stageLimit = 'l'
	stageExec  = 'e'
)

// init is the bound helper. Every binary that calls Start links this
// package, so /proc/self/exe reaches it in each: ergo, scripts/examples and
// every test binary. It runs before main and TestMain; a binary not started
// as the helper only compares one argument.
func init() {
	if len(os.Args) > 5 && os.Args[1] == boundMarker {
		boundExec(os.Args[2], os.Args[3], os.Args[4], os.Args[5:])
	}
}

// start starts cmd through the bound helper: the binary of the caller,
// re-run with the bound, the report pipe's descriptor and the program as
// arguments. The helper raises its own oom_score_adj and sets its own
// RLIMIT_DATA (setrlimit), then execs the program, which keeps both
// across the exec: the program's first instruction runs under them,
// however late the caller is scheduled. RLIMIT_DATA (Linux 4.7 on) counts
// every private writable mapping, which is where the Go runtime maps its
// heap and goroutine stacks; its address space reservations are PROT_NONE
// and do not count, so the limit is close to the heap a program may hold.
// The helper reports a step that failed on the pipe, which its exec closes
// (close-on-exec); Start returns once the program runs, or with the error.
//
// History (batch-12 integration): fu-oom set the bound after a plain
// cmd.Start, so a program that ended first ran unbounded with no error
// (round 7). Round 7 then started the program traced and bounded it at its
// exec stop, but a signal between PTRACE_TRACEME and the exec stopped the
// child where only the thread blocked in cmd.Start could resume it, and
// Start hung (verifier M3). The helper needs neither the race nor ptrace:
// it is util-linux's `prlimit CMD` and Docker's reexec, with the program's
// pid, process group and descriptors exactly as cmd asks.
func start(cmd *exec.Cmd, limit uint64) error {
	if cmd.Err != nil {
		return cmd.Err
	}
	report, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer report.Close()
	path, args, extra := cmd.Path, cmd.Args, cmd.ExtraFiles
	fd := 3 + len(extra)
	cmd.Path = "/proc/self/exe"
	cmd.Args = append([]string{args[0], boundMarker, strconv.FormatUint(limit, 10), strconv.Itoa(fd), path}, args...)
	cmd.ExtraFiles = append(extra[:len(extra):len(extra)], write)
	err = cmd.Start()
	cmd.Path, cmd.Args, cmd.ExtraFiles = path, args, extra
	write.Close()
	if err != nil {
		return err
	}
	if beforeBound != nil {
		beforeBound(cmd.Process.Pid)
	}
	var failed [5]byte
	n, err := io.ReadFull(report, failed[:])
	switch {
	case n == 0 && err == io.EOF:
		// The exec closed the pipe: the program runs, bounded.
		return nil
	case err != nil:
		return fmt.Errorf("read the bound helper's report: %w", err)
	}
	errno := syscall.Errno(binary.LittleEndian.Uint32(failed[1:]))
	switch failed[0] {
	case stageScore:
		return fmt.Errorf("raise oom_score_adj: %w", errno)
	case stageLimit:
		return fmt.Errorf("setrlimit(RLIMIT_DATA, %s): %w", Format(limit), errno)
	case stageExec:
		return &os.PathError{Op: "fork/exec", Path: path, Err: errno}
	}
	return fmt.Errorf("the bound helper reported %q", failed[:])
}

// boundExec is the helper's body: raise the score, set the limit, exec the
// program, and report the step that fails on fd, then exit. It never
// returns.
func boundExec(limitArg, fdArg, path string, argv []string) {
	fd, err := strconv.Atoi(fdArg)
	if err != nil {
		os.Exit(127)
	}
	syscall.CloseOnExec(fd)
	fail := func(stage byte, err error) {
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			errno = syscall.EINVAL
		}
		var b [5]byte
		b[0] = stage
		binary.LittleEndian.PutUint32(b[1:], uint32(errno))
		_, _ = syscall.Write(fd, b[:])
		os.Exit(127)
	}
	limit, err := strconv.ParseUint(limitArg, 10, 64)
	if err != nil {
		fail(stageLimit, syscall.EINVAL)
	}
	// A read-only /proc (a sandbox that mounts everything read-only, as the
	// closure sweep does) leaves the score as it is; the memory limit,
	// which needs no /proc, still holds.
	if err := writeProc("/proc/self/oom_score_adj", strconv.Itoa(OOMScore)); err != nil && !errors.Is(err, syscall.EROFS) {
		fail(stageScore, err)
	}
	if limit != 0 {
		if err := syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
			fail(stageLimit, err)
		}
	}
	fail(stageExec, syscall.Exec(path, argv, os.Environ()))
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
