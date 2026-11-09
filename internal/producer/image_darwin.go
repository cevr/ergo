//go:build darwin

package producer

import (
	"encoding/hex"
	"io"
	"os"
	"syscall"
	"unsafe"
)

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
// The file is read and verified on the first Read, not here, so an image
// memo hit (imageKey) costs one open and one fstat; a failed verification
// is that Read's error, with the same reason it had here.
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
	return &executableImage{file: file, cdhash: cdhash}, nil
}

// executableImage is the file at the executable's path and the kernel's cdhash
// of the executing image; it reads as the authenticated code.
type executableImage struct {
	file   *os.File
	cdhash [cdhashSize]byte
	code   io.ReadCloser
	err    error
}

func (s *executableImage) Read(p []byte) (int, error) {
	if s.code == nil && s.err == nil {
		s.code, s.err = s.verify()
	}
	if s.err != nil {
		return 0, s.err
	}
	return s.code.Read(p)
}

func (s *executableImage) verify() (io.ReadCloser, error) {
	image, err := io.ReadAll(io.LimitReader(s.file, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(image) > maxImageBytes {
		return nil, errImageTooLarge
	}
	return authenticatedCode(image, s.cdhash[:])
}

func (s *executableImage) Close() error { return s.file.Close() }

// imageKey is the image memo's key of a signed image: the kernel's cdhash of
// the executing image, then the kernel's stat of the opened file, with the
// fields Linux keys (statKey). The cdhash binds the digest: the code
// digested is the code whose SHA-256 page hashes the CodeDirectory with that
// cdhash lists (verifyCodeSignature rejects weaker ones), so a process
// executing other code has another cdhash and is hashed again, even if the
// file at its path was verified by an earlier process. The stat binds the
// file as on Linux: Darwin lets a running image be written, but any write
// sets the change time, which no caller can set back, and a replaced file
// is another inode.
func imageKey(image io.Reader) (string, bool) {
	signed, ok := image.(*executableImage)
	if !ok {
		return "", false
	}
	var st syscall.Stat_t
	if syscall.Fstat(int(signed.file.Fd()), &st) != nil {
		return "", false
	}
	return hex.EncodeToString(signed.cdhash[:]) + ":" + statKey(uint64(uint32(st.Dev)), st.Ino, st.Size, st.Mtimespec.Sec, st.Mtimespec.Nsec, st.Ctimespec.Sec, st.Ctimespec.Nsec), true
}
