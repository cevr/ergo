//go:build linux && (amd64 || arm64)

package opentui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/hosts/opentui/screen"
)

// ptyPair is a pseudo-terminal pair: the controlling side and the
// terminal, closed when the test ends.
func ptyPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Fatal("TIOCSPTLCK:", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Fatal("TIOCGPTN:", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := setWindow(m, 24, 80); err != nil {
		t.Fatal(err)
	}
	return m, s
}

func setWindow(f *os.File, rows, cols uint16) error {
	ws := struct{ Row, Col, X, Y uint16 }{rows, cols, 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		return fmt.Errorf("TIOCSWINSZ: %w", e)
	}
	return nil
}

func termios(t *testing.T, f *os.File) syscall.Termios {
	t.Helper()
	var st syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&st))); e != 0 {
		t.Fatal("TCGETS:", e)
	}
	return st
}

// The sequences that enter and leave the alternate screen, and show the
// cursor.
const (
	enterAlt   = "\x1b[?1049h"
	leaveAlt   = "\x1b[?1049l"
	showCursor = "\x1b[?25h"
)

// terminalRun is one failterm run on a pseudo-terminal: what it wrote to
// the terminal, what it said on standard error, and its exit.
type terminalRun struct {
	wrote, said string
	err         error
}

// runOnTerminal runs bin AT CALL STATUS with the terminal side of a fresh pair
// as its standard input and output. Once the first frame is visible it
// is sent Ctrl+C or a bad resize; a run that fails earlier ends by
// itself. The terminal's mode after the run must be the mode before it.
func runOnTerminal(t *testing.T, bin string, at, call, status int, badResize bool) terminalRun {
	m, s := ptyPair(t)
	before := termios(t, s)
	var mu sync.Mutex
	var out bytes.Buffer
	ready := make(chan struct{})
	// The first frame is after Open installed its signal watcher.
	const marker = "count 0"
	readDone := make(chan struct{})
	// read gets a value after each read of the controlling side.
	read := make(chan struct{}, 1)
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		once := false
		for {
			n, err := m.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			if !once && strings.Contains(out.String(), marker) {
				once = true
				close(ready)
			}
			mu.Unlock()
			select {
			case read <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	var said bytes.Buffer
	cmd := bounded(bin, fmt.Sprint(at), fmt.Sprint(call), fmt.Sprint(status))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, &said
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-ready:
		if badResize {
			if err := setWindow(m, 0, 0); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, werr := m.Write([]byte{3}); werr != nil {
				t.Fatal(werr)
			}
		}
		err = <-exited
	}
	if after := termios(t, s); after != before {
		t.Errorf("failterm %d %d %d left the terminal's mode changed: %+v, was %+v", at, call, status, after, before)
	}
	// What the run wrote is all in the pair's buffer once it exited; a
	// write of our own marks its end.
	if _, werr := s.Write([]byte("\x00end")); werr != nil {
		t.Fatal(werr)
	}
	for {
		mu.Lock()
		done := strings.HasSuffix(out.String(), "\x00end")
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-read:
		case <-readDone:
			t.Fatal("the terminal's output ended before the run's end")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return terminalRun{wrote: strings.TrimSuffix(out.String(), "\x00end"), said: said.String(), err: err}
}

// TestRunTerminalRestoresAfterAFailure: tui.RunTerminal leaves the
// terminal as it found it however the run ends. failterm runs counter's
// app on a pseudo-terminal once with no failure (ended by Ctrl+C, which
// counts the screen's checks), then once with each startup check failing,
// once for Next and once for Quit. Every run leaves termios as it was
// before; every run that entered the alternate screen leaves it and shows
// the cursor after its last entry; a failing run exits 1 and names the
// call and node of the failing check and the status.
//
// It builds and runs a Go binary on a pseudo-terminal because raw mode,
// the screen's terminal output and its restoration are the process's:
// no in-process check reaches them. Linux only: the pair is opened
// through /dev/ptmx.
//
// Mutants: OTF-raw-left, OTF-quit-swallowed, OTF-resize-as-input.
func TestRunTerminalRestoresAfterAFailure(t *testing.T) {
	bin, err := build("failterm", "./cmd/failterm")
	if err != nil {
		t.Fatal(err)
	}
	full := runOnTerminal(t, bin, 0, screen.AnyCall, 0, false)
	lines := strings.Split(strings.TrimSpace(full.said), "\n")
	if full.err != nil || lines[0] != "ended" || len(lines) < 3 {
		t.Fatalf("failterm with no failure: %v\n%s", full.err, full.said)
	}
	checks := lines[1:]
	restored := func(r terminalRun) string {
		i := strings.LastIndex(r.wrote, enterAlt)
		if i < 0 {
			return ""
		}
		rest := r.wrote[i:]
		if !strings.Contains(rest, leaveAlt) || !strings.Contains(rest, showCursor) {
			return fmt.Sprintf("after its last %q the terminal's output has no %q and %q: %q", enterAlt, leaveAlt, showCursor, rest[max(0, len(rest)-200):])
		}
		return ""
	}
	if why := restored(full); why != "" {
		t.Fatalf("with no failure: %s", why)
	}
	exercised := 0
	check := func(at, only, call, node int) {
		exercised++
		st := failStatuses[exercised%len(failStatuses)]
		r := runOnTerminal(t, bin, at, only, st, false)
		want := fmt.Sprintf("failed %s %d %d", callNames[call], node, st)
		got := strings.SplitN(strings.TrimSpace(r.said), "\n", 2)[0]
		if got != want || !exitedOne(r.err) {
			t.Errorf("check %d (selector %d) failing with %d: said %q, want %q (exit %v)\n%s", at, only, st, got, want, r.err, r.said)
		}
		if why := restored(r); why != "" {
			t.Errorf("check %d (selector %d) failing: %s", at, only, why)
		}
	}
	for i, line := range checks {
		var call, node int
		if n, err := fmt.Sscanf(line, "check %d %d", &call, &node); n != 2 || err != nil {
			t.Fatalf("invalid check trace: %q", line)
		}
		if call == screen.CallInput {
			break
		}
		check(i+1, screen.AnyCall, call, node)
	}
	// Pty delivery can let Next consume more than one event before Ctrl+C.
	// Select Input and Quit independently of that count.
	check(1, screen.CallInput, screen.CallInput, 0)
	check(1, screen.CallQuit, screen.CallQuit, 0)
	r := runOnTerminal(t, bin, 0, screen.AnyCall, 0, true)
	if got := strings.SplitN(r.said, "\n", 2)[0]; got != "failed resize 0 -" || !exitedOne(r.err) {
		t.Errorf("an invalid live resize: said %q, exit %v", r.said, r.err)
	}
	if why := restored(r); why != "" {
		t.Errorf("an invalid live resize: %s", why)
	}
	t.Logf("%d injected failures and one refused resize, the terminal restored after each", exercised)
}

func exitedOne(err error) bool {
	e, ok := err.(*exec.ExitError)
	return ok && e.ExitCode() == 1
}
