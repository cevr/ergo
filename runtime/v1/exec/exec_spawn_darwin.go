//go:build darwin

package exec

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

// The struct kinfo_proc of Darwin's <sys/sysctl.h> for a 64-bit process
// (x/sys/unix KinfoProc, SizeofKinfoProc 0x288): its kp_proc, a struct
// extern_proc, starts with p_un, whose p_starttime is a struct timeval
// (tv_sec int64 at 0, tv_usec int32 at 8), and holds p_stat, a char, at 36.
const (
	execKinfoSize  = 648
	execKinfoStat  = 36
	execStatZombie = 5 // SZOMB of <sys/proc.h>
	execCtlKern    = 1 // CTL_KERN
	execKernProc   = 14
	execProcPid    = 1 // KERN_PROC_PID
)

// execKinfo is the kinfo_proc of pid, read with the sysctl
// kern.proc.pid.<pid>; ok is false when the process has no entry (the
// sysctl then gives no bytes).
func execKinfo(pid int) (k [execKinfoSize]byte, ok bool) {
	mib := [4]int32{execCtlKern, execKernProc, execProcPid, int32(pid)}
	n := uintptr(execKinfoSize)
	if execSysctl(mib[:], &k[0], &n) != nil {
		return k, false
	}
	return k, n == execKinfoSize
}

// execSysctl is the one unsafe edge of exec's detached start: the __sysctl
// system call reads mib and writes at most *n bytes into old, and sets *n
// to the bytes it wrote. mib, old and n point to live Go values that the
// caller keeps for the call, converted in the call expression
// (unsafe.Pointer rule 4). The trap is Go's own syscall path (no cgo), as
// for term's ioctl.
func execSysctl(mib []int32, old *byte, n *uintptr) error {
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(old)), uintptr(unsafe.Pointer(n)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// execKinfoStart is the start time of a kinfo_proc, p_starttime, in
// microseconds after the Unix epoch.
func execKinfoStart(k *[execKinfoSize]byte) int64 {
	sec := int64(binary.NativeEndian.Uint64(k[0:8]))
	usec := int64(int32(binary.NativeEndian.Uint32(k[8:12])))
	return sec*1_000_000 + usec
}

// execStartTime is the start time of the process pid in microseconds
// after the Unix epoch (kinfo_proc p_starttime), or 0 when the process has
// no entry.
func execStartTime(pid int) int64 {
	k, ok := execKinfo(pid)
	if !ok {
		return 0
	}
	return execKinfoStart(&k)
}

// execRunning reports that the process pid exists, has not ended (a
// zombie, p_stat SZOMB, has) and started at start.
func execRunning(pid int, start int64) bool {
	k, ok := execKinfo(pid)
	if !ok || k[execKinfoStat] == execStatZombie {
		return false
	}
	return execKinfoStart(&k) == start
}
