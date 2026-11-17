//go:build unix

package main

import (
	"errors"
	"fmt"
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

// ownDir refuses a folder that is not a real directory of this user's
// that only this user can write: another user's folder could hold a
// socket of theirs at a daemon's path.
func ownDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return errors.New("not a directory")
	case !ok || int(st.Uid) != os.Getuid():
		return errors.New("owned by another user")
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("writable by others (%v)", info.Mode().Perm())
	}
	return nil
}
