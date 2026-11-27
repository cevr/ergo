//go:build !darwin

package hostfs

import "syscall"

// Getfsstat returns syscall.ENOSYS on non-Darwin platforms; Linux reads
// /proc/self/mountinfo directly.
func Getfsstat() ([]MountStat, error) {
	return nil, syscall.ENOSYS
}
