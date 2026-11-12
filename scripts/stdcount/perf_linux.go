//go:build linux

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

// perfAttr is struct perf_event_attr (linux/perf_event.h), version 8.
type perfAttr struct {
	typ, size           uint32
	config              uint64
	sample              uint64
	sampleType, readFmt uint64
	flags               uint64
	wakeup, bpType      uint32
	bpAddr, bpLen       uint64
	branchSampleType    uint64
	sampleRegsUser      uint64
	sampleStackUser     uint32
	clockid             int32
	sampleRegsIntr      uint64
	auxWatermark        uint32
	sampleMaxStack      uint16
	_                   uint16
	auxSampleSize       uint32
	_                   uint32
	sigData             uint64
	config3             uint64
}

const (
	perfTypeHardware    = 0
	perfHWInstructions  = 1
	perfFormatEnabled   = 1 << 0
	perfFormatRunning   = 1 << 1
	perfFlagDisabled    = 1 << 0
	perfFlagInherit     = 1 << 1
	perfFlagExclKernel  = 1 << 5
	perfFlagExclHV      = 1 << 6
	perfFlagEnableOnExe = 1 << 12
)

// countInstructions runs cmd and returns the user-space instructions its
// process and every thread and child it starts retired: a counter on the
// calling thread, inherited by the fork and enabled at exec, as `perf stat`
// counts a child. The calling goroutine is locked to its thread for the
// run. The child leads a process group of its own; a run past the deadline
// that the caller set on cmd is stopped with its group.
func countInstructions(cmd *exec.Cmd) (uint64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := perfAttr{typ: perfTypeHardware, config: perfHWInstructions, readFmt: perfFormatEnabled | perfFormatRunning,
		flags: perfFlagDisabled | perfFlagInherit | perfFlagExclKernel | perfFlagExclHV | perfFlagEnableOnExe}
	a.size = uint32(unsafe.Sizeof(a))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&a)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("perf_event_open: %w (kernel.perf_event_paranoid must be 2 or lower)", errno)
	}
	defer syscall.Close(int(fd))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// The group this run started, never another.
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	runErr := cmd.Run()
	var v [3]uint64
	if _, err := syscall.Read(int(fd), (*[24]byte)(unsafe.Pointer(&v[0]))[:]); err != nil {
		return 0, fmt.Errorf("read the counter: %w", err)
	}
	if runErr != nil {
		return 0, runErr
	}
	if v[1] == 0 || v[2] < v[1] {
		return 0, errors.New("the counter was multiplexed; the count would be scaled, not counted")
	}
	return v[0], nil
}
