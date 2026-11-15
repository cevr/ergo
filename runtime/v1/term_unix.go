//go:build linux || darwin

package v1

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// termStack holds the states the pushes saved, latest last.
var termStack struct {
	sync.Mutex
	saved []syscall.Termios
}

func termEnter(fd int, mode termMode) error {
	termStack.Lock()
	defer termStack.Unlock()
	before, ok := getTermios(fd)
	if !ok {
		return TermNotTerminal{}
	}
	next := before
	switch mode {
	case termRaw:
		rawState(&next)
	case termNoEcho:
		noEchoState(&next)
	}
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

// winsize is the kernel's struct winsize, the result of TIOCGWINSZ.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// ioctl is the one unsafe edge of std term: a system call that reads or
// writes the kernel struct at arg. Safety invariant: arg points to a live
// Go value of the exact kernel type the request names (syscall.Termios for
// the get and set requests, winsize for the size request), so the kernel
// writes inside it; the pointer is converted in the call expression, so
// the value stays live and unmoved for the call (unsafe.Pointer rule 4).
// No safe form exists in Go's standard library, so this is the FFI edge
// of the 2026-11-02 rule, not a speed use. TestTermModesRestore
// (internal/sketch/lower) checks the complete state after generated mode
// scopes. The pointer conversion lives only here, at the syscall edge.
func ioctl[T syscall.Termios | winsize](fd int, req uintptr, arg *T) bool {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(arg)))
	return errno == 0
}

func getTermios(fd int) (syscall.Termios, bool) {
	var t syscall.Termios
	ok := ioctl(fd, ioctlGetTermios, &t)
	return t, ok
}

func setTermios(fd int, t *syscall.Termios) bool {
	return ioctl(fd, ioctlSetTermios, t)
}

func termSize(fd int) (cols, rows int, ok bool) {
	var w winsize
	if !ioctl(fd, syscall.TIOCGWINSZ, &w) {
		return 0, 0, false
	}
	return int(w.Col), int(w.Row), true
}

// fileIsTerminal reports whether f is a terminal: whether its state can be
// read, as isatty does. It reads f's descriptor through SyscallConn, so f
// keeps its blocking mode (Fd would set it blocking).
func fileIsTerminal(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	if rc.Control(func(fd uintptr) { _, ok = getTermios(int(fd)) }) != nil {
		return false
	}
	return ok
}
