//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import (
	"context"
	"os/exec"

	"ergo.local/prototype/internal/proclimit"
)

func runBoundedChild(ctx context.Context, cmd *exec.Cmd, memLimit uint64) error {
	proclimit.Watch(cmd)
	if err := proclimit.Start(cmd, memLimit); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
