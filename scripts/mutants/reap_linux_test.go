//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reapHelperVariable makes this test binary a helper process instead of a
// test run: "orphan" starts a "hold" process in a group of its own, writes
// its pid to REAP_PIDFILE and exits at once; "hold" waits for a signal.
const reapHelperVariable = "ERGO_MUTANTS_REAP_HELPER"

// reapHelper runs the helper the environment names, and says whether it
// did (TestMain then exits).
func reapHelper() bool {
	switch os.Getenv(reapHelperVariable) {
	case "orphan":
		hold := exec.Command(os.Args[0])
		hold.Env = append(os.Environ(), reapHelperVariable+"=hold")
		hold.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := hold.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("REAP_PIDFILE"), []byte(strconv.Itoa(hold.Process.Pid)), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "hold":
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		<-c
		os.Exit(0)
	}
	return false
}

// TestAbandonedGroupIsStopped is the smallest repro of the GT-BOUND full
// run's leak (GT-REAP): a child that leaves a process in a group of its
// own, as `ergo test` does with its explorations, and ends without
// stopping it (as a test the runner stops at its deadline does). Its
// group stop does not reach that process; the runner, its subreaper,
// stops it when the child ends. Mutant: gtr-orphan-left. It starts this
// test binary as the child, so it builds no Go.
func TestAbandonedGroupIsStopped(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	env := append(os.Environ(), reapHelperVariable+"=orphan", "REAP_PIDFILE="+pidFile)
	e := &engine{}
	// The deadline only stops a hung helper.
	o := e.exec(context.Background(), t.TempDir(), []string{os.Args[0]}, env, time.Hour)
	if o.reason != "" {
		t.Fatalf("the helper failed: %s\n%s", o.reason, o.tail)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("pid %q", data)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		// Stop the leftover: its own group, which this test's helper made.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		t.Fatalf("the helper's orphan %d (group %d) outlived it: %v", pid, pid, err)
	}
}
