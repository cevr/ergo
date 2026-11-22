//go:build linux

package proclimit

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

// ownGroup starts cmd's program as the leader of a process group of its
// own (Watch), so Wait can end what it leaves behind. A caller that chose
// its own session or group keeps it.
func ownGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if a := cmd.SysProcAttr; !a.Setsid && !a.Foreground && a.Pgid == 0 {
		a.Setpgid = true
	}
}

// waitingForOutput, when set (a test), runs once the program has exited
// and its group is killed, as Wait starts to wait for the output to close;
// fixed reports a fixed wait still armed on cmd (WaitDelay).
var waitingForOutput func(fixed bool)

// Wait waits for cmd's program (started by Start, after Watch) to exit,
// then kills its process group, the processes it left behind, which may
// hold its output pipes, and then waits for the output to close: the
// copies of every byte the program wrote, however slowly they drain. It
// never gives up a fixed time after the exit (exec.Cmd.WaitDelay); the
// only deadline is ctx's, which stops a run whose output a process outside
// its group still holds, and names it. The program's pid is not reaped
// until the group is killed (waitid with WNOWAIT), so the group's id is
// never another's.
func Wait(ctx context.Context, cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		exited(pid)
		if a := cmd.SysProcAttr; a != nil && a.Setpgid && a.Pgid == 0 && pid > 1 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		if waitingForOutput != nil {
			waitingForOutput(cmd.WaitDelay != 0)
		}
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("program %s: its output is still open after it and its process group ended (a process outside its group holds it): %w", cmd.Path, ctx.Err())
	}
}

// exited blocks until pid has exited, leaving it unreaped (WNOWAIT): its
// exit is the event, and cmd.Wait reaps it after.
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
