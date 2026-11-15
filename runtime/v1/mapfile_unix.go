//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package v1

import (
	"os"
	"syscall"
)

// fsMap maps size bytes of f read-only and shared (FSMapFile's mapping);
// the mapping is never unmapped.
func fsMap(f *os.File, size int) ([]byte, error) {
	return syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
}
