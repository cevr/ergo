package budget

import (
	"os"
	"syscall"
)

// groupAttr puts the command in its own process group and kills it when
// Run dies.
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// maxRSS is the largest resident set of the command's processes, in
// bytes (Linux counts kilobytes).
func maxRSS(ps *os.ProcessState) int64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss * 1024
	}
	return 0
}
