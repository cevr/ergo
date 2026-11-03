package main

import "syscall"

// File system magic numbers of statfs(2) for file systems in memory.
const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458f6
)

// memoryFileSystem reports whether dir is on tmpfs or ramfs, whose files
// are memory charged to the process that writes them.
func memoryFileSystem(dir string) (bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return false, err
	}
	switch uint32(stat.Type) {
	case tmpfsMagic, ramfsMagic:
		return true, nil
	}
	return false, nil
}
