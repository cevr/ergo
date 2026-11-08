package v1

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The body of std exec (docs/spec/stdlib.md, exec): ExecRun runs one
// program to its end. It reads and changes the host, so it is not pure.
//
// The stop protocol, which TestExecStopModel holds:
//
//   - The program starts in a process group of its own (Setpgid), so the
//     group holds it and every process it starts that does not leave the
//     group.
//   - Run returns only after the program is reaped and its output is
//     complete: stdout and stderr are read to their end.
//   - A stop of the calling task kills the whole group (SIGKILL to the
//     group the body started), waits until the program is reaped, and then
//     ends the call with rt.Interrupted. So a stop never leaves a process
//     of the group, and the caller's exit actions run after the child is
//     gone. Inside a masked region the task's Done is nil, so the wait is
//     uninterruptible (I2) and the program runs to its end.
//   - When the program exits, Run kills what is left of its group. In
//     either case Run then waits until the host has ended and reaped each
//     process of the group (execEndGroup, at most execReapWait), so a call
//     leaves no process of the group, not even a zombie. When a process
//     left in the group holds stdout or stderr open, Run reads the output
//     for at most execWaitDelay after the exit, then closes it and returns
//     the program's status with the output read so far. A program that
//     must outlive the call leaves the group itself (setsid).

// execWaitDelay is how long Run waits, after the program exits, for a
// process it left behind to close the program's output.
const execWaitDelay = 500 * time.Millisecond

// ExecNotFound is exec.NotFound: no program Name is on the PATH, or no file
// is at the path Name.
type ExecNotFound struct{ Name string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (e ExecNotFound) Error() string { return "exec.NotFound{Name:" + e.Name + "}" }

// ExecDenied is exec.Denied: the process may not run the file Name.
type ExecDenied struct{ Name string }

// Error is the failure's text, as ExecNotFound's is.
func (e ExecDenied) Error() string { return "exec.Denied{Name:" + e.Name + "}" }

// ExecFailed is exec.Failed: the program Name could not start for another
// reason, such as a working directory that does not exist. Reason is the
// host's text for it.
type ExecFailed struct {
	Name   string
	Reason string
}

// Error is the failure's text, as ExecNotFound's is.
func (e ExecFailed) Error() string { return "exec.Failed{Name:" + e.Name + " Reason:" + e.Reason + "}" }

// ExecResult is exec.Result: the exit status of a program that ran, and
// all it wrote to stdout and stderr. A status that is not 0 is a result,
// not a failure. A program that a signal ended has status 128 plus the
// signal's number, as a shell reports it.
type ExecResult struct {
	Status int
	Stdout string
	Stderr string
}

// ExecRun is the body of exec.Host's Run on the caller's task t: it runs
// name with args in dir ("" is the caller's working directory), with the
// host process's environment and env's entries over it, and with stdin as
// its standard input.
func ExecRun(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string) (ExecResult, error) {
	t.Check()
	if name == "" {
		return ExecResult{}, ExecNotFound{Name: name} // no program has the empty name
	}
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			reason := "not a directory"
			if err != nil {
				reason = fsReason(err)
			}
			return ExecResult{}, ExecFailed{Name: name, Reason: "dir " + dir + ": " + reason}
		}
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		cmd.Env = os.Environ()
		for _, k := range keys {
			cmd.Env = append(cmd.Env, k+"="+env[k]) // a later entry wins (os/exec)
		}
	}
	if stdin != "" {
		// An empty input is the null device, which reads end of file at
		// once: os/exec then needs no pipe and no copying goroutine.
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = execWaitDelay
	if err := cmd.Start(); err != nil {
		return ExecResult{}, execErr(name, err)
	}
	group := cmd.Process.Pid // the group's id: Setpgid made the program its leader
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-t.Done():
		// Each process of the group is one the body started; the group
		// exists while one of them is unreaped.
		_ = syscall.Kill(-group, syscall.SIGKILL)
		<-done
		execEndGroup(group)
		panic(rt.Interrupted)
	}
	_ = err // the status is in cmd.ProcessState; ErrWaitDelay and *ExitError carry nothing more
	// The program exited and is reaped. A process it left in its group
	// still holds the group's id, so the group can be no other's while one
	// is left; when none is left, the kill finds no group (ESRCH), unless,
	// in the instant since the reap, the kernel gave the program's pid to
	// a new process that made itself a group leader (guess: unreachable in
	// practice, since pids are reused only after the counter wraps).
	execEndGroup(group)
	return ExecResult{Status: execStatus(cmd.ProcessState), Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// execReapWait bounds how long Run waits, after it kills the group, for the
// host to end and reap the processes that were left in it.
const execReapWait = 500 * time.Millisecond

// execEndGroup kills each process left in the group and waits until the
// group is empty. SIGKILL only marks a process: the kernel ends it later,
// and its new parent (the host's reaper, as the program that started it
// is reaped) then reaps it. A process exists, and keeps the group's id,
// until it is reaped, so kill(-group, 0) fails with ESRCH exactly when no
// process of the group is left, zombies included (measured on Linux: a
// SIGKILLed grandchild was still in the run state 22µs after a kill that
// did not wait). The wait is bounded by execReapWait for a host whose
// reaper is slow or a process the kernel cannot end at once (one in an
// uninterruptible wait); each such process has SIGKILL pending and runs
// no code of its own again. When the group is already empty, the first
// kill fails with ESRCH and the call does not wait.
func execEndGroup(group int) {
	if syscall.Kill(-group, syscall.SIGKILL) != nil {
		return // ESRCH: no process of the group is left
	}
	deadline := time.Now().Add(execReapWait)
	for pause := 20 * time.Microsecond; syscall.Kill(-group, 0) == nil && time.Now().Before(deadline); pause = min(2*pause, 2*time.Millisecond) {
		time.Sleep(pause)
	}
}

// execStatus is the exit status of a program that ended: its exit code, or
// 128 plus the signal that ended it.
func execStatus(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// execErr is the std exec failure of a program name that did not start:
// NotFound for a name not on the PATH or a path to no file, Denied for a
// file the process may not run, and Failed with the host's reason
// otherwise.
func execErr(name string, err error) error {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return ExecNotFound{Name: name}
	case errors.Is(err, fs.ErrPermission):
		return ExecDenied{Name: name}
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return ExecFailed{Name: name, Reason: pe.Err.Error()}
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return ExecFailed{Name: name, Reason: ee.Err.Error()}
	}
	return ExecFailed{Name: name, Reason: err.Error() + " (" + strconv.Quote(name) + ")"}
}
