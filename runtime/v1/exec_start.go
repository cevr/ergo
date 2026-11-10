package v1

import (
	"bufio"
	"io"
	"iter"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The body of std exec's Start (docs/spec/stdlib.md, exec 9 to 12): a
// program that runs while the caller reads its output. Start gives a
// Running, a resource: its release (Close) ends the program and reaps it.
//
// The lifecycle, which TestRunningMatchesModel holds against a model over
// a fake process (execProc):
//
//   - Running: the program runs. Lines gives the lines of the streams
//     whose sink is ToCaller, in the order they come; Wait waits for the
//     program's end.
//   - Ending (the release, before the program ended): SIGTERM to the group
//     when the command's Grace is above 0, then, when the program has not
//     ended within Grace, SIGKILL to the group; then the wait for the end.
//     Grace 0 sends SIGKILL at once, as Run's stop does.
//   - Exited: the program ended (not reaped yet). finish reads the output
//     to its end for at most execWaitDelay, kills what is left of the group
//     (SIGKILL), closes the output a process outside the group still holds,
//     reaps the program and waits until the group is empty (as Run does).
//   - Done: the status is known; Wait gives it, and a later Close does
//     nothing more.
//
// Each signal goes to the group while the program, its leader, is not yet
// reaped, so the group's id is still the body's (worker protocol Signals).

// ExecLine is exec.Line: one line of a stream whose sink is ToCaller,
// without its line feed, and whether it came from standard error.
type ExecLine struct {
	Text   string
	Stderr bool
}

// The sink kinds, in exec.ergo's order (sinkKind).
const (
	execInherit  = 0
	execDiscard  = 1
	execToFile   = 2
	execToCaller = 3
)

// execLineBuffer is how many lines of the ToCaller streams wait for the
// caller. A full buffer stops the readers, the pipe fills, and the program
// waits in its write: the caller's reading paces the program.
const execLineBuffer = 64

// execProc is a started program as Running drives it: the host's process
// group (hostProc) or a test's fake.
type execProc interface {
	// exited is closed when the program has ended; it is not reaped yet.
	exited() <-chan struct{}
	// signal sends sig to the program's group. It is called only before
	// reap.
	signal(sig syscall.Signal)
	// reap reaps the program after it exited, waits until no process of
	// its group is left, and gives its status.
	reap() int
}

// Running is exec.Running: a started program and the lines it writes to
// the caller.
type Running struct {
	proc  execProc
	grace time.Duration
	// after is time.After; a model test replaces it, so that no check
	// depends on the clock.
	after func(time.Duration) <-chan time.Time

	lines chan ExecLine // nil when no stream's sink is ToCaller
	drop  chan struct{} // closed by finish: the readers drop each line
	ended chan struct{} // closed when every reader has read its stream to the end
	pipes []io.Closer   // the read ends of the ToCaller pipes
	files []*os.File    // the sinks' files, closed after the reap

	once   sync.Once
	status int
}

// newRunning starts the readers of the ToCaller streams out and errs
// (either may be nil) for p.
func newRunning(p execProc, grace time.Duration, out, errs io.ReadCloser, files []*os.File) *Running {
	r := &Running{proc: p, grace: grace, after: time.After, drop: make(chan struct{}), ended: make(chan struct{}), files: files}
	var wg sync.WaitGroup
	for _, s := range []struct {
		rc     io.ReadCloser
		stderr bool
	}{{out, false}, {errs, true}} {
		if s.rc == nil {
			continue
		}
		if r.lines == nil {
			r.lines = make(chan ExecLine, execLineBuffer)
		}
		r.pipes = append(r.pipes, s.rc)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.read(s.rc, s.stderr)
		}()
	}
	go func() {
		wg.Wait()
		if r.lines != nil {
			close(r.lines)
		}
		close(r.ended)
	}()
	return r
}

// read gives each line of rc to the caller until the stream ends, or
// drops it once finish has begun. A last line with no line feed is a line.
func (r *Running) read(rc io.Reader, stderr bool) {
	b := bufio.NewReader(rc)
	for {
		text, err := b.ReadString('\n')
		if text != "" {
			l := ExecLine{Text: strings.TrimSuffix(text, "\n"), Stderr: stderr}
			select {
			case r.lines <- l:
			case <-r.drop:
			}
		}
		if err != nil {
			return
		}
	}
}

// Lines is r.Lines(): the lines of the ToCaller streams as they come, until
// both have ended. It waits on the caller's task t: a stop ends the wait
// with an interruption, and the program goes on until r's release. After
// Wait or Close it gives no line. A break leaves the rest for the next
// Lines.
func (r *Running) Lines(t *rt.Task) iter.Seq[ExecLine] {
	return func(yield func(ExecLine) bool) {
		if r.lines == nil {
			return
		}
		for {
			t.Check()
			select {
			case <-r.drop:
				return
			default:
			}
			select {
			case l, ok := <-r.lines:
				if !ok || !yield(l) {
					return
				}
			case <-r.drop:
				return
			case <-t.Done():
				t.Check()
			}
		}
	}
}

// Wait is r.Wait(): it waits on the caller's task t for the program's end
// and gives its status (128 plus the signal's number for a program a signal
// ended). It drops the lines the caller has not read, so a full buffer
// cannot hold the program. A stop of t ends the wait with an interruption;
// the program goes on until r's release, which ends it. Inside a mask the
// wait is uninterruptible. A second Wait gives the same status.
func (r *Running) Wait(t *rt.Task) int {
	t.Check()
	select {
	case <-r.proc.exited():
	case <-t.Done():
		t.Check()
		<-r.proc.exited() // unreachable: Done is closed only for a stop
	}
	r.finish()
	return r.status
}

// Close is the release of r (resources.md R5, R9): masked on ct, the task
// that releases it, it ends the program when it still runs (SIGTERM, the
// grace, SIGKILL; see the lifecycle above) and then finishes as Wait does.
func (r *Running) Close(ct *rt.Task) {
	Release(ct, func() {
		r.end()
		r.finish()
	})
}

// Stop is the consuming, status-returning form of Close. It completes the
// same masked shutdown even when ct is already stopped; it must not call
// Wait, whose interruption point would lose the status after cleanup.
func (r *Running) Stop(ct *rt.Task) int {
	r.Close(ct)
	return r.status
}

// timer is r.after(d), or a channel that never fires when r has no clock
// (the Script's replay, whose program has ended and whose lines are read).
func (r *Running) timer(d time.Duration) <-chan time.Time {
	if r.after == nil {
		return nil
	}
	return r.after(d)
}

// end ends a program that still runs: SIGTERM to its group and the grace,
// then SIGKILL, and waits for its end. A program that has ended gets no
// signal here.
func (r *Running) end() {
	select {
	case <-r.proc.exited():
		return
	default:
	}
	if r.grace > 0 {
		r.proc.signal(syscall.SIGTERM)
		select {
		case <-r.proc.exited():
			return
		case <-r.timer(r.grace):
		}
	}
	r.proc.signal(syscall.SIGKILL)
	<-r.proc.exited()
}

// finish runs once, after the program exited: the output is read to its
// end for at most execWaitDelay, what is left of the group is killed, the
// output a process outside the group holds is closed, the program is
// reaped and the group waited for, and the sinks' files are closed.
func (r *Running) finish() {
	r.once.Do(func() {
		close(r.drop)
		select {
		case <-r.ended:
		case <-r.timer(execWaitDelay):
		}
		r.proc.signal(syscall.SIGKILL) // what is left of the group
		select {
		case <-r.ended:
		default:
			for _, p := range r.pipes {
				_ = p.Close()
			}
			<-r.ended
		}
		r.status = r.proc.reap()
		for _, f := range r.files {
			_ = f.Close()
		}
	})
}

// hostProc is a program of the host in a process group of its own.
type hostProc struct {
	cmd   *exec.Cmd
	group int
	exit  execExit
	done  chan struct{}
}

func (p *hostProc) exited() <-chan struct{} { return p.done }

func (p *hostProc) signal(sig syscall.Signal) {
	if sig == syscall.SIGKILL {
		execKillGroup(p.group)
		return
	}
	execSignalGroup(p.group, sig)
}

func (p *hostProc) reap() int {
	_ = p.cmd.Wait()
	execAwaitGroupEnd(p.group)
	p.exit.close()
	return execStatus(p.cmd.ProcessState)
}

// ExecStart is the body of exec.Host's Start on the caller's task t: it
// starts name with args as ExecRun does (env over the host's environment,
// dir, stdin), with its standard output and standard error going to the
// sinks outKind and errKind (exec.ergo's sinkKind; a file sink names its
// path and whether it appends). It returns once the program has started.
//
//ergo:host
func ExecStart(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, grace time.Duration, outKind int, outPath string, outAppend bool, errKind int, errPath string, errAppend bool) (*Running, error) {
	t.Check()
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
			cmd.Env = append(cmd.Env, k+"="+env[k])
		}
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var files []*os.File  // the sinks' files, the parent's copies
	var writes []*os.File // the write ends of the ToCaller pipes, the parent's copies
	var reads [2]*os.File // the read ends: stdout's, stderr's
	fail := func(err error) (*Running, error) {
		for _, f := range slices.Concat(files, writes, reads[:]) {
			if f != nil {
				_ = f.Close()
			}
		}
		return nil, err
	}
	for i, s := range []struct {
		kind   int
		path   string
		append bool
		label  string
	}{{outKind, outPath, outAppend, "stdout"}, {errKind, errPath, errAppend, "stderr"}} {
		var w *os.File
		switch s.kind {
		case execInherit:
			w = []*os.File{os.Stdout, os.Stderr}[i]
		case execDiscard:
			// nil: os/exec gives the program the null device.
		case execToFile:
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if s.append {
				flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			}
			f, err := os.OpenFile(s.path, flags, 0o644)
			if err != nil {
				return fail(ExecFailed{Name: name, Reason: s.label + " " + s.path + ": " + fsReason(err)})
			}
			files = append(files, f)
			w = f
		case execToCaller:
			pr, pw, err := os.Pipe()
			if err != nil {
				return fail(execErr(name, err))
			}
			reads[i] = pr
			writes = append(writes, pw)
			w = pw
		default:
			panic("exec: unknown sink kind")
		}
		if i == 0 {
			if w != nil {
				cmd.Stdout = w
			}
		} else if w != nil {
			cmd.Stderr = w
		}
	}
	exit, err := execWatchExit()
	if err != nil {
		return fail(execErr(name, err))
	}
	cmd.WaitDelay = execWaitDelay
	err = cmd.Start()
	for _, w := range writes {
		_ = w.Close() // the program holds its own copies
	}
	writes = nil
	if err != nil {
		exit.close()
		return fail(execErr(name, err))
	}
	p := &hostProc{cmd: cmd, group: cmd.Process.Pid, exit: exit, done: make(chan struct{})}
	go func() { exit.await(p.group); close(p.done) }()
	var out, errs io.ReadCloser
	if reads[0] != nil {
		out = reads[0]
	}
	if reads[1] != nil {
		errs = reads[1]
	}
	return newRunning(p, grace, out, errs, files), nil
}

// execSignalHook, when set, runs just before a signal other than SIGKILL
// goes to a group. Only a test sets it.
var execSignalHook func(group int, sig syscall.Signal)

// execSignalGroup sends sig to each process of the group, which the
// caller has not reaped the leader of.
func execSignalGroup(group int, sig syscall.Signal) {
	if execSignalHook != nil {
		execSignalHook(group, sig)
	}
	_ = syscall.Kill(-group, sig)
}

// replayProc is a program that has already ended with status: the Script
// provider's.
type replayProc struct{ status int }

var execClosed = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

func (replayProc) exited() <-chan struct{} { return execClosed }
func (replayProc) signal(syscall.Signal)   {}
func (p replayProc) reap() int             { return p.status }

// ExecReplay is the body of exec.Script's Start: a Running whose program
// has already ended with status, and whose Lines give out's lines (when
// outCaller) and then errs' lines (when errCaller). It starts nothing.
func ExecReplay(out []string, errs []string, status int, outCaller bool, errCaller bool) *Running {
	var lines []ExecLine
	if outCaller {
		for _, l := range out {
			lines = append(lines, ExecLine{Text: l})
		}
	}
	if errCaller {
		for _, l := range errs {
			lines = append(lines, ExecLine{Text: l, Stderr: true})
		}
	}
	r := &Running{proc: replayProc{status: status}, drop: make(chan struct{}), ended: make(chan struct{})}
	if outCaller || errCaller {
		r.lines = make(chan ExecLine, len(lines))
		// The buffer holds every line, so the filler never waits; it runs
		// apart only so that the call itself holds no channel operation
		// (ExecReplay is a noWait binding).
		go func(ch chan<- ExecLine) {
			for _, l := range lines {
				ch <- l
			}
			close(ch)
		}(r.lines)
	}
	close(r.ended)
	return r
}
