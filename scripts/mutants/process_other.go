//go:build !unix

package main

import (
	"os"
	"os/exec"
)

// ownGroup does nothing here: the runner is for Unix hosts.
func ownGroup(*exec.Cmd) {}

// stopGroup stops only the process itself.
func stopGroup(process *os.Process) {
	if process != nil {
		_ = process.Kill()
	}
}
