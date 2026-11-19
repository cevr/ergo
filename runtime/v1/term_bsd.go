//go:build dragonfly || freebsd || netbsd || openbsd

package v1

import "syscall"

// The BSDs name the termios requests as Darwin does (term_darwin.go).
const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)
