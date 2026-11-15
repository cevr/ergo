//go:build !linux && !darwin

package v1

import "syscall"

// execStartTime is 0: this host's start time is not read yet, so Alive
// and Kill there cannot tell a reused pid from the program (a known gap,
// docs/spec/stdlib.md, Detached start 4).
func execStartTime(pid int) int64 { return 0 }

// execRunning reports that the process pid exists: signal 0 checks it and
// sends nothing. A zombie still counts here.
func execRunning(pid int, start int64) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
