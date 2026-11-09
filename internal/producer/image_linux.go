//go:build linux

package producer

import (
	"fmt"
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
	return fmt.Sprintf("%d:%d:%d:%d.%09d:%d.%09d", st.Dev, st.Ino, st.Size, st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec), true
}
