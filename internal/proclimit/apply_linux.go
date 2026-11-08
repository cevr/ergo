//go:build linux

package proclimit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// apply sets the oom_score_adj and RLIMIT_DATA of process pid, a child the
// caller started and has not yet waited for. RLIMIT_DATA (Linux 4.7 on)
// counts every private writable mapping, which is where the Go runtime
// maps its heap and goroutine stacks; its address space reservations are
// PROT_NONE and do not count, so the limit is close to the heap a program
// may hold. A child that has already ended (a zombie) has nothing left to
// bound: the kernel then gives its /proc files to root and refuses the
// write, and apply returns nil.
func apply(pid int, limit uint64) error {
	err := set(pid, limit)
	if err != nil && ended(pid) {
		return nil
	}
	return err
}

func set(pid int, limit uint64) error {
	// A read-only /proc (a sandbox that mounts everything read-only, as the
	// closure sweep does) leaves the score as it is; the memory limit,
	// which needs no /proc, still holds.
	if err := writeProc("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", strconv.Itoa(OOMScore)); err != nil && !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("raise oom_score_adj: %w", err)
	}
	if limit == 0 {
		return nil
	}
	rl := syscall.Rlimit{Cur: limit, Max: limit}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), uintptr(syscall.RLIMIT_DATA),
		uintptr(unsafe.Pointer(&rl)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("prlimit(RLIMIT_DATA, %s): %w", Format(limit), errno)
	}
	return nil
}

// writeProc writes a /proc file with system calls, not package os, whose
// test log would make the file an input of Go's test cache: a /proc file
// is new on each run, so the result of a test that runs a program would
// never be cached.
func writeProc(path, value string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(value)); err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	return nil
}

// ended reports whether process pid is exiting or has exited: it has
// released its memory, so /proc/PID/status shows no VmSize (the kernel
// gives the process's /proc files to root from that point), or the file is
// gone. Start returns after the exec, so a running program always has its
// memory.
func ended(pid int) bool {
	fd, err := syscall.Open("/proc/"+strconv.Itoa(pid)+"/status", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return true
	}
	defer syscall.Close(fd)
	status := make([]byte, 16<<10)
	n, err := syscall.Read(fd, status)
	return err != nil || !bytes.Contains(status[:max(n, 0)], []byte("\nVmSize:"))
}
