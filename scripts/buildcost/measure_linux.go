package main

import (
	"os/exec"
	"runtime"
	"syscall"
	"time"
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

// openCounter opens a counter of the user-mode instructions of the calling
// thread and every process it starts from now on: hardware instructions,
// disabled until the child's exec, inherited, kernel and hypervisor
// excluded. The caller holds its OS thread.
func openCounter() (int, bool) {
	const (
		disabled      = 1 << 0
		inherit       = 1 << 1
		excludeKernel = 1 << 5
		excludeHV     = 1 << 6
		enableOnExec  = 1 << 12
	)
	a := perfAttr{typ: 0, config: 1, readFmt: 3, flags: disabled | inherit | excludeKernel | excludeHV | enableOnExec}
	a.size = uint32(unsafe.Sizeof(a))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&a)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	return int(fd), errno == 0
}

func instructionsCounted() bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fd, ok := openCounter()
	if ok {
		syscall.Close(fd)
	}
	return ok
}

// measure runs cmd and counts its process tree's instructions and times.
func measure(cmd *exec.Cmd) result {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fd, counting := openCounter()
	start := time.Now()
	err := cmd.Run()
	res := result{wall: time.Since(start)}
	if err != nil {
		res.exit = -1
		if ee, ok := err.(*exec.ExitError); ok {
			res.exit = ee.ExitCode()
		}
	}
	if counting {
		var b [3]uint64
		syscall.Read(fd, (*[24]byte)(unsafe.Pointer(&b[0]))[:])
		syscall.Close(fd)
		res.instructions = b[0]
	}
	if cmd.ProcessState != nil {
		res.user, res.sys = cmd.ProcessState.UserTime(), cmd.ProcessState.SystemTime()
	}
	return res
}
