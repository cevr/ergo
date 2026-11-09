//go:build linux

package producer

import (
	"io"
	"os"
	"syscall"
)

// openImage opens the executing image through procfs, which names the file
// the kernel mapped rather than any installation pathname.
func openImage() (io.ReadCloser, error) { return os.Open("/proc/self/exe") }

// imageKey is the image memo's key of an opened image: the kernel's stat of
// the open file (not of a pathname), so a file replaced since the process
// started is not the one keyed. A reader that is not an open file has no
// key.
func imageKey(image io.Reader) (string, bool) {
	file, ok := image.(*os.File)
	if !ok {
		return "", false
	}
	var st syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &st) != nil {
		return "", false
	}
	return statKey(uint64(st.Dev), uint64(st.Ino), int64(st.Size), int64(st.Mtim.Sec), int64(st.Mtim.Nsec), int64(st.Ctim.Sec), int64(st.Ctim.Nsec)), true
}
