//go:build !unix

package main

import (
	"os"
	"os/exec"
)

// ownGroup has no process groups to make here; the gate runs on Linux and
// darwin, which use process_unix.go.
func ownGroup(*exec.Cmd) {}

// stopGroup stops only the process the runner started.
func stopGroup(process *os.Process) {
	if process != nil {
		_ = process.Kill()
	}
}

func signalName(*os.ProcessState) string { return "unknown" }

// stopSignals stop a run; here only an interrupt.
var stopSignals = []os.Signal{os.Interrupt}

// signalExit names a stop signal and gives the status of a run it stopped:
// 130, as for SIGINT.
func signalExit(sig os.Signal) (string, int) { return sig.String(), 130 }
