package v1

import (
	"io"
	"sync"
	"syscall"
	"unicode/utf8"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of std term (docs/spec/stdlib.md, term): the terminal of
// standard input. A mode change is a stack: TermEnterRaw and
// TermEnterNoEcho push the state before them, and TermLeave restores the
// state the latest push saved. term.Raw and term.NoEcho pair each push
// with a Leave in an exit action (scope.OnExit), so every exit path of the
// scope, a stop included, restores the terminal.
//
// Prior art: Go's golang.org/x/term (MakeRaw, Restore, GetSize) does the
// same ioctls and leaves the pairing to a defer; Elixir's OTP 26 raw mode
// is a shell option the caller sets back; Roc basic-cli Tty has
// enableRawMode and disableRawMode with no pairing.

// TermNotTerminal is term.NotTerminal: standard input is not a terminal,
// so its mode cannot change and it has no size.
type TermNotTerminal struct{}

// Error is the failure's Ergo name.
func (TermNotTerminal) Error() string { return "term.NotTerminal{}" }

// TermWindow is term.Window: the terminal's width in columns and height in
// rows.
type TermWindow struct {
	Cols int
	Rows int
}

// termStack holds the states the pushes saved, latest last.
var termStack struct {
	sync.Mutex
	saved []syscall.Termios
}

// TermEnterRaw is the body of term.Host's Enter(Raw): standard input's
// terminal reads each key as it comes, with no echo, no line editing and
// no signal keys (Ctrl-C is a key). Output processing stays on, so a line
// feed still starts a new line.
//
//ergo:host
func TermEnterRaw() error {
	return termEnter(0, rawState)
}

// TermEnterNoEcho is the body of term.Host's Enter(NoEcho): the terminal
// keeps its line editing and stops echoing what is typed.
//
//ergo:host
func TermEnterNoEcho() error {
	return termEnter(0, noEchoState)
}

// TermLeave is the body of term.Host's Leave: the terminal goes back to
// the state the latest Enter not yet left saved. With none, it does
// nothing.
//
//ergo:host
func TermLeave() {
	termLeave(0)
}

// TermGetSize is the body of term.Host's Size: the size of the terminal of
// standard output, or of standard input when standard output is not one.
//
//ergo:host
func TermGetSize() (TermWindow, error) {
	for _, fd := range []int{1, 0} {
		if cols, rows, ok := termSize(fd); ok {
			return TermWindow{Cols: cols, Rows: rows}, nil
		}
	}
	return TermWindow{}, TermNotTerminal{}
}

// TermReadKey is the body of term.Host's ReadKey on t, the caller's task:
// the next key of standard input, read through the one shared reader of
// io (so no byte that a line read buffered is lost). It returns the key's
// token (keyToken): "" at the end of the input. A stop of t ends the wait
// and raises rt.Interrupted; the bytes read stay for the next read.
//
//ergo:host
func TermReadKey(t *rt.Task) (string, error) {
	t.Check()
	b, err, ok := stdinReader().take(t.Done(), keyLen)
	switch {
	case !ok:
		panic(rt.Interrupted)
	case err == io.EOF:
		return "", nil
	case err != nil:
		return "", ReadFailed{Reason: fsReason(err)}
	}
	return keyToken(b), nil
}

func termEnter(fd int, change func(*syscall.Termios)) error {
	termStack.Lock()
	defer termStack.Unlock()
	before, ok := getTermios(fd)
	if !ok {
		return TermNotTerminal{}
	}
	next := before
	change(&next)
	if !setTermios(fd, &next) {
		return TermNotTerminal{}
	}
	termStack.saved = append(termStack.saved, before)
	return nil
}

func termLeave(fd int) {
	termStack.Lock()
	defer termStack.Unlock()
	n := len(termStack.saved)
	if n == 0 {
		return
	}
	before := termStack.saved[n-1]
	termStack.saved = termStack.saved[:n-1]
	setTermios(fd, &before)
}

// rawState is cfmakeraw without its output change: no input translation,
// no echo, no canonical mode, no signal keys, eight-bit characters, and a
// read that waits for one byte.
func rawState(t *syscall.Termios) {
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
}

// noEchoState turns echo off and keeps line editing.
func noEchoState(t *syscall.Termios) {
	t.Lflag &^= syscall.ECHO | syscall.ECHOE | syscall.ECHOK | syscall.ECHONL
}

// keyLen is the length of the first key at the front of b, or -1 when b
// holds only the start of one and more input may come (end is false). A
// key is one byte, one UTF-8 rune, or an escape sequence: ESC [ or ESC O,
// parameter bytes, and a final byte in 0x40-0x7e. An ESC with nothing
// after it in the bytes read is the Escape key, since a terminal writes a
// sequence in one write.
func keyLen(b []byte, end bool) int {
	switch {
	case len(b) == 0:
		return -1
	case b[0] == 0x1b:
		if len(b) == 1 {
			return 1
		}
		if b[1] != '[' && b[1] != 'O' {
			return 1
		}
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		if end {
			return len(b)
		}
		return -1
	case b[0] < utf8.RuneSelf:
		return 1
	case !utf8.FullRune(b) && !end:
		return -1
	}
	_, n := utf8.DecodeRune(b)
	return n
}

// keyToken is the token of one key's bytes that term's decode reads: a
// carriage return or a line feed is "\r"; a delete or a backspace is
// "\x7f"; an arrow is ESC [ and its letter, whether the terminal sent
// ESC [ or ESC O; every other key is its bytes.
func keyToken(b []byte) string {
	switch {
	case len(b) == 1 && (b[0] == '\n' || b[0] == '\r'):
		return "\r"
	case len(b) == 1 && b[0] == 0x08:
		return "\x7f"
	case len(b) == 3 && b[0] == 0x1b && b[1] == 'O':
		return "\x1b[" + string(b[2])
	}
	return string(b)
}
