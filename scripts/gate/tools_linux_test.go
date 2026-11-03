//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Tool queries on Linux, where setsid(1) exists. A query runs in the gate's
// own process group. At its deadline the tool is killed; a descendant still
// holding the output pipes is cut off WaitDelay later and may outlive the
// query, so each fixture records the pids it forks and the test kills them.

// TestToolQueryIsBoundedByItsDeadlineAndWaitDelay runs tools that hang after
// forking a pipe holder, in the query's session or in a new one (setsid).
// Killing the tool at the deadline leaves the holder with the pipes open, so
// only WaitDelay ends the query; each fingerprint is still unlocated within
// its bound.
func TestToolQueryIsBoundedByItsDeadlineAndWaitDelay(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	killRecorded(t, pids)
	cases := map[string]string{
		"forks a holder and hangs":                  "sleep 30 &\necho $! >> " + pids + "\nwait\n",
		"forks a holder in a new session and hangs": "setsid sh -c 'echo $$ >> " + pids + "; exec sleep 30' &\nsleep 30 &\necho $! >> " + pids + "\nwait\n",
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		delete(cases, "forks a holder in a new session and hangs")
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			shortQueryTimeout(t)
			bin := t.TempDir()
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			executable(t, bin, "probe-tool", "#!/bin/sh\n"+body)
			assertHungToolUnlocated(t)
		})
	}
}

// TestGateUnderNohupIgnoresHangup runs a gate whose tool query hangs under
// nohup and sends it SIGHUP: an ignored hangup stays ignored, so the gate
// keeps running, as it did before the gate handled any signal.
func TestGateUnderNohupIgnoresHangup(t *testing.T) {
	if os.Getenv("GATE_HELPER") != "" {
		t.Skip("the helper runs as a subprocess of this test")
	}
	if _, err := exec.LookPath("nohup"); err != nil {
		t.Skip("no nohup")
	}
	pids := filepath.Join(t.TempDir(), "pids")
	killRecorded(t, pids)
	bin := t.TempDir()
	executable(t, bin, "probe-tool", "#!/bin/sh\necho $$ >> "+pids+"\nexec sleep 30\n")
	helper := exec.Command("nohup", os.Args[0], "-test.run=^TestGateHelper$", "-test.count=1")
	helper.Env = append(os.Environ(), "GATE_HELPER="+t.TempDir(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	helper.Stdout, helper.Stderr = &output, &output
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()
	stop := func() {
		helper.Process.Kill()
		<-exited
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(recordedPids(t, pids)) == 0 {
		if time.Now().After(deadline) {
			stop()
			t.Fatalf("the helper's query never started:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := helper.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("the gate under nohup ended on SIGHUP (%v):\n%s", err, output.String())
	case <-time.After(time.Second):
	}
	stop()
}

// TestGateHelper is the gate TestGateUnderNohupIgnoresHangup signals: a run
// over a directory that is no repository, whose one step names probe-tool.
// Loading the files fails at once, and run then waits for the hung
// fingerprint query it started.
func TestGateHelper(t *testing.T) {
	root := os.Getenv("GATE_HELPER")
	if root == "" {
		t.Skip("run by TestGateUnderNohupIgnoresHangup")
	}
	g := &gate{root: root, cacheDir: t.TempDir(), steps: []*Step{{Name: "probe", Argv: []string{"true"}, Inputs: &Inputs{Tools: []string{"probe-tool"}}}}}
	g.run(&bytes.Buffer{})
}

// killRecorded kills, when the test ends, every process recorded in the
// pid files, so a failing assertion leaves nothing running.
func killRecorded(t *testing.T, files ...string) {
	t.Cleanup(func() {
		for _, file := range files {
			for _, pid := range recordedPids(t, file) {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

// recordedPids reads the process IDs a fixture appended to path.
func recordedPids(t *testing.T, path string) []int {
	t.Helper()
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, field := range strings.Fields(string(content)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}
