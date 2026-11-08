package proclimit

import (
	"bytes"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Watcher passes a program's stderr on and notes whether the Go runtime
// reported that it ran out of memory. The report's first line comes before
// a stack dump of any length, so Watcher looks at every byte.
type Watcher struct {
	mu    sync.Mutex
	next  io.Writer
	carry []byte // the last len(OutOfMemory)-1 bytes, for a report split across writes
	seen  bool
}

// Watch makes cmd's stderr pass through a Watcher, which Check reads. Call
// it after setting cmd.Stderr and before starting cmd. The program's stderr
// is then a pipe, not the file cmd.Stderr names. A process the program
// leaves behind may hold the pipe open; unless cmd sets its own WaitDelay,
// Wait then gives up on the pipe one second after the program exits, as it
// would not wait for a file.
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
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = time.Second
	}
	return w
}

func (w *Watcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.seen {
		joined := append(w.carry, p...)
		w.seen = bytes.Contains(joined, []byte(OutOfMemory))
		keep := min(len(joined), len(OutOfMemory)-1)
		w.carry = append(w.carry[:0:0], joined[len(joined)-keep:]...)
	}
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

// Stderr returns what Check needs of the program's stderr: the report when
// the Go runtime made one, else nothing.
func (w *Watcher) Stderr() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen {
		return []byte(OutOfMemory)
	}
	return nil
}
