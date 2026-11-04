//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts the command as the leader of a new process group, so the
// runner can stop it with everything it started.
func ownGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopGroup sends SIGKILL to the process group the runner started with
// process as its leader (ownGroup). It is the runner's only signal: never to
// another group, and never to pid -1, 0 or 1. A group whose members are all
// gone is no error.
func stopGroup(process *os.Process) {
	if process == nil || process.Pid <= 1 {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
}

func signalName(state *os.ProcessState) string {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal().String()
	}
	return "unknown"
}
