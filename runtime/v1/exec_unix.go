//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import "syscall"

// execUnsupported is empty: Run and Start start programs here, each in a
// process group of its own (exec_other.go is the platform that cannot).
const execUnsupported = ""

// execGroupAttr puts the program in a new process group, so a stop or a
// grace's end reaches the processes it started too.
func execGroupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// execKill sends sig to pid (a negative pid is a group), as kill(2).
func execKill(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
