//go:build linux || darwin

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

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
