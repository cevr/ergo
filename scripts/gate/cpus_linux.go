package main

import (
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// affinityCPUs is the number of CPUs in the calling thread's affinity mask
// (sched_getaffinity), read now: runtime.NumCPU is the process's mask at
// its start.
func affinityCPUs() int {
	var mask [1024]uint64 // 65,536 CPUs
	n, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return runtime.NumCPU()
	}
	count := 0
	for _, word := range mask[:n/8] {
		count += bits.OnesCount64(word)
	}
	return max(1, count)
}

// cgroupCPULimits are the cpu.max contents of this process's cgroup and of
// every cgroup above it that has one. A process in a namespace without the
// cgroup file system (the merge tier's sandbox) finds none.
func cgroupCPULimits() []string {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	var path string
	for _, line := range strings.Split(string(membership), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			path = rest
		}
	}
	if path == "" {
		return nil
	}
	var limits []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if limit, err := readTrimmed(filepath.Join(cgroupRoot, dir, "cpu.max")); err == nil {
			limits = append(limits, limit)
		}
		if dir == "/" || dir == "." {
			return limits
		}
	}
}
