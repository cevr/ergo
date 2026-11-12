//go:build unix

package lint

import (
	"os"
	"syscall"
)

// processGroupAttr starts a pack as the leader of a new process group, so
// the runner can kill it together with every descendant that stayed in it.
func processGroupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// killProcessGroup kills the pack's process group. It is safe after the
// leader was reaped: the group id stays reserved while any member lives,
// and an empty group reports ESRCH, which is ignored.
func killProcessGroup(process *os.Process) { syscall.Kill(-process.Pid, syscall.SIGKILL) }
