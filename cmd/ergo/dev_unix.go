//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// viaShortPath calls use with path, or, when path is longer than a socket
// may be, with /proc/self/fd/N/NAME: N an open descriptor of path's folder
// and NAME its last element, which bind and connect resolve to the same
// file (Linux; on a host without /proc the call fails with the error
// use returns, never on another path).
func viaShortPath(path string, use func(string) error) error {
	if len(path) <= devSocketPathMax {
		return use(path)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return use(fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), filepath.Base(path)))
}
