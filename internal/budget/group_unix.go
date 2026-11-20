//go:build unix && !linux

package budget

import "syscall"

// groupAttr puts the command in its own process group. There is no
// parent-death signal outside Linux: a command outlives a killed Run.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
