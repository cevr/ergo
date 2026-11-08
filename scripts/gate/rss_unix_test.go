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

	"ergo.local/prototype/internal/testwait"
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
	case <-testwait.After(t):
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
	// thread waits for the end of its input, which the test holds open
	// (start returns once the thread runs).
	script := `import ctypes, platform, sys, threading
threading.Thread(target=sys.stdin.read).start()
ctypes.CDLL(None).syscall({"x86_64": 60, "aarch64": 93}[platform.machine()], 0)`
	command := exec.Command(python, "-c", script)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	pid := command.Process.Pid
	if !testwait.Until(t, func() bool {
		status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return strings.Contains(string(status), "State:\tZ")
	}) {
		t.Fatal("the leader thread never became a zombie")
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
// records its pid and its process group; each must be equal. Then the test
// observes the gate's stopper itself, with a ps that runs until the test
// lets it end: a stop kills a sampler's ps in flight and has waited for it
// when it ends, and a stop waits for a finish's ps in flight and lets it
// run to its end. An untracked ps in a group of its own fails both.
func TestRSSWatchTracksItsPS(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("the fake ps reads its process group from /proc")
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "groups")
	hold := filepath.Join(bin, "hold")
	executable(t, bin, "ps", "#!/bin/sh\nread -r pid comm state ppid pgrp rest < /proc/$$/stat\necho \"$pid $pgrp\" >> '"+record+"'\nwhile [ -e '"+hold+"' ]; do sleep 0.01; done\nprintf '125 4321 S live\\n'\n")
	t.Setenv("PATH", bin)
	// The test stops the gate's stopper; a fresh one serves the tests after
	// it. No other test runs meanwhile: this one is not parallel.
	gateStop = stopper{}
	t.Cleanup(func() { gateStop = stopper{} })

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

	// From here each ps runs until the hold file is gone. The test removes
	// it at its end in every case, so no ps outlives it.
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(hold) })

	// A stop kills the sampler's ps in flight, and has waited for it: when
	// the stop ends, the ps has been reaped.
	sampled := make(chan error, 1)
	go func() {
		_, err := sampleGroups()
		sampled <- err
	}()
	pid := awaitPS(t, record, 3)
	// A tracked ps can run before its kill is armed (Start returns after
	// exec); the stop under test is the one that finds it armed. Untracked,
	// it is never armed, and the stop below shows that.
	// The wait ends at the arm, or at the test's deadline: a stop that
	// came before the arm on a loaded host found no kill and failed a
	// tracked ps (fu-gate-flakes).
	testwait.Until(t, func() bool {
		gateStop.mu.Lock()
		defer gateStop.mu.Unlock()
		return len(gateStop.kills) > 0
	})
	stopped := make(chan struct{})
	go func() {
		gateStop.stop(syscall.SIGTERM)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-testwait.After(t):
		t.Fatal("the stop did not end: it did not kill the sampler's ps")
	}
	if _, err := os.Stat(filepath.Join("/proc", pid)); err == nil {
		t.Error("the stop ended while the sampler's ps (pid " + pid + ") ran: the stop did not track it")
	}
	select {
	case err := <-sampled:
		if err == nil {
			t.Error("the sampler's ps ran to its end during a stop, want it killed")
		}
	case <-testwait.After(t):
		t.Error("the sampler's ps still ran at the test's deadline after the stop ended: the stop did not track it")
	}

	// A stop waits for a finish's ps in flight and does not kill it: the
	// step's entry holds the stop until its finish returns, and then the ps
	// alone holds it.
	gateStop = stopper{}
	step, err := gateStop.track()
	if err != nil {
		t.Fatal(err)
	}
	inspected := make(chan error, 1)
	go func() {
		_, err := processGroupMembers(4321)
		inspected <- err
	}()
	awaitPS(t, record, 4)
	stopped = make(chan struct{})
	go func() {
		gateStop.stop(syscall.SIGTERM)
		close(stopped)
	}()
	testwait.Until(t, gateStop.stopping)
	step.done()
	select {
	case <-stopped:
		t.Error("the stop ended while a finish's ps ran: the stop did not track it")
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-inspected:
		if err != nil {
			t.Errorf("the finish's ps during a stop: %v, want it to run to its end", err)
		}
	case <-testwait.After(t):
		t.Fatal("the finish's ps did not end once released")
	}
	select {
	case <-stopped:
	case <-testwait.After(t):
		t.Fatal("the stop did not end after the finish's ps ended")
	}
}

// awaitPS waits until the fake ps has recorded its nth run and returns that
// run's pid.
func awaitPS(t *testing.T, record string, n int) string {
	t.Helper()
	pid := ""
	if !testwait.Until(t, func() bool {
		recorded, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
		if len(lines) >= n {
			pid = strings.Fields(lines[n-1])[0]
		}
		return pid != ""
	}) {
		t.Fatalf("ps did not start its run %d", n)
	}
	return pid
}
