package exec

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
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
//   - When the program exits, Run kills what is left of its group.
//   - Each kill comes before the reap: Run waits for the program's end
//     without reaping it (execExit, waitid WNOWAIT on Linux, a kqueue
//     NOTE_EXIT on Darwin), kills the group while the unreaped program
//     still holds the group's id, and reaps it after. So the body signals
//     only a group it started and still holds (worker protocol Signals).
//   - In either case Run then waits until the host has ended and reaped
//     each process of the group (execAwaitGroupEnd, at most execReapWait), so a call
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
// name with args in dir ("" is the caller's working directory), with exactly
// env and with stdin as its standard input. A stop of t sends SIGTERM to the group and waits
// grace for the program's end before the SIGKILL; grace 0 sends SIGKILL at
// once.
//
//ergo:host
//ergo:wait caller
func ExecRun(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration) (ExecResult, error) {
	return execRun(t, "", name, args, env, dir, stdin, grace)
}

// ExecRunIn is ExecRun for a command of exec.NewTestHost's folder root
// (exec confined.ergo checked its paths): on Linux the program runs in the
// folder's sandbox, where the folder is the only path it can write
// (exec_sandbox_linux.go); elsewhere it runs as ExecRun's does.
//
//ergo:host
//ergo:wait caller
func ExecRunIn(t *rt.Task, root string, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration) (ExecResult, error) {
	return execRun(t, root, name, args, env, dir, stdin, grace)
}

// execRun is ExecRun's body, in the sandbox of the folder root when root is
// not "".
func execRun(t *rt.Task, root string, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration) (ExecResult, error) {
	t.Check()
	cmd, err := ExecCommand(name, args, env, dir, stdin)
	if err != nil {
		return ExecResult{}, err
	}
	cmd.SysProcAttr = execGroupAttr()
	// Run owns the output pipes, and not os/exec, so that it can read the
	// output to its end while the program is still unreaped (below).
	outR, outW, err := os.Pipe()
	if err != nil {
		return ExecResult{}, execErr(name, err)
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return ExecResult{}, execErr(name, err)
	}
	defer errR.Close()
	exit, err := execWatchExit()
	if err != nil {
		outW.Close()
		errW.Close()
		return ExecResult{}, execErr(name, err)
	}
	defer exit.close()
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.WaitDelay = execWaitDelay // bounds the copy of stdin to a process that holds it open and does not read
	child, err := execBegin(cmd, root)
	outW.Close() // the program holds its own copies
	errW.Close()
	if err != nil {
		return ExecResult{}, execStartErr(name, err)
	}
	group := child.pid // the group's id: Setpgid made the program its leader
	var stdout, stderr bytes.Buffer
	read := make(chan struct{}, 2)
	go func() { _, _ = stdout.ReadFrom(outR); read <- struct{}{} }()
	go func() { _, _ = stderr.ReadFrom(errR); read <- struct{}{} }()
	exited := make(chan struct{})
	go func() { exit.await(group); close(exited) }()
	// Nothing reaps the program before cmd.Wait below. Until then it is a
	// process or a zombie that holds its pid and so the group's id: no
	// other group can have that id. So each kill below reaches only the
	// group the body started (worker protocol Signals).
	stopped, reads := false, 0
	select {
	case <-exited:
		// The program ended. Read the output until a process left in the
		// group closes it, for at most execWaitDelay, or until a stop.
		wait := time.NewTimer(execWaitDelay)
	output:
		for reads < 2 {
			select {
			case <-read:
				reads++
			case <-wait.C:
				break output
			case <-t.Done():
				stopped = true
				break output
			}
		}
		wait.Stop()
		execKillGroup(group)
	case <-t.Done():
		stopped = true
		if grace > 0 {
			// The command's grace (exec.WithGrace): SIGTERM first, and
			// SIGKILL only when the program has not ended within it. The
			// wait is uninterruptible: the stop is what it serves.
			execSignalGroup(group, syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(grace):
			}
		}
		execKillGroup(group)
		<-exited
		// The kill ended each holder of the output in the group; one that
		// left the group may hold it for at most execWaitDelay.
		wait := time.NewTimer(execWaitDelay)
	held:
		for reads < 2 {
			select {
			case <-read:
				reads++
			case <-wait.C:
				break held
			}
		}
		wait.Stop()
	}
	if reads < 2 {
		// A process outside the group, or one the kill has not ended yet,
		// holds the output: close it, as os/exec's WaitDelay does, and keep
		// what was read.
		outR.Close()
		errR.Close()
		for ; reads < 2; reads++ {
			<-read
		}
	}
	status := child.reap()
	execAwaitGroupEnd(group)
	if stopped {
		panic(rt.Interrupted)
	}
	return ExecResult{Status: status, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// execChild is a started program, the leader of its own group and a child
// of this process: one os/exec started (cmd), or one the case's sandbox
// helper forked for this process (proc; exec_sandbox_linux.go).
type execChild struct {
	pid       int
	cmd       *exec.Cmd
	proc      *os.Process
	stdin     *os.File // the sandboxed start's copy of stdin goes into it
	stdinOnce sync.Once
}

// execBegin starts cmd: in the sandbox of the test's folder root when root
// is not "" (exec.NewTestHost's commands), on the host otherwise.
func execBegin(cmd *exec.Cmd, root string) (*execChild, error) {
	if root != "" {
		return execSandboxStart(cmd, root)
	}
	return execHostStart(cmd)
}

// execHostStart starts cmd with os/exec.
func execHostStart(cmd *exec.Cmd) (*execChild, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{pid: cmd.Process.Pid, cmd: cmd}, nil
}

// reap waits for the program, which has ended or been killed, and returns
// its status (execStatus).
func (c *execChild) reap() int {
	if c.cmd != nil {
		_ = c.cmd.Wait() // the status is in cmd.ProcessState, and an ExitError carries nothing more
		return execStatus(c.cmd.ProcessState)
	}
	state, err := c.proc.Wait()
	c.closeStdin() // as os/exec's WaitDelay does for a stdin no one reads
	if err != nil {
		panic("exec: reaping the sandboxed program " + strconv.Itoa(c.pid) + ": " + err.Error())
	}
	return execStatus(state)
}

func (c *execChild) closeStdin() {
	if c.stdin != nil {
		c.stdinOnce.Do(func() { _ = c.stdin.Close() })
	}
}

// execStartErr is the std exec failure of a start that failed: the
// sandbox's own failure is exec.Failed with its reason, so a command of
// the test's folder runs nowhere rather than unconfined; any other is
// execErr's.
func execStartErr(name string, err error) error {
	var se execSandboxError
	if errors.As(err, &se) {
		return ExecFailed{Name: name, Reason: sandboxReason(se.err)}
	}
	return execErr(name, err)
}

// execKillHook, when set, runs just before Run kills the group, with the
// group's id. Only a test sets it: TestExecStopModel checks that the
// program is still unreaped at that point.
var execKillHook func(group int)

// execKillGroup sends SIGKILL to each process of the group. The caller has
// not reaped the group's leader, so the group is the one the body started.
func execKillGroup(group int) {
	if execKillHook != nil {
		execKillHook(group)
	}
	_ = execKill(-group, syscall.SIGKILL)
}

// execReapWait bounds how long Run waits, after it kills the group, for the
// host to end and reap the processes that were left in it.
const execReapWait = 500 * time.Millisecond

// execAwaitGroupEnd waits, after the kill and the reap of the program,
// until no process of the group is left. SIGKILL only marks a process: the
// kernel ends it later, and its new parent (the host's reaper, as the
// program that started it has ended) then reaps it. A process exists, and
// keeps the group's id, until it is reaped, so kill(-group, 0) fails with
// ESRCH exactly when no process of the group is left, zombies included
// (measured on Linux: a SIGKILLed grandchild was still in the run state
// 22µs after a kill that did not wait). Signal 0 sends no signal (POSIX:
// it only checks that the group exists), so this probe reaches no process,
// also when the group is gone. The wait is bounded by execReapWait for a
// host whose reaper is slow or a process the kernel cannot end at once
// (one in an uninterruptible wait); each such process has SIGKILL pending
// and runs no code of its own again. When the group is already empty, the
// first probe fails with ESRCH and the call does not wait.
func execAwaitGroupEnd(group int) {
	deadline := time.Now().Add(execReapWait)
	for pause := 20 * time.Microsecond; execKill(-group, 0) == nil && time.Now().Before(deadline); pause = min(2*pause, 2*time.Millisecond) {
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

// ExecCommand is the command of name with args for Run, Start and Spawn:
// in dir ("" is the caller's working directory), with exactly env's
// entries as its environment, in the order of their keys (std exec: the
// environment is a value; nothing of this process's environment reaches
// the program unless the caller put it in env), and stdin as its standard
// input. A name with no slash is looked up on env's PATH, never on this
// process's (ExecLookPath). It fails NotFound for the empty name, which no
// program has, and for a name no directory of env's PATH holds, and Failed
// for a dir that is not a directory. The caller sets SysProcAttr.
//
// It is exported for the runtime's own Go callers that start a child (a
// task runner's child start): the contract above is theirs too, so a child
// any of them starts sees exactly env and is found on env's PATH.
func ExecCommand(name string, args []string, env map[string]string, dir string, stdin string) (*exec.Cmd, error) {
	if name == "" {
		return nil, ExecNotFound{Name: name}
	}
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			reason := "not a directory"
			if err != nil {
				reason = fsReason(err)
			}
			return nil, ExecFailed{Name: name, Reason: "dir " + dir + ": " + reason}
		}
	}
	path, err := ExecLookPath(name, env["PATH"])
	if err != nil {
		return nil, err
	}
	// A Cmd built by hand, not exec.Command, which would look name up on
	// this process's PATH.
	cmd := &exec.Cmd{Path: path, Args: append([]string{name}, args...), Dir: dir}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	// Never nil: os/exec gives a program with a nil Env this process's
	// whole environment.
	cmd.Env = make([]string, 0, len(keys))
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	if stdin != "" {
		// An empty input is the null device, which reads end of file at
		// once: os/exec then needs no pipe and no copying goroutine.
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd, nil
}

// ExecLookPath is the file that runs for name: name itself when it holds a
// slash, else the first regular file named name that the process may
// run in a directory of path, a list as PATH writes it (a colon between
// two directories). A directory that is not absolute is passed over, as Go's
// LookPath refuses a relative result (exec.ErrDot), so an empty entry or
// "." never runs a file of the working directory. A name that holds no
// slash and no such file is NotFound; one found only as a file the process
// may not run is Denied.
func ExecLookPath(name string, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	denied := false
	for _, dir := range strings.Split(path, ":") {
		if !strings.HasPrefix(dir, "/") {
			continue
		}
		file := dir + "/" + name
		fi, err := os.Stat(file)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if syscall.Access(file, 0x1) != nil { // X_OK
			denied = true
			continue
		}
		return file, nil
	}
	if denied {
		return "", ExecDenied{Name: name}
	}
	return "", ExecNotFound{Name: name}
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
