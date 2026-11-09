//go:build darwin

package producer

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// imageKey: the Darwin image is verified code in memory, not an open file,
// so it has no memo key and is hashed every time.
func imageKey(io.Reader) (string, bool) { return "", false }

// csOpsCDHash asks csops(2) for the cdhash of a process's main executable,
// as xnu's cs_blobs.h numbers it.
const csOpsCDHash = 5

// openImage returns the bytes the identity digests: on Darwin, the executing
// image's authenticated code. Darwin has no /proc/self/exe, and
// os.Executable names the path the process was started from, which may since
// name other bytes. The kernel keeps the cdhash of the image it executes;
// the file at that path counts only if its code signature carries that
// cdhash and its pages match it (verifyCodeSignature), and then only the
// code those pages cover is digested, under its own domain tag
// (authenticatedCode): the signature container and any trailing bytes are
// not authenticated and cannot change the running code. A universal file
// gives a process-scoped identity. The file is read once, so the bytes
// verified are the bytes hashed. A Go binary for
// darwin/arm64 is always signed (the linker signs it ad hoc); an unsigned
// image has no kernel record, and its identity stays process-scoped.
//
// The design relies on xnu answering CS_OPS_CDHASH from the code signature
// blob attached to the process's text vnode, which the kernel keeps while
// that vnode is in use (inferred from xnu's csops and vn_getcdhash, not
// measured directly). Consistent with that, measured on darwin/arm64: a
// signed file rewritten in place and run stays killed after its bytes are
// restored, while a new file with the same bytes runs. Rewriting the file in
// place to another signed build while an old process runs is not measured.
//
// csops is reached by its raw trap number; Go's own darwin packages call
// libSystem instead. A refused trap gives the process-scoped fallback.
func openImage() (io.ReadCloser, error) {
	var cdhash [cdhashSize]byte
	if _, _, errno := syscall.Syscall6(syscall.SYS_CSOPS, uintptr(os.Getpid()), csOpsCDHash, uintptr(unsafe.Pointer(&cdhash[0])), cdhashSize, 0, 0); errno != 0 {
		return nil, errNoSignatureRecord
	}
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	image, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(image) > maxImageBytes {
		return nil, errImageTooLarge
	}
	return authenticatedCode(image, cdhash[:])
}
