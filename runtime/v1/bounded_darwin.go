package v1

import (
	"os"
	"syscall"
	"unsafe"
)

// physicalPath is the path macOS gives for f's descriptor (F_GETPATH),
// else path resolved (absResolved).
func physicalPath(f *os.File, path string) string {
	var buf [1024]byte // MAXPATHLEN
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno == 0 {
		for i, b := range buf {
			if b == 0 {
				return string(buf[:i])
			}
		}
	}
	return absResolved(path)
}
