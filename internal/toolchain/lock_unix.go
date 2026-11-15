//go:build unix

package toolchain

import (
	"errors"
	"os"
	"syscall"
)

// locks says whether lock excludes other installs.
const locks = true

// lock takes an exclusive flock on path, the install lock of one release.
// When another process holds it, waiting runs (when set) and lock waits.
// The kernel drops the lock when its holder dies.
func lock(path string, waiting func()) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		if waiting != nil {
			waiting()
		}
		for err = syscall.Flock(fd, syscall.LOCK_EX); errors.Is(err, syscall.EINTR); {
			err = syscall.Flock(fd, syscall.LOCK_EX)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
