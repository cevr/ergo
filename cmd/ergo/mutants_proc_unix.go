//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"ergo.local/prototype/internal/proclimit"
)

func prepareBoundedChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	proclimit.Watch(cmd)
	cmd.Env = capChildGOMAXPROCS(cmd.Env, 4)
}

func killProcessGroup(pid int) {
	if pid > 1 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

func runBoundedChild(ctx context.Context, cmd *exec.Cmd, memLimit uint64) error {
	prepareBoundedChild(cmd)
	if err := proclimit.Start(cmd, memLimit); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		killProcessGroup(pid)
		return err
	case <-ctx.Done():
		killProcessGroup(pid)
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

func capChildGOMAXPROCS(env []string, maxProcs int) []string {
	if len(env) == 0 {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if val, ok := strings.CutPrefix(kv, "GOMAXPROCS="); ok {
			found = true
			if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= maxProcs {
				out = append(out, kv)
			} else {
				out = append(out, "GOMAXPROCS="+strconv.Itoa(maxProcs))
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "GOMAXPROCS="+strconv.Itoa(maxProcs))
	}
	return out
}
