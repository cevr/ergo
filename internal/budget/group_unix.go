//go:build unix && !linux

package budget

import (
	"os"
	"syscall"
)

// groupAttr puts the command in its own process group. There is no
// parent-death signal outside Linux: a command outlives a killed Run.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// maxRSS is the largest resident set of the command's processes, in bytes
// (the BSDs and Darwin count bytes).
func maxRSS(ps *os.ProcessState) int64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return int64(ru.Maxrss)
	}
	return 0
}
