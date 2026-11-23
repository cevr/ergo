//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"ergo.local/prototype/hosts/opentui/ot"
)

// Open is a screen on the process's terminal: it reads standard input and
// writes standard output, and follows the terminal's size (SIGWINCH). The
// caller sets raw mode (std term.Raw) around it; while it is open nothing
// else may read standard input. Quit restores the terminal.
func Open() (*Screen, error) {
	cols, rows, err := termSize(os.Stdout)
	if err != nil {
		return nil, fail(CallOpen, 0, fmt.Errorf("screen: the terminal's size: %w", err))
	}
	s, err := OpenOn(os.Stdin, os.Stdout, cols, rows)
	if err != nil {
		return nil, err
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	watcherStopped := make(chan struct{})
	go func() {
		defer close(watcherStopped)
		for {
			select {
			case <-winch:
				c, r, err := termSize(os.Stdout)
				if err != nil {
					err = fail(CallResize, 0, fmt.Errorf("screen: the terminal's size: %w", err))
				} else {
					err = s.Resize(c, r)
				}
				if err != nil {
					s.failInput(err)
					return
				}
			case <-done:
				return
			}
		}
	}()
	readerStop := s.stop
	s.stop = func() {
		signal.Stop(winch)
		close(done)
		<-watcherStopped
		readerStop()
	}
	return s, nil
}

// OpenOn is a terminal screen of cols by rows cells that reads in and
// writes out: Open's, on files a test can make (a pipe pair).
func OpenOn(in, out *os.File, cols, rows int) (*Screen, error) {
	s, err := newScreen(cols, rows, out)
	if err != nil {
		return nil, err
	}
	s.live, s.in = true, in
	err = check(CallSetup, 0)
	setup := false
	s.do(func() {
		if err != nil {
			return
		}
		s.calls++
		err = ot.SessionSetupTerminal(s.ctx, s.session, ot.SessionTerminalOptions{
			Flags:              ot.OT_TERMINAL_ALTERNATE_SCREEN | ot.OT_TERMINAL_MOUSE | ot.OT_TERMINAL_CLEAR_ON_CLOSE,
			KittyKeyboardFlags: ot.OT_KITTY_KEYBOARD_DISAMBIGUATE,
		})
		if err == nil {
			setup = true
			err = s.pump(false)
		} else {
			err = fail(CallSetup, 0, err)
		}
	})
	if err != nil {
		// A successful setup may already have written to the terminal.
		// Restore it in that case, and free what newScreen made either way.
		s.live = setup
		_ = s.Quit()
		return nil, fail(CallSetup, 0, fmt.Errorf("screen: terminal setup: %w", err))
	}
	// One reader of the input: its bytes go to the parser on the owner
	// thread; end of input closes.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		buf := make([]byte, 4096)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if !s.feedLive(append([]byte(nil), buf[:n]...)) {
					return
				}
			}
			if rerr != nil {
				s.EndInput()
				return
			}
		}
	}()
	s.stop = func() {}
	_ = stopped
	return s, nil
}

// feedLive parses b on the real clock; when the parser holds a lone
// Escape it is decided at the parser's deadline by a feed of no bytes.
// It reports false once the screen closed.
func (s *Screen) feedLive(b []byte) bool {
	var got []Input
	var wait time.Duration
	if err := s.owner.Do(func() {
		if s.isDone() {
			return
		}
		now := uint64(time.Since(s.start).Nanoseconds()) + 1
		got = s.parse(b, now)
		if s.deadline > now {
			wait = time.Duration(s.deadline - now)
		}
	}); err != nil {
		return false
	}
	s.push(got...)
	if wait > 0 {
		time.AfterFunc(wait, func() { s.feedLive(nil) })
	}
	return true
}

func (s *Screen) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// pump runs the session's timed work until it needs no turn now (owner
// thread), writing its output. With untilClosed it goes on until the
// session is closed, waiting for each deadline the library names.
func (s *Screen) pump(untilClosed bool) error {
	for range 100000 {
		now := uint64(time.Since(s.start).Nanoseconds()) + 1
		s.calls++
		r, err := ot.SessionPump(s.ctx, s.session, now, 64)
		if err != nil {
			return fail(CallWrite, 0, fmt.Errorf("screen: pump: %w", err))
		}
		if err := s.flush(); err != nil {
			return err
		}
		switch r.Status {
		case ot.OT_PUMP_CLOSED:
			return nil
		case ot.OT_PUMP_WAIT_UNTIL:
			if !untilClosed {
				return nil
			}
			if d := time.Duration(r.DeadlineNs - now); r.DeadlineNs > now {
				time.Sleep(d)
			}
		case ot.OT_PUMP_AGAIN, ot.OT_PUMP_OUTPUT_PENDING:
		default:
			// Idle: no work until the next input, frame or close.
			return nil
		}
	}
	return fail(CallWrite, 0, ErrUnsettled)
}

// restore closes the session and writes the terminal's restoration
// (owner thread).
func (s *Screen) restore() error {
	s.calls++
	if err := ot.SessionClose(s.ctx, s.session); err != nil {
		return fail(CallQuit, 0, fmt.Errorf("screen: close: %w", err))
	}
	return s.pump(true)
}

// termSize is the size of the terminal f in cells.
func termSize(f *os.File) (int, int, error) {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if e != 0 {
		return 0, 0, e
	}
	if ws.Col == 0 || ws.Row == 0 {
		return 0, 0, ErrZeroSize
	}
	return int(ws.Col), int(ws.Row), nil
}
