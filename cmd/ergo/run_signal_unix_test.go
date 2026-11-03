//go:build unix

package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// sketchWaitSource prints "ready" and then sleeps for a minute.
const sketchWaitSource = `func wait() needs Console, Scheduler {
	Console.Println("ready")
	Scheduler.Sleep(60s)
}

let Host = context {
	Scheduler: LiveScheduler,
}

func main() needs Console {
	with Host { wait() }
}
`

// TestRunForwardsSignalsToTheProgram: `ergo run` forwards SIGINT and
// SIGTERM to the program it runs. The program installs no handler, so the
// signal ends it at once, long before its sleep would, and `ergo run` exits
// 128 plus the signal number with no message of its own: 130 for SIGINT,
// the interruption status of docs/spec/runtime.md rule 6, and 143 for
// SIGTERM.
func TestRunForwardsSignalsToTheProgram(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	file := smokeWrite(t, root, "wait.ergo", sketchWaitSource)
	for signal, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143} {
		start := time.Now()
		stdout, stderr, code := runInterruptedEntry(t, binary, root, file, signal)
		if stdout != "ready\n" || code != want || stderr != "" || time.Since(start) > 50*time.Second {
			t.Errorf("%v: code=%d stdout=%q stderr=%q after %v; want exit %d, the program ended by the forwarded signal", signal, code, stdout, stderr, time.Since(start), want)
		}
	}
}

// TestRunPassesTheProgramsExitStatusThrough: a positive exit status passes
// through without a message of ergo's own; a program ended by a signal is
// an error naming the signal.
func TestRunPassesTheProgramsExitStatusThrough(t *testing.T) {
	err := runEntryProcess(exec.Command("sh", "-c", "exit 3"))
	var exit exitStatusError
	if !errors.As(err, &exit) || exit.code != 3 || !exit.handled || exit.message != "" {
		t.Fatalf("exit 3 gave %#v", err)
	}
	if err := runEntryProcess(exec.Command("sh", "-c", "exit 0")); err != nil {
		t.Fatalf("exit 0 gave %v", err)
	}
	err = runEntryProcess(exec.Command("sh", "-c", `kill -TERM $$`))
	if errors.As(err, &exit) || err == nil || !strings.Contains(err.Error(), "terminated") {
		t.Fatalf("a program ended by a signal gave %#v", err)
	}
	if err := runEntryProcess(exec.Command(filepath.Join(t.TempDir(), "missing"))); err == nil {
		t.Fatal("a missing program started")
	}
}

// runInterruptedEntry runs `ergo run` in its own process group, signals only
// `ergo` once the program is ready, and on a timeout kills the whole group so
// no descendant keeps the capture pipes open. WaitDelay bounds pipe draining.
func runInterruptedEntry(t *testing.T, binary, root, file string, signal syscall.Signal) (string, string, int) {
	t.Helper()
	command := exec.Command(binary, "run", file)
	command.Dir = root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	stdout := &readyWriter{ready: make(chan struct{})}
	command.Stdout, command.Stderr = stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	killGroup := func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
	}
	select {
	case <-stdout.ready:
	case err := <-done:
		t.Fatalf("program exited before ready: %v stderr=%q", err, stderr.String())
	case <-time.After(processDeadline):
		killGroup()
		t.Fatalf("program did not report ready: stderr=%q", stderr.String())
	}
	if err := command.Process.Signal(signal); err != nil {
		killGroup()
		t.Fatal(err)
	}
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(30 * time.Second):
		killGroup()
		t.Fatalf("%v did not end the program", signal)
	}
	code := 0
	var exited *exec.ExitError
	if errors.As(waitErr, &exited) {
		code = exited.ExitCode()
	} else if waitErr != nil {
		t.Fatal(waitErr)
	}
	return stdout.String(), stderr.String(), code
}

// readyWriter collects a program's stdout and reports its first line.
type readyWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	seen  bool
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data.Write(p)
	if !w.seen && strings.Contains(w.data.String(), "\n") {
		w.seen = true
		close(w.ready)
	}
	return len(p), nil
}

func (w *readyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}
