//go:build !linux

package main

import (
	"os/exec"
	"time"
)

func instructionsCounted() bool { return false }

// measure runs cmd and reports its times; this host counts no
// instructions.
func measure(cmd *exec.Cmd) result {
	start := time.Now()
	err := cmd.Run()
	res := result{wall: time.Since(start)}
	if err != nil {
		res.exit = -1
		if ee, ok := err.(*exec.ExitError); ok {
			res.exit = ee.ExitCode()
		}
	}
	if cmd.ProcessState != nil {
		res.user, res.sys = cmd.ProcessState.UserTime(), cmd.ProcessState.SystemTime()
	}
	return res
}
