//go:build linux

package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// prSetChildSubreaper is Linux's PR_SET_CHILD_SUBREAPER (prctl(2)).
const prSetChildSubreaper = 36

// A test that starts a process in a group of its own (as the Processes
// rule asks: `ergo test` explorations, fetchpool's workers) stops that
// group when it ends. A child the runner stops at its deadline never ends
// that way, so stopGroup does not reach those groups: three such
// `ergo test … fetchpool` processes ran 13.8 h after their worker had
// given up on the mutant (GT-REAP). So the runner is the subreaper of
// everything it starts: an orphan of a child becomes the runner's child,
// and reapAfter stops each orphan that carries the ended child's token
// (execTokenVariable), with its group, and waits for it.
var subreaper = sync.OnceFunc(func() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
})

// started are the pids exec started and has not waited for yet: they are
// os/exec's to wait for, never the reaper's.
// startMu keeps a sweep from seeing a child between its fork and its
// track: such a child that exited at once would be a zombie the sweep
// waits for, and os/exec's Wait would fail.
var (
	started sync.Map
	startMu sync.RWMutex
)

// starting holds off sweeps until the returned function is called, after
// track.
func starting() func() {
	startMu.RLock()
	return startMu.RUnlock
}

func track(pid int)   { started.Store(pid, true) }
func untrack(pid int) { started.Delete(pid) }

// reapAfter stops and waits for the runner's orphan children whose
// environment carries token, until none is left (stopping one hands its
// own children to the runner). An empty token stops every orphan: the
// run's end. It also waits for any orphan that has already exited. It
// signals only a pid that is the runner's own child, and that child's
// group when the group is not the runner's; never pid 0, 1 or -1.
func reapAfter(token string) {
	self, group := os.Getpid(), syscall.Getpgrp()
	want := []byte(execTokenVariable + "=" + token)
	for {
		var stopped []int
		startMu.Lock()
		for _, pid := range runnerChildren(self) {
			if _, mine := started.Load(pid); mine || pid <= 1 || pid == self {
				continue
			}
			zombie, pgid := procState(pid)
			if !zombie {
				env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
				if err != nil {
					continue
				}
				if token != "" && !carries(env, want) {
					continue
				}
				if pgid > 1 && pgid != group {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				}
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			stopped = append(stopped, pid)
		}
		startMu.Unlock()
		if len(stopped) == 0 {
			return
		}
		for _, pid := range stopped {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &status, 0, nil)
		}
	}
}

// carries says whether a /proc environ (NUL-separated) holds the entry.
func carries(environ, entry []byte) bool {
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if bytes.Equal(kv, entry) {
			return true
		}
	}
	return false
}

// runnerChildren are the pids whose parent is self. /proc/self/task/*/
// children lists a thread's own children only; an orphan goes to the
// thread group, so this reads every process's parent.
func runnerChildren(self int) []int {
	var out []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if ppid := procParent(pid); ppid == self {
			out = append(out, pid)
		}
	}
	return out
}

// procParent is pid's parent, or 0.
func procParent(pid int) int {
	fields := statFields(pid)
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// procState says whether pid is a zombie, and its process group.
func procState(pid int) (bool, int) {
	fields := statFields(pid)
	if len(fields) < 3 {
		return false, 0
	}
	pgid, _ := strconv.Atoi(fields[2])
	return fields[0] == "Z", pgid
}

// statFields are /proc/PID/stat's fields after the command name: state,
// ppid, pgrp, ….
func statFields(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil
	}
	at := bytes.LastIndexByte(data, ')')
	if at < 0 {
		return nil
	}
	return strings.Fields(string(data[at+1:]))
}
