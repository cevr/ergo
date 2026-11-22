//go:build linux

package proclimit

import (
	"syscall"
	"unsafe"
)

// exited blocks until pid has exited, leaving it unreaped (waitid with
// WNOWAIT): its exit is the event, and cmd.Wait reaps it after.
func exited(pid int) {
	const pPID, wExited, wNoWait = 1, 4, 0x01000000
	var info [128]byte // siginfo_t
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), wExited|wNoWait, 0, 0)
		if e != syscall.EINTR {
			return
		}
	}
}
