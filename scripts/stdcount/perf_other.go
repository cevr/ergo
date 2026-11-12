//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

// countInstructions needs Linux's perf_event_open; elsewhere the harness
// refuses to count.
func countInstructions(*exec.Cmd) (uint64, error) {
	return 0, errors.New("the instruction counter needs Linux (perf_event_open)")
}
