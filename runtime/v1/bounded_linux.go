package v1

import (
	"os"
	"path/filepath"
	"strconv"
)

// physicalPath is the path Linux gives for f's descriptor
// (/proc/self/fd), else path resolved (absResolved).
func physicalPath(f *os.File, path string) string {
	if p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd()))); err == nil && filepath.IsAbs(p) {
		return p
	}
	return absResolved(path)
}
