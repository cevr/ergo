//go:build linux && (amd64 || arm64)

package opentui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"
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
	ws := struct{ Row, Col, X, Y uint16 }{24, 80, 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		t.Fatal("TIOCSWINSZ:", e)
	}
	return m, s
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

// runOnTerminal runs bin AT STATUS with the terminal side of a fresh pair
// as its standard input and output. Once it has entered the alternate
// screen (its first frame) it is sent Ctrl+C; a run that fails before
// that ends by itself. The terminal's mode after the run must be the mode
// before it.
func runOnTerminal(t *testing.T, bin string, at, status int) terminalRun {
	m, s := ptyPair(t)
	before := termios(t, s)
	var mu sync.Mutex
	var out bytes.Buffer
	entered := make(chan struct{})
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
			if !once && strings.Contains(out.String(), enterAlt) {
				once = true
				close(entered)
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
	cmd := bounded(bin, fmt.Sprint(at), fmt.Sprint(status))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, &said
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-entered:
		if _, werr := m.Write([]byte{3}); werr != nil {
			t.Fatal(werr)
		}
		err = <-exited
	}
	if after := termios(t, s); after != before {
		t.Errorf("failterm %d %d left the terminal's mode changed: %+v, was %+v", at, status, after, before)
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
// counts the screen's checks), then once with each check failing with a
// library status. Every run leaves the terminal's mode (termios) as it was
// before; every run that entered the alternate screen leaves it and shows
// the cursor after its last entry; a failing run exits 1 and names the
// call and node of the failing check and the status.
//
// It builds and runs a Go binary on a pseudo-terminal because raw mode,
// the screen's terminal output and its restoration are the process's:
// no in-process check reaches them. Linux only: the pair is opened
// through /dev/ptmx.
//
// Mutants (testdata/mutants.json): OTF-raw-left, OTF-quit-swallowed.
func TestRunTerminalRestoresAfterAFailure(t *testing.T) {
	bin, err := build("failterm", "./cmd/failterm")
	if err != nil {
		t.Fatal(err)
	}
	full := runOnTerminal(t, bin, 0, 0)
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
	for at := 1; at <= len(checks); at++ {
		st := failStatuses[at%len(failStatuses)]
		r := runOnTerminal(t, bin, at, st)
		var call, node int
		fmt.Sscanf(strings.TrimPrefix(checks[at-1], "check "), "%d %d", &call, &node)
		want := fmt.Sprintf("failed %s %d %d", callNames[call], node, st)
		got := strings.SplitN(strings.TrimSpace(r.said), "\n", 2)[0]
		if got != want || r.err == nil {
			t.Errorf("check %d of %d (%s) failing with %d: said %q, want %q (exit %v)\n%s", at, len(checks), checks[at-1], st, got, want, r.err, r.said)
		}
		if why := restored(r); why != "" {
			t.Errorf("check %d failing: %s", at, why)
		}
	}
	t.Logf("%d checks failed one by one, the terminal restored after each", len(checks))
}
