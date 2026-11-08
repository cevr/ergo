//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A command that writes past the cap on stdout and then past it on stderr,
// one after the other, gets both streams named when the cap's stop comes
// late; it can come late on any host, since the stop waits for the runner's
// goroutine to run and to take live.mu, while the command's last stdout
// bytes are already in the pipe's buffer and its next write goes on. Here
// the stop is held deterministically: the started hook takes live.mu and
// gives it back only when the command reports that its stderr write ended.
// This is the batch-7 TestOutputCap/record failure (rec/c11 [N+1 N+1] got
// "stdout over …; stderr over …" at load 75-90), made certain.
// Not parallel: it sets testHookStarted, which every execute reads.
func TestOutputCapLateStopNamesBothStreams(t *testing.T) {
	dir := t.TempDir()
	written := filepath.Join(dir, "written")
	if err := syscall.Mkfifo(written, 0o600); err != nil {
		t.Fatal(err)
	}
	// Opened read-write, the FIFO opens at once and never reads EOF; the
	// read waits for the command's line, bounded by the test's deadline in
	// case the stop no longer waits for live.mu and kills the command first.
	fifo, err := os.OpenFile(written, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	if deadline, ok := t.Deadline(); ok {
		fifo.SetReadDeadline(time.Now().Add(time.Until(deadline) * 9 / 10))
	}
	l := newLive()
	held := make(chan error, 1)
	testHookStarted = func([]string) {
		l.mu.Lock()
		go func() {
			defer l.mu.Unlock()
			_, err := fifo.Read(make([]byte, 16))
			held <- err
		}()
	}
	defer func() { testHookStarted = nil }()
	n := outputCap + 1
	script := fmt.Sprintf("head -c %d /dev/zero\nhead -c %d /dev/zero >&2\necho written >\"$1\"\n", n, n)
	o, err := l.execute([]string{"sh", "-c", script, "sh", written}, dir, os.Environ(), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-held; err != nil {
		t.Fatalf("the command never reported its stderr write (the stop did not wait for live.mu?): %v", err)
	}
	want := wantOver([2]int{n, n})
	if got := o.overCap(); got != want[len(want)-1] || !verdictIn(" "+got, want) {
		t.Errorf("a late stop: report %q, want %q (each stream over the cap before the stop, stdout first)", got, want[len(want)-1])
	}
	if o.stdout != nil || o.stderr != nil {
		t.Errorf("over-cap streams kept %d and %d bytes, want none", len(o.stdout), len(o.stderr))
	}
}
