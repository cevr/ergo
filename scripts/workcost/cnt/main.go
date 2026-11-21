//go:build linux

// Command cnt runs a command and prints what it cost as counts: the
// user-mode instructions of the command and its children
// (perf_event_open with inherit, started at the exec), its user and
// system CPU time and peak resident set (rusage), and its wall time,
// labelled as loaded (the host is shared; the wall time is not a claim).
// scripts/workcost runs each step of the receipt through it.
//
//	usage: cnt [-l LABEL] -- CMD ARGS...
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// perfAttr is struct perf_event_attr up to sample_max_stack (version 5).
type perfAttr struct {
	Type, Size       uint32
	Config           uint64
	Sample           uint64
	SampleType       uint64
	ReadFormat       uint64
	Bits             uint64
	Wakeup, BpType   uint32
	Ext1, Ext2       uint64
	BranchSampleType uint64
	SampleRegsUser   uint64
	SampleStackUser  uint32
	ClockID          int32
	SampleRegsIntr   uint64
	AuxWatermark     uint32
	SampleMaxStack   uint16
	Reserved2        uint16
}

func main() {
	args := os.Args[1:]
	label := ""
	if len(args) > 1 && args[0] == "-l" {
		label, args = args[1], args[2:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cnt [-l LABEL] -- CMD ARGS...")
		os.Exit(2)
	}
	// The counter is opened on this thread with inherit and
	// enable_on_exec: the child inherits it, and it starts at its exec.
	runtime.LockOSThread()
	// PERF_TYPE_HARDWARE, PERF_COUNT_HW_INSTRUCTIONS; bits: disabled,
	// inherit, exclude_kernel, exclude_hv, enable_on_exec.
	attr := perfAttr{Type: 0, Config: 1, Bits: 1<<0 | 1<<1 | 1<<5 | 1<<6 | 1<<12}
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	if errno != 0 {
		fmt.Fprintln(os.Stderr, "cnt: perf_event_open:", errno)
		fd = ^uintptr(0)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	var instructions uint64
	if fd != ^uintptr(0) {
		var b [8]byte
		if _, err := syscall.Read(int(fd), b[:]); err == nil {
			instructions = binary.LittleEndian.Uint64(b[:])
		}
	}
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	if cmd.ProcessState == nil {
		fmt.Fprintln(os.Stderr, "cnt:", err)
		os.Exit(127)
	}
	ru := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	user := time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond
	sys := time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond
	load, _ := os.ReadFile("/proc/loadavg")
	if len(load) > 5 {
		load = load[:5]
	}
	fmt.Printf("%s\texit=%d\tuser_instr=%d\tutime_ms=%.1f\tstime_ms=%.1f\tmaxrss_kb=%d\twall_ms(loaded)=%.1f\tload=%s\n", label, code, instructions, float64(user.Microseconds())/1000, float64(sys.Microseconds())/1000, ru.Maxrss, float64(wall.Microseconds())/1000, load)
	os.Exit(code)
}
