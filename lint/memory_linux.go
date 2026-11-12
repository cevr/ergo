package lint

import (
	"syscall"
	"unsafe"
)

// limitMemory caps the pack's data segment with prlimit(2). RLIMIT_DATA
// counts private writable mappings (Linux 4.7+), which bounds a Go or
// native pack's heap without breaking runtimes that reserve large address
// ranges, as RLIMIT_AS would.
func limitMemory(pid int, bytes uint64) error {
	limit := syscall.Rlimit{Cur: bytes, Max: bytes}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), syscall.RLIMIT_DATA, uintptr(unsafe.Pointer(&limit)), 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
