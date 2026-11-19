//go:build linux && (amd64 || arm64)

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

// renameExchange is RENAME_EXCHANGE, renameat2's flag that swaps the two
// paths (linux/fs.h).
const renameExchange = 1 << 1

// atFDCWD is AT_FDCWD: a path relative to the working directory.
const atFDCWD = -0x64

// exchangePaths swaps the files at a and b with one renameat2 call. A
// kernel without renameat2 (ENOSYS) or a file system without
// RENAME_EXCHANGE (EINVAL, which renameat2 also gives for a directory
// swapped with a folder inside it) is errNoExchange.
func exchangePaths(a string, b string) error {
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	fd := atFDCWD
	_, _, errno := syscall.Syscall6(sysRenameat2, uintptr(fd), uintptr(unsafe.Pointer(pa)), uintptr(fd), uintptr(unsafe.Pointer(pb)), renameExchange, 0)
	switch errno {
	case 0:
		return nil
	case syscall.ENOSYS, syscall.EINVAL:
		return errNoExchange
	}
	return &os.LinkError{Op: "exchange", Old: a, New: b, Err: errno}
}
