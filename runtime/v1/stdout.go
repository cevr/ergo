package v1

import (
	"io"
	"os"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// Standard output is buffered when it is not a terminal (docs/spec/
// stdlib.md, io 10; owner 2026-11-12, std-better Q2 "Buffered when
// piped"). Every write of the program's standard output, Console's and
// io.Host's, goes through writeStdout. On a terminal each write goes to
// the host as it comes, as before. Anywhere else (a pipe, a file,
// /dev/null) the writes are kept, whole, in one buffer of the process, and
// reach the host when the buffer would pass stdoutChunk bytes and at each
// flush point: before a write to standard error (stderrSink), before a read
// of standard input waits on the host (lineReader.take), before a child
// process starts (ExecRun, ExecStart), when a task reaches a wait of the
// runtime (rt.Task.Done: a sleep, a queue, a join, an actor, a child, the
// network), and at the program's exit, a defect or an interruption
// included (mainStatus). So output is in program order
// against standard error, an interactive prompt shows before the read that
// waits for its answer, and a child's output follows what was printed
// before it started. What is lost: the buffer of a process that a signal
// kills without an exit (SIGKILL), or that a panic outside every task
// ends. A program that needs go.Foreign may write the host's stdout
// through Go's own os.Stdout, which the buffer cannot see, so its entry
// keeps standard output direct (StdoutDirect), and so does a test build.

// stdoutChunk is the size at which the buffer goes to the host: the size
// of Go's bufio.Writer.
const stdoutChunk = 4096

// stdout is the process's standard-output state. file is the os.Stdout
// the mode was decided for: a Go test that sets os.Stdout to a new file
// gets a new decision (as stdinReader does for os.Stdin), after what the
// old one kept is written to it.
var stdout stdoutState

type stdoutState struct {
	mu      sync.Mutex
	file    *os.File
	w       io.Writer
	decided bool
	direct  bool
	buf     []byte
	writes  int // the writes that reached the host: the counted cost (SB8 receipt; TestStdoutFlushModel)
}

// The buffer's held flag is rt's (rt.OutputHeld), so that a task's wait
// (rt.Task.Done) flushes it: a program that writes to a pipe and then
// waits, on a sleep, a queue, a join, a child, the network or its input,
// shows its output before the wait, with no timer.
func init() { rt.SetWaitFlush(flushStdout) }

// stdoutForcedDirect makes every later decision direct (StdoutDirect).
var stdoutForcedDirect atomic.Bool

// stdoutIsTerminal decides the mode: fileIsTerminal, which a test replaces
// to stand for a terminal.
var stdoutIsTerminal = fileIsTerminal

// StdoutDirect keeps standard output unbuffered for the rest of the
// process: the entry of a program that needs go.Foreign calls it before
// Main, and the test build's entry calls it (the harness writes the
// host's stdout through Go). What the buffer holds is written first.
//
//ergo:host
func StdoutDirect() {
	stdoutForcedDirect.Store(true)
	stdout.mu.Lock()
	defer stdout.mu.Unlock()
	stdout.flushLocked()
	stdout.decided = false
}

// writeStdout writes text to standard output in one piece: whole, never
// interleaved with another write.
func writeStdout(text string) {
	stdout.mu.Lock()
	defer stdout.mu.Unlock()
	stdout.decide()
	if stdout.direct {
		stdout.writes++
		_, _ = io.WriteString(stdout.w, text)
		return
	}
	if len(stdout.buf)+len(text) > stdoutChunk {
		stdout.flushLocked()
		if len(text) >= stdoutChunk {
			stdout.writes++
			_, _ = io.WriteString(stdout.w, text)
			return
		}
	}
	stdout.buf = append(stdout.buf, text...)
	rt.OutputHeld(true)
}

// decide sets the mode for os.Stdout, once per file: direct on a
// terminal or when StdoutDirect was called, buffered anywhere else. Called
// with mu held.
func (s *stdoutState) decide() {
	if s.decided && s.file == os.Stdout {
		return
	}
	s.flushLocked()
	s.file, s.w = os.Stdout, os.Stdout
	s.direct = stdoutForcedDirect.Load() || stdoutIsTerminal(os.Stdout)
	s.decided = true
}

// flushLocked writes the buffer to the host in one write. Called with mu
// held.
func (s *stdoutState) flushLocked() {
	if len(s.buf) > 0 {
		s.writes++
		_, _ = s.w.Write(s.buf)
		s.buf = s.buf[:0]
	}
	rt.OutputHeld(false)
}

// flushStdout writes what the buffer holds to the host: a flush point.
func flushStdout() {
	if !rt.IsOutputHeld() {
		return
	}
	stdout.mu.Lock()
	defer stdout.mu.Unlock()
	stdout.flushLocked()
}

// stderrSink is standard error behind the flush point: each write first
// sends what standard output holds, so the two streams keep program order.
// Every write of the runtime to standard error goes through it.
type stderrSink struct{}

func (stderrSink) Write(p []byte) (int, error) {
	flushStdout()
	return os.Stderr.Write(p)
}
