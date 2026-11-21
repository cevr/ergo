package cases

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// RemoveAll removes a case directory, first making its folders writable, as a
// case may leave a read-only one behind.
func RemoveAll(dir string) {
	if os.RemoveAll(dir) == nil {
		return
	}
	_ = filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(name, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// outcome is what a command that started did. A command that could not start
// has no outcome: execute returns an error instead, so no caller can read an
// exit status 0 from a command that never ran.
type Outcome struct {
	// stdout and stderr hold at most OutputCap bytes each; a stream over the
	// cap is marked, and its bytes are dropped.
	Stdout, Stderr         []byte
	StdoutOver, StderrOver bool
	// status is the exit status, or -1 when a signal ended the process.
	Status   int
	Signal   string
	TimedOut bool
	Elapsed  time.Duration
	// cpu is the user and system time of the process and of the children
	// it waited for (the kernel's wait4 count): the work the case did,
	// which the host's load does not stretch as it stretches elapsed.
	CPU time.Duration
	// limited is a command that failed under a memory limit and whose stderr
	// holds a memory cause read by proclimit.CauseOf.
	Limited bool
}

func (o Outcome) Passed() bool {
	return !o.TimedOut && o.Status == 0 && !o.StdoutOver && !o.StderrOver
}

func (o Outcome) StatusText() string {
	if o.Status < 0 {
		return "signal " + o.Signal
	}
	return strconv.Itoa(o.Status)
}

// why says, after a colon, why a command did not finish, or nothing.
func (o Outcome) Why(timeout time.Duration) string {
	switch {
	case o.TimedOut:
		return fmt.Sprintf(" hung: no end after %s, stopped", timeout)
	case o.Status < 0:
		return " ended by signal " + o.Signal
	}
	return ""
}

// TestHookStarted, when not nil, is called by execute on its own goroutine
// right after a command started, its group was registered and its deferred
// stop was armed. It is nil in production; a test sets it to inject a panic
// (net/http's testHook pattern).
var TestHookStarted func(argv []string)

// StreamDelay bounds how long execute waits, after the last stop of a
// command's group, for its streams to end: a process that left the group
// (setsid) and still holds a stream holds the runner no longer than this, as
// exec.Cmd.WaitDelay bounds it for Go.
const StreamDelay = time.Second

// execute runs argv in dir with exactly env, stdin from the file named (or
// empty input), in its own process group, registered with l. Each of its
// streams goes through a pipe into memory, at most OutputCap bytes, so no
// output reaches the disk; at the first byte past the cap the stream is over
// and execute stops the group at once. It also stops the group when timeout
// (if not 0) passes, when the leader exits (whatever the leader left running
// goes with it), and when a panic unwinds this goroutine: stopGroup is the
// only signal the runner sends, to the group of a child it started.
// It returns an error, and no outcome, when the command could not start: its
// stdin cannot be opened, its pipes cannot be made, the run is stopping, or
// the command cannot be started.
// A memory limit that is not 0 bounds the command and makes it the first
// process the kernel's out-of-memory killer stops (proclimit.Start).
func (l *Live) Execute(argv []string, dir string, env []string, stdin string, timeout time.Duration, memory uint64) (Outcome, error) {
	return l.ExecuteArmed(argv, dir, env, stdin, timeout, memory, "")
}

// ExecuteArmed is execute whose timeout starts at an event, not at the
// start: when arm is not "", the deadline starts once the command's stderr
// holds arm (ergo test -work prints "WORK=" when its build ends), and it
// starts again at each output the command writes after that (a test or
// law's line): so it measures silence, and stops only a run that stopped
// making progress, whatever load does to a run that makes it. The
// build before it has no deadline, as the runner's own builds have none
// (r.tool(0, "build", ...)): a deadline only stops a hung program, and a
// build that load makes slow is not one.
func (l *Live) ExecuteArmed(argv []string, dir string, env []string, stdin string, timeout time.Duration, memory uint64, arm string) (Outcome, error) {
	input, err := os.Open(os.DevNull)
	if stdin != "" {
		input, err = os.Open(stdin)
	}
	if err != nil {
		return Outcome{}, err
	}
	defer input.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return Outcome{}, err
	}
	defer outRead.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		outWrite.Close()
		return Outcome{}, err
	}
	defer errRead.Close()
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = dir
	command.Env = env
	command.Stdin, command.Stdout, command.Stderr = input, outWrite, errWrite
	ownGroup(command)
	started := time.Now()
	err = l.start(command, memory)
	// The command holds its own copies of the write ends. The runner's go
	// now, or no reader would ever see the end of a stream.
	outWrite.Close()
	errWrite.Close()
	if err != nil {
		return Outcome{}, err
	}
	// From here a panic on this goroutine stops the group as it unwinds.
	defer l.release(command.Process)
	stdout, stderr := newCapture(), newCapture()
	if arm != "" {
		stderr.mark, stderr.seen = []byte(arm), make(chan struct{})
		progress := make(chan struct{}, 1)
		stdout.progress, stderr.progress = progress, progress
	}
	overCap := func() { l.stop(command.Process) }
	go stdout.fill(outRead, overCap)
	go stderr.fill(errRead, overCap)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if TestHookStarted != nil {
		TestHookStarted(argv)
	}
	var o Outcome
	var expired <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	startDeadline := func() {
		timer = time.NewTimer(timeout)
		expired = timer.C
	}
	var armed <-chan struct{}
	if timeout > 0 {
		if arm == "" {
			startDeadline()
		} else {
			armed = stderr.seen
		}
	}
run:
	for {
		select {
		case <-done:
			break run
		case <-armed:
			armed = nil
			startDeadline()
		case <-stdout.progress:
			// Output after the build restarts the deadline (silence only).
			if timer != nil {
				timer.Reset(timeout)
			}
		case <-expired:
			o.TimedOut = true
			l.stop(command.Process)
			<-done
			break run
		}
	}
	o.Elapsed = time.Since(started)
	// The last stop: whatever the leader left running in its group goes with
	// it, and no cap stop signals the group after this.
	l.release(command.Process)
	held := time.NewTimer(StreamDelay)
	defer held.Stop()
wait:
	for _, c := range []*capture{stdout, stderr} {
		select {
		case <-c.done:
		case <-held.C:
			break wait
		}
	}
	// A process outside the group that still holds a stream now writes into
	// a closed pipe (EPIPE or SIGPIPE); each fill ends at once.
	outRead.Close()
	errRead.Close()
	<-stdout.done
	<-stderr.done
	o.Stdout, o.StdoutOver = stdout.data, stdout.over
	o.Stderr, o.StderrOver = stderr.data, stderr.over
	o.Status = command.ProcessState.ExitCode()
	o.CPU = command.ProcessState.UserTime() + command.ProcessState.SystemTime()
	if o.Status < 0 {
		o.Signal = signalName(command.ProcessState)
	}
	o.classifyLimit(memory)
	return o, nil
}

// classifyLimit reads the completed command's memory report, if it failed
// under a cap. A failed command includes one stopped by a signal.
func (o *Outcome) classifyLimit(memory uint64) {
	o.Limited = memory > 0 && o.Status != 0 && proclimit.CauseOf(o.Stderr) != proclimit.NoCause
}
