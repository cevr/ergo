//go:build linux

package v1

import "syscall"

// execExit waits for the end of a program that ExecRun started, without
// reaping it (exec.go). On Linux it needs no state.
type execExit struct{}

// execWatchExit prepares the wait, before the program starts.
func execWatchExit() (execExit, error) { return execExit{}, nil }

// await returns when the program pid has ended (exited, or been killed by
// a signal), and leaves it unreaped: waitid with WEXITED|WNOWAIT, the wait
// Go's os package does before its own reap (os/wait_waitid.go). A stop
// (SIGSTOP, SIGTTIN) does not end the wait: WSTOPPED is not asked for. The
// info argument is NULL, which Linux accepts (kernel/exit.c, waitid), so
// the call needs no unsafe pointer. Any return but EINTR ends the wait;
// ECHILD, the only other one, means the program cannot be waited for, and
// then the reap in cmd.Wait reports it.
func (execExit) await(pid int) {
	const pPID = 1 // idtype_t P_PID
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), 0, syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if e != syscall.EINTR {
			return
		}
	}
}

// close releases the wait's state: none on Linux.
func (execExit) close() {}
