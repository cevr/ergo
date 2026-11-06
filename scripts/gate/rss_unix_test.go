//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakePS puts a ps on PATH that prints the given lines, then prints nothing
// once the file the returned path names exists.
func fakePS(t *testing.T, lines string) string {
	t.Helper()
	bin := t.TempDir()
	gone := filepath.Join(bin, "gone")
	executable(t, bin, "ps", "#!/bin/sh\n[ -e '"+gone+"' ] && exit 0\nprintf '"+lines+"'\n")
	t.Setenv("PATH", bin)
	return gone
}

// The leftover diagnostic names live processes only; a zombie, with or
// without running threads, is a process the stop is already ending.
func TestProcessGroupMembersSkipsZombies(t *testing.T) {
	fakePS(t, "123 4321 ZNl zombie-leader\\n124 4321 Z dead\\n125 4321 S live\\n126 99 S other\\n")
	members, err := processGroupMembers(4321)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(members, ",") != "live (pid 125)" {
		t.Fatalf("members %v, want only the live process", members)
	}
}

// A zombie leader whose other threads run ("Zl" on Linux) still shares its
// file table, so it can hold the step's output pipe: the group is not empty
// until it is gone. A zombie without threads has closed its files.
func TestWaitForProcessGroupKeepsZombieLeaderWithThreads(t *testing.T) {
	gone := fakePS(t, "123 4321 ZNl zombie-leader\\n124 4321 Z dead\\n")
	done := make(chan error, 1)
	go func() { done <- waitForProcessGroup(4321) }()
	select {
	case err := <-done:
		t.Fatalf("the wait ended (%v) while a zombie leader's threads ran", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.WriteFile(gone, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end once the group was gone")
	}

	fakePS(t, "124 4321 Z dead\\n")
	if err := waitForProcessGroup(4321); err != nil {
		t.Fatal(err)
	}
}

// The real ps marks a zombie leader whose other thread runs as "Zl", and
// running() reads it as able to hold the output pipe.
func TestGroupProcessZombieLeaderOfRealPS(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("needs Linux threads: darwin makes a process a zombie only once all its threads end")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3 to end a leader thread alone")
	}
	// The leader thread exits by the exit system call while a second
	// thread sleeps.
	script := `import ctypes, platform, threading, time
threading.Thread(target=time.sleep, args=(5,)).start()
time.sleep(0.1)
ctypes.CDLL(None).syscall({"x86_64": 60, "aarch64": 93}[platform.machine()], 0)`
	command := exec.Command(python, "-c", script)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	pid := command.Process.Pid
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if !time.Now().Before(deadline) {
			t.Fatal("the leader thread never became a zombie")
		}
		status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if strings.Contains(string(status), "State:\tZ") {
			break
		}
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	group, err := processGroup(pgid)
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range group {
		if process.pid == pid {
			if !process.zombie() || !process.running() {
				t.Fatalf("ps stat %q: want a zombie leader that still runs", process.stat)
			}
			return
		}
	}
	t.Fatalf("pid %d not in its process group %d: %v", pid, pgid, group)
}

// Every ps the RSS watch runs is a tracked command (fu-gate U5): the
// sampler's and the one a step's finish runs. A tracked command runs in a
// process group of its own, which a stop kills and waits for; an
// untracked exec.Command runs in the gate's group, and a ps of it in
// flight when the gate exits holds what the gate inherited. The fake ps
// records its pid and its process group; each must be equal.
func TestRSSWatchTracksItsPS(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("the fake ps reads its process group from /proc")
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "groups")
	executable(t, bin, "ps", "#!/bin/sh\nread -r pid comm state ppid pgrp rest < /proc/$$/stat\necho \"$pid $pgrp\" >> '"+record+"'\nprintf '125 4321 S live\\n'\n")
	t.Setenv("PATH", bin)

	watch, err := openRSSWatch(memoryBudget{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	watch.close()
	if _, err := processGroupMembers(4321); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ps ran %d times, want 2 (the sampler's, the finish's): %q", len(lines), recorded)
	}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != fields[1] {
			t.Errorf("ps %d (pid, process group) %q: not in a group of its own, so not a tracked command", i+1, line)
		}
	}
}
