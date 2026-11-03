package main

import (
	"fmt"
	"os"
	"syscall"
)

// diskFileSystems are the file systems, by statfs(2) magic number, that keep
// a regular file's pages on disk: page cache the kernel writes back and
// reclaims, never memory that stays charged to the gate. The list fails
// closed: tmpfs, ramfs, overlayfs (whose upper directory can be tmpfs), FUSE
// and every file system not named here are refused.
var diskFileSystems = map[uint32]string{
	0xEF53:     "ext4",
	0x58465342: "xfs",
	0x9123683E: "btrfs",
	0xF2F52010: "f2fs",
	0x2FC12FC1: "zfs",
}

// otherFileSystems name the refused file systems a refusal most likely meets.
var otherFileSystems = map[uint32]string{
	0x01021994: "tmpfs",
	0x858458F6: "ramfs",
	0x794C7630: "overlayfs",
	0x65735546: "fuse",
	0x6969:     "nfs",
}

// outputDestination admits file as a place the gate writes step output to,
// judged on the opened file itself, never on a path: a pipe, a terminal, a
// socket or a device holds no step output in the gate's memory, and a
// regular file only on a file system of diskFileSystems. A refusal names the
// file system it found; the caller adds how to fix it.
func outputDestination(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("cannot be checked: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	connection, err := file.SyscallConn()
	if err != nil {
		return fmt.Errorf("cannot be checked: %w", err)
	}
	var stat syscall.Statfs_t
	var statErr error
	if err := connection.Control(func(fd uintptr) { statErr = syscall.Fstatfs(int(fd), &stat) }); err != nil {
		return fmt.Errorf("cannot be checked: %w", err)
	}
	if statErr != nil {
		return fmt.Errorf("cannot be checked: fstatfs: %w", statErr)
	}
	magic := uint32(stat.Type)
	if _, ok := diskFileSystems[magic]; ok {
		return nil
	}
	name := otherFileSystems[magic]
	if name == "" {
		name = "unknown"
	}
	return fmt.Errorf("is on file system %s (magic %#x), which the gate does not know to keep files on disk (it admits ext4, xfs, btrfs, f2fs and zfs); a file in memory would charge step output to the gate", name, magic)
}
