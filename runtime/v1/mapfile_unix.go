//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package v1

import (
	"os"
	"syscall"
)

// fsMap maps size bytes of f read-only and shared (FSMapFile's mapping);
// Mapped.Close unmaps it (fsUnmap).
func fsMap(f *os.File, size int) ([]byte, error) {
	return syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
}

// fsUnmap unmaps a mapping fsMap made.
func fsUnmap(data []byte) { _ = syscall.Munmap(data) }
