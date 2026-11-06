//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts command in a process group of its own, which a
// stop kills whole (runCommand).
func ownProcessGroup(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
}

// killProcessGroup kills the process group whose leader is pid.
func killProcessGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
