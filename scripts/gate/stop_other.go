//go:build !unix

package main

import (
	"os"
	"os/exec"
)

func ownProcessGroup(*exec.Cmd) {}

// killProcessGroup kills the process pid; there are no process groups.
func killProcessGroup(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}
