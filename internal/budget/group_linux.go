package budget

import "syscall"

// groupAttr puts the command in its own process group and kills it when
// Run dies.
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
