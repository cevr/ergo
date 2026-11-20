package v1

import (
	"io"
	"os"

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

// termMode is a mode an Enter pushes. term_unix.go changes the terminal's
// state to it.
type termMode int

const (
	termRaw termMode = iota
	termNoEcho
)

// TermEnterRaw is the body of term.Host's Enter(Raw): standard input's
// terminal reads each key as it comes, with no echo, no line editing and
// no signal keys (Ctrl-C is a key). Output processing stays on, so a line
// feed still starts a new line.
//
//ergo:host
//ergo:wait none
func TermEnterRaw() error {
	return termEnter(0, termRaw)
}

// TermEnterNoEcho is the body of term.Host's Enter(NoEcho): the terminal
// keeps its line editing and stops echoing what is typed.
//
//ergo:host
//ergo:wait none
func TermEnterNoEcho() error {
	return termEnter(0, termNoEcho)
}

// TermLeave is the body of term.Host's Leave: the terminal goes back to
// the state the latest Enter not yet left saved. With none, it does
// nothing.
//
//ergo:host
//ergo:wait none
func TermLeave() {
	termLeave(0)
}

// TermGetSize is the body of term.Host's Size: the size of the terminal of
// standard output, or of standard input when standard output is not one.
//
//ergo:host
//ergo:wait none
func TermGetSize() (TermWindow, error) {
	for _, fd := range []int{1, 0} {
		if cols, rows, ok := termSize(fd); ok {
			return TermWindow{Cols: cols, Rows: rows}, nil
		}
	}
	return TermWindow{}, TermNotTerminal{}
}

// TermPeeked is term's peeked: what TermPeek found in standard input's
// buffer, not taken from it. Bytes is all the buffer holds; End is whether
// the input ended after them; Resized is whether the window's size changed
// while the peek waited (Bytes is then what the buffer held, maybe no
// more than before).
type TermPeeked struct {
	Bytes   string
	End     bool
	Resized bool
}

// TermPeek is the body of term.Host's ReadKey's wait on t, the caller's
// task: it waits until io's one shared reader of standard input holds more
// than have bytes, the input ends, or the window's size changes, and
// returns what the buffer holds without taking it, so a byte the key did
// not use stays for the next read, a line read's included. term's Cut and
// Decode name the key and TermDrop takes its bytes: the splitting is
// Ergo's, in one place. A stop of t ends the wait and raises
// rt.Interrupted. A read failure other than the end of the input fails
// ReadFailed, once.
//
//ergo:host
//ergo:wait caller
func TermPeek(t *rt.Task, have int) (TermPeeked, error) {
	t.Check()
	b, end, resized, err, ok := stdinReader().peek(t, have, termResizes())
	switch {
	case !ok:
		panic(rt.Interrupted)
	case err != nil:
		return TermPeeked{}, ReadFailed{Reason: fsReason(err)}
	}
	return TermPeeked{Bytes: string(b), End: end, Resized: resized}, nil
}

// TermDrop is the body of term.Host's ReadKey's take: it drops the first
// n bytes of io's shared reader of standard input, the bytes of the key
// TermPeek showed.
//
//ergo:host
//ergo:wait none
func TermDrop(n int) {
	stdinReader().drop(n)
}

// peek waits on t until l's buffer holds more than have bytes, the input
// ended, or a value comes on resized, and returns the buffer (a copy),
// whether the input ended after it and whether the size changed. The end
// of the input stays, so every later peek sees it; any other read failure
// is returned once. It is not ok when t stopped.
func (l *lineReader) peek(t *rt.Task, have int, resized <-chan os.Signal) (b []byte, end bool, sized bool, err error, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var stop <-chan struct{}
	asked := false
	for {
		if len(l.buf) > have || l.err != nil {
			if l.err != nil && l.err != io.EOF {
				err, l.err = l.err, nil
				return nil, false, false, err, true
			}
			return append([]byte(nil), l.buf...), l.err != nil, false, nil, true
		}
		if !asked && t != nil {
			stop, asked = t.Done(), true
		}
		if !l.reading {
			l.reading = true
			l.landed = make(chan struct{})
			go l.read()
		}
		landed := l.landed
		l.mu.Unlock()
		select {
		case <-landed:
		case <-resized:
			l.mu.Lock()
			return append([]byte(nil), l.buf...), false, true, nil, true
		case <-stop:
			l.mu.Lock()
			return nil, false, false, nil, false
		}
		l.mu.Lock()
	}
}

// drop drops the first n bytes of l's buffer (all of them when it holds
// fewer).
func (l *lineReader) drop(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = l.buf[min(max(n, 0), len(l.buf)):]
	if len(l.buf) == 0 {
		l.buf = nil
	}
}
