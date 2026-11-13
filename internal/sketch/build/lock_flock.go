//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package build

import (
	"errors"
	"os"
	"syscall"
)

// leasesSupported reports that this platform has flock(2), so a private
// root carries an owner's lease that the kernel releases when the owner
// exits, and a sweep can prove that a root has no live owner.
const leasesSupported = true

// lockShared takes a shared lock on f, waiting while an exclusive one is
// held.
func lockShared(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// lockExclusive takes an exclusive lock on f, waiting while another lock
// is held.
func lockExclusive(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// tryLockExclusive takes an exclusive lock on f without waiting, and
// reports whether it did.
func tryLockExclusive(f *os.File) bool {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			return err == nil
		}
	}
}
