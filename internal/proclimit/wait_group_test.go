//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package proclimit

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// heldWriter takes no byte until release is closed: the test, not the
// host's load, decides when the program's output drains.
type heldWriter struct {
	release chan struct{}
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (w *heldWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestWaitReadsOutputAfterExit: Wait returns only once every byte the
// program wrote has been copied, however long after its exit that is. The
// program (sh) writes about 40 KB, less than a pipe holds, and exits; its
// output is held until Wait has seen the exit and begun to wait for the
// output, and is released then only if no fixed wait is armed. A fixed
// wait (exec.Cmd.WaitDelay, the 1s Watch set until WAITDELAY) gives up on
// the held output and Wait returns exec.ErrWaitDelay: the flake of
// BUDGET-BACKFILL check1, where `ergo test` failed a passing model after a
// loaded host drained its output too slowly. It needs a real process and
// pipe: the wait is exec's on a program's exit. Mutant:
// proclimit-fixed-wait.
func TestWaitReadsOutputAfterExit(t *testing.T) {
	out := &heldWriter{release: make(chan struct{})}
	waitingForOutput = func(fixed bool) {
		if !fixed {
			close(out.release)
		}
	}
	t.Cleanup(func() { waitingForOutput = nil })
	cmd := exec.Command("sh", "-c", `i=0; while [ $i -lt 400 ]; do echo "line $i ........................................................................................"; i=$((i+1)); done; echo last`)
	cmd.Stdout = out
	Watch(cmd)
	if err := Start(cmd, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	if err := Wait(ctx, cmd); err != nil {
		if errors.Is(err, exec.ErrWaitDelay) {
			close(out.release)
		}
		t.Fatalf("Wait: %v; want every byte of the output, after the exit", err)
	}
	text := out.buf.String()
	if lines := strings.Count(text, "\n"); lines != 401 || !strings.HasSuffix(text, "\nlast\n") {
		t.Fatalf("got %d lines ending %q; want 401 ending \"last\"", lines, text[max(0, len(text)-20):])
	}
}

// ended reports a process that is gone or a zombie (ps prints its state,
// on Linux, Darwin and the BSDs alike): killed, whoever reaps it.
func ended(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))
	_ = err // ps exits 1 for a pid that is gone, with no output
	return state == "" || strings.HasPrefix(state, "Z")
}

// TestWaitEndsWhatTheProgramLeaves: a program that leaves a process
// behind holding its output (sh's `sleep &`) does not hold Wait: Wait
// kills the program's own process group once it exits, and the leftover
// dies with it. The test's process, in another group, is untouched. The
// only wait is the test's deadline, which names the hang. It needs real
// processes: a group and a pipe held open by a process are the kernel's.
// It runs on every unix Wait ends a group on (wait_group.go), Darwin and
// the BSDs through kqueue's exit event (wait_bsd.go).
// Mutants: proclimit-no-group-kill, proclimit-no-own-group.
func TestWaitEndsWhatTheProgramLeaves(t *testing.T) {
	var out bytes.Buffer
	cmd := exec.Command("sh", "-c", "sleep 100000 & echo $!")
	cmd.Stdout = &out
	Watch(cmd)
	if err := Start(cmd, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	if err := Wait(ctx, cmd); err != nil {
		t.Fatalf("Wait: %v; want it to end the leftover that holds the output", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil || pid <= 1 {
		t.Fatalf("the leftover's pid: %q", out.String())
	}
	if !testwait.Until(t, func() bool { return ended(pid) }) {
		t.Errorf("the leftover %d still runs after Wait", pid)
	}
}
