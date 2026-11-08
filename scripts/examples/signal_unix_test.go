//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// SIGINT, SIGTERM or SIGHUP to the runner while a case runs: the runner stops
// the case's group, removes its scratch and the case's directory, prints
// "examples: stopped by <signal>" and exits 128 plus the signal number. The
// runner is this test binary started again as the runner (asRunner); the test
// signals only that child, never its own process.
func TestASignalStopsTheRun(t *testing.T) {
	t.Parallel()
	rows := []struct {
		signal syscall.Signal
		name   string
	}{{syscall.SIGINT, "SIGINT"}, {syscall.SIGTERM, "SIGTERM"}, {syscall.SIGHUP, "SIGHUP"}}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ticks := filepath.Join(t.TempDir(), "ticks")
			f.program("waiter", "#!/bin/sh\n"+ticker(`"$TICKS"`)+caseSleep, "slow\n",
				map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": ""}}, nil)
			temp := t.TempDir()
			runner := exec.Command(os.Args[0], "-root", f.root, "-ergo", f.ergo, "-timeout", caseTimeout(t), "waiter")
			runner.Env = append(os.Environ(), asRunner+"=1", "TMPDIR="+temp)
			var stdout, stderr bytes.Buffer
			runner.Stdout, runner.Stderr = &stdout, &stderr
			testwait.WaitDelay(t, runner)
			if err := runner.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() {
				_ = runner.Wait()
				close(exited)
			}()
			t.Cleanup(func() {
				select {
				case <-exited:
				default:
					_ = runner.Process.Kill() // the child this test started and still holds
					<-exited
				}
			})
			if !testwait.Until(t, func() bool { info, err := os.Stat(ticks); return err == nil && info.Size() > 0 }) {
				t.Fatalf("the case never ticked")
			}
			if err := runner.Process.Signal(row.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-testwait.After(t):
				t.Fatalf("the runner did not end by the test's deadline after %s", row.name)
			}
			if code, want := runner.ProcessState.ExitCode(), 128+int(row.signal); code != want {
				t.Errorf("after %s the runner exited %d (%s), want %d\n%s%s", row.name, code, runner.ProcessState, want, stdout.String(), stderr.String())
			}
			if want := "examples: stopped by " + row.name + "\n"; !strings.Contains(stderr.String(), want) {
				t.Errorf("no %q on stderr: %q", want, stderr.String())
			}
			if grows(t, ticks) {
				t.Error("the case's background child still runs after the runner ended")
			}
			entries, err := os.ReadDir(temp)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				t.Errorf("the runner left %s in its TMPDIR", entry.Name())
			}
		})
	}
}
