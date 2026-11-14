//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// lockDev takes the workspace and release's lock (flock, not
// blocking); errRunning when another daemon holds it. The kernel gives
// it back when the process ends, crash included, so a dead daemon leaves
// no lock. The file stays: removing a lock file races with its next
// holder.
func lockDev(path string) (unlock func(), err error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errRunning
		}
		return nil, err
	}
	return func() { file.Close() }, nil
}
