//go:build darwin

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

// sysRenameatxNP is SYS_RENAMEATX_NP, the system call behind libc's
// renameatx_np and renamex_np (xnu's syscalls.master; golang.org/x/sys/unix
// zsysnum_darwin_{amd64,arm64}.go).
const sysRenameatxNP = 488

// renameSwap is RENAME_SWAP, renameatx_np's flag that swaps the two paths
// (<stdio.h>).
const renameSwap = 0x2

// atFDCWDDarwin is macOS's AT_FDCWD: a path relative to the working
// directory.
const atFDCWDDarwin = -2

// exchangePaths swaps the files at a and b with one renameatx_np system
// call (RENAME_SWAP). Go's syscall package does not bind it, so it is
// the exported syscall.Syscall6 with its number. On darwin that function
// is not libc: Go 1.27's syscall/asm_darwin_arm64.s:52-75 traps directly
// (number in X16, the six arguments in X0-X5, SVC $0x80, carry set on
// error) and asm_darwin_amd64.s:36-47 the same (DI, SI, DX, R10, R8, R9;
// AX = number + 0x2000000, SYSCALL), so the five arguments arrive in the
// registers the kernel reads. syscall's own wrappers (zsyscall_darwin_*.go)
// go through libc instead, by the lowercase syscall.syscall6 and a
// trampoline; Apple promises only that libc interface, so this body
// rests on xnu's system call numbers, which Go's Syscall6 rests on too.
// No assembly and no linkname of ours, so the runtime stays a package of
// Go files that every build path, the direct one included, compiles. A
// file system without RENAME_SWAP (ENOTSUP, EINVAL) is errNoExchange.
func exchangePaths(a string, b string) error {
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	fd := atFDCWDDarwin
	_, _, errno := syscall.Syscall6(sysRenameatxNP, uintptr(fd), uintptr(unsafe.Pointer(pa)), uintptr(fd), uintptr(unsafe.Pointer(pb)), renameSwap, 0)
	switch errno {
	case 0:
		return nil
	case syscall.ENOTSUP, syscall.EINVAL, syscall.ENOSYS:
		return errNoExchange
	}
	return &os.LinkError{Op: "exchange", Old: a, New: b, Err: errno}
}
