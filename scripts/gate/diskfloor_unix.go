//go:build unix

package main

import "syscall"

// statFree is the free bytes of the file system under path that an
// unprivileged process may use.
func statFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
