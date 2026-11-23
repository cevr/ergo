//go:build linux && (amd64 || arm64)

package testinputs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Subreaping is process-wide. Exec owns it only from launch through the
// orphan check, and never races that interval with another Exec.
var execReaper sync.Mutex

// Exec runs a command under observation (ergo task exec): this binary,
// which links this package, starts again as a child that installs the
// filter, hands its listener over and executes the command in its place
// (runTarget). It returns when the command exits, with what its process
// tree depended on. Result.Reason is not empty when the
// observation is incomplete; the result must then not be reused.
//
// The command gets spec.Env and nothing else (no variable of this
// process), standard input from /dev/null (a pipe or a terminal is an
// input no path names), and spec.Dir as its working directory.
func Exec(spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, fmt.Errorf("no command")
	}
	executable, err := os.Executable()
	if err != nil {
		return ExecResult{}, err
	}
	command := exec.Command(executable)
	command.Args = spec.Argv
	command.Dir = spec.Dir
	for _, kv := range spec.Env {
		if !strings.HasPrefix(kv, "ERGO_TESTINPUTS_") {
			command.Env = append(command.Env, kv)
		}
	}
	command.Env = append(command.Env, targetVariable+"=1")
	stdout, err := captureOutput(spec.Stdout)
	if err != nil {
		return ExecResult{}, err
	}
	defer stdout.close()
	stderr, err := captureOutput(spec.Stderr)
	if err != nil {
		return ExecResult{}, err
	}
	defer stderr.close()
	command.Stdout, command.Stderr = stdout.writer, stderr.writer
	log := newAccessLog(spec.Snap)
	o := &observer{sink: log, target: true, dirs: linkedDirs{aliases: map[string][]string{}}}
	if err := o.runTarget(command); err != nil {
		return ExecResult{}, err
	}
	if !o.armed.Load() {
		log.refuse("the command never ran under the filter")
	}
	log.validate()
	if err := errors.Join(stdout.flush(spec.Stdout), stderr.flush(spec.Stderr)); err != nil {
		log.refuse("copy the command output: " + err.Error())
	}
	result := ExecResult{Exit: childExit(command.ProcessState), Reason: log.reason()}
	result.Inputs, result.Written = log.result()
	return result, nil
}

func (o *observer) runTarget(command *exec.Cmd) error {
	execReaper.Lock()
	defer execReaper.Unlock()
	restore, err := subreaper()
	if err != nil {
		return err
	}
	defer func() {
		if err := restore(); err != nil {
			o.sink.refuse(err.Error())
		}
	}()
	previous, err := directChildren()
	if err != nil {
		return err
	}
	if err := o.run(command); err != nil {
		return err
	}
	if children, err := directChildren(); err != nil {
		o.sink.refuse(err.Error())
	} else {
		for pid, child := range children {
			if old, ok := previous[pid]; ok && old.start == child.start {
				continue
			}
			if child.state == "Z" && child.group == command.Process.Pid {
				// This is a child from our private group, not one
				// another caller might be about to Wait on.
				var status syscall.WaitStatus
				syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
				continue
			}
			if child.state != "Z" {
				o.sink.refuse("a process the command started is still running after it exited")
			}
		}
	}
	return nil
}

type output struct {
	writer io.Writer
	spool  *os.File
}

// A pipe would keep Wait open while a descendant held its write end,
// preventing the orphan check. Files leave Wait tied to the command's
// process exit; a caller's own file never needs a spool.
func captureOutput(dst io.Writer) (output, error) {
	if _, file := dst.(*os.File); dst == nil || file {
		return output{writer: dst}, nil
	}
	f, err := os.CreateTemp("", "ergo-exec-output-*")
	if err != nil {
		return output{}, err
	}
	// The already open descriptors still name it. It needs no filesystem
	// entry, even if the command is interrupted.
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return output{}, err
	}
	return output{writer: f, spool: f}, nil
}

func (o output) close() {
	if o.spool != nil {
		o.spool.Close()
	}
}

func (o output) flush(dst io.Writer) error {
	if o.spool == nil {
		return nil
	}
	st, err := o.spool.Stat()
	if err != nil {
		return err
	}
	// Read at most what was written at completion, even if a refused
	// descendant retains the file and adds to it.
	_, err = io.Copy(dst, io.NewSectionReader(o.spool, 0, st.Size()))
	return err
}

func subreaper() (restore func() error, err error) {
	const setChildSubreaper = 36
	const getChildSubreaper = 37
	var previous int32 // PR_GET_CHILD_SUBREAPER writes a C int.
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, getChildSubreaper, uintptr(unsafe.Pointer(&previous)), 0); errno != 0 {
		return nil, fmt.Errorf("prctl(PR_GET_CHILD_SUBREAPER): %w", errno)
	}
	set := func(enable uintptr) error {
		if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, setChildSubreaper, enable, 0); errno != 0 {
			return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER): %w", errno)
		}
		return nil
	}
	if previous != 0 {
		return func() error { return nil }, nil
	}
	if err := set(1); err != nil {
		return nil, err
	}
	return func() error { return set(0) }, nil
}

type childState struct {
	state, start string
	group        int
}

// directChildren sees descendants that were reparented to the subreaper,
// including a descendant that left the command's own process group.
func directChildren() (map[int]childState, error) {
	self := strconv.Itoa(os.Getpid())
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("list children in /proc: %w", err)
	}
	children := map[int]childState{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp ... starttime. comm may hold ')' .
		end := strings.LastIndexByte(string(stat), ')')
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) <= 19 || fields[1] != self {
			continue
		}
		group, _ := strconv.Atoi(fields[2])
		children[pid] = childState{fields[0], fields[19], group}
	}
	return children, nil
}

func childExit(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
