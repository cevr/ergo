//go:build darwin

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

// renameSwap is RENAME_SWAP, renamex_np's flag that swaps the two paths
// (<stdio.h>).
const renameSwap = 0x2

// exchangePaths swaps the files at a and b with one renamex_np call
// (RENAME_SWAP). Go's syscall package does not bind renamex_np, so it is
// called through libc as golang.org/x/sys/unix calls it: a trampoline
// (exchange_darwin.s) that jumps to the libSystem symbol, run by
// syscall's own libc caller. A file system without RENAME_SWAP (ENOTSUP,
// EINVAL) is errNoExchange.
func exchangePaths(a string, b string) error {
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	_, _, errno := syscall_syscall(libc_renamex_np_trampoline_addr, uintptr(unsafe.Pointer(pa)), uintptr(unsafe.Pointer(pb)), renameSwap)
	switch errno {
	case 0:
		return nil
	case syscall.ENOTSUP, syscall.EINVAL, syscall.ENOSYS:
		return errNoExchange
	}
	return &os.LinkError{Op: "exchange", Old: a, New: b, Err: errno}
}

// syscall_syscall is syscall's libc caller on darwin: it calls fn with
// three arguments on the system stack and gives back errno when fn
// returns -1.
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// libc_renamex_np_trampoline_addr is the address of the trampoline to
// libSystem's renamex_np (exchange_darwin.s).
var libc_renamex_np_trampoline_addr uintptr

//go:cgo_import_dynamic libc_renamex_np renamex_np "/usr/lib/libSystem.B.dylib"
