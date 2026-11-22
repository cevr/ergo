//go:build !linux

package proclimit

import (
	"context"
	"os/exec"
)

// ownGroup does nothing here: Wait has no exit event that leaves the
// program unreaped (waitid's WNOWAIT, wait_linux.go), so it cannot end a
// group safely after the exit.
func ownGroup(*exec.Cmd) {}

// Wait is cmd.Wait here. It waits for the output to close with no fixed
// limit; a process the program leaves behind holding its output keeps
// Wait until ctx ends (wait_linux.go ends such a process with the
// program's group).
func Wait(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
