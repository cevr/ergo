package proclimit

import (
	"io"
	"os/exec"
	"sync"
)

// Watcher passes a program's stderr on and notes whether the Go runtime
// reported that the kernel refused it memory (CauseOf). The report's first
// lines come before a stack dump of any length, so Watcher looks at every
// byte.
type Watcher struct {
	mu   sync.Mutex
	next io.Writer
	scan scanner
}

// Watch makes cmd's stderr pass through a Watcher, which Check reads. Call
// it after setting cmd.Stderr and before starting cmd. The program's stderr
// is then a pipe, not the file cmd.Stderr names, so wait for it with Wait,
// not cmd.Wait: a process the program leaves behind may hold the pipe
// open, and Watch starts the program in a process group of its own, which
// Wait kills once the program exits. Watch arms no fixed wait
// (exec.Cmd.WaitDelay): the 1s it set until WAITDELAY failed passing runs
// whose large output had not drained a second after the exit, on a loaded
// host (BUDGET-BACKFILL, 2026-11-21).
func Watch(cmd *exec.Cmd) *Watcher {
	next := cmd.Stderr
	if next == nil {
		next = io.Discard
	}
	w := &Watcher{next: next}
	if same(cmd.Stdout, cmd.Stderr) {
		// One writer for both streams (CombinedOutput's shape): it keeps one
		// pipe and one writer, so writes stay ordered and never concurrent.
		cmd.Stdout = w
	}
	cmd.Stderr = w
	ownGroup(cmd)
	return w
}

func (w *Watcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.scan.write(p)
	w.mu.Unlock()
	return w.next.Write(p)
}

// same reports whether a and b are the same writer, as os/exec compares
// them: a writer whose type cannot be compared is not the same.
func same(a, b io.Writer) (equal bool) {
	if a == nil || b == nil {
		return false
	}
	defer func() { _ = recover() }()
	return a == b
}

// Stderr returns what Check needs of the program's stderr: the shortest
// report of the cause the Watcher saw, else nothing.
func (w *Watcher) Stderr() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.scan.cause {
	case RuntimeReport:
		return []byte(OutOfMemory)
	case QueueFault:
		return []byte(queueFaultReport)
	}
	return nil
}
