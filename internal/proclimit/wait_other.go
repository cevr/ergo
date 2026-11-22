//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package proclimit

import (
	"context"
	"os/exec"
)

// ownGroup does nothing here (Windows, Plan 9, Solaris, AIX, wasm): there
// is no exit event that leaves the program unreaped (wait_linux.go,
// wait_bsd.go), so no group can be ended safely after the exit.
func ownGroup(*exec.Cmd) {}

// Wait is cmd.Wait here, stopped by ctx. Its bound: it waits until every
// process that holds the program's output has ended, the program and any
// it started that inherited the pipes, with no fixed limit; a leftover
// that never ends keeps it until ctx ends. On Windows a job object that
// kills the program's tree at its exit would close that gap; WAITDELAY
// does not build it.
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
