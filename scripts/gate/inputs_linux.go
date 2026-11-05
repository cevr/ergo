package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// checkInputs reads every entry of each declared read-only tree and
// returns the masks the run needs, or an error naming a path that cannot
// be masked (planMasks). An entry is found when a process of this user
// could write it: owned by this user with a write bit, group-writable with
// a group of this user, writable by others, or writable by access(2) (an
// ACL); when it is a directory this user cannot read; or when it marks a Go
// cache (goCacheRoot), whose root is found. A symbolic link is not read:
// its directory's entry decides. A tree that does not exist is left to
// bwrap, which refuses to bind it.
func checkInputs(trees []sandboxPath, programs []string) ([]sandboxMask, error) {
	uid := uint32(os.Getuid())
	groups := map[uint32]bool{uint32(os.Getgid()): true}
	if list, err := os.Getgroups(); err == nil {
		for _, g := range list {
			groups[uint32(g)] = true
		}
	}
	var findings []inputFinding
	for _, tree := range trees {
		if _, err := os.Lstat(tree.Path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		found := func(path string, dir bool, reason string) {
			findings = append(findings, inputFinding{tree: tree, path: path, dir: dir, reason: reason})
		}
		err := filepath.WalkDir(tree.Path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				// A directory that cannot be listed, or an entry that
				// cannot be read: its directory is masked.
				if entry == nil || entry.IsDir() {
					found(path, true, "a directory this user cannot read")
					return filepath.SkipDir
				}
				found(filepath.Dir(path), true, "a directory this user cannot read")
				return filepath.SkipDir
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				found(filepath.Dir(path), true, "a directory whose entries this user cannot read")
				return filepath.SkipDir
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				found(path, entry.IsDir(), "an entry without an owner")
				return skip(entry)
			}
			if userWritable(stat, uid, groups, path) {
				found(path, entry.IsDir(), "writable by this user")
				return skip(entry)
			}
			if !entry.IsDir() {
				if root, ok := goCacheRoot(path, readHead); ok {
					found(root, true, "a Go cache")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return planMasks(findings, trees, programs)
}

func skip(entry fs.DirEntry) error {
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// userWritable reports whether a process of this user could write the
// entry: by its mode bits, which the owner can also change, or by
// access(2), which reads ACLs.
func userWritable(stat *syscall.Stat_t, uid uint32, groups map[uint32]bool, path string) bool {
	mode := stat.Mode
	switch {
	case stat.Uid == uid && mode&0o200 != 0:
		return true
	case groups[stat.Gid] && mode&0o020 != 0:
		return true
	case mode&0o002 != 0:
		return true
	}
	return syscall.Access(path, 2) == nil
}

// readHead reads the first bytes of a file, or nothing.
func readHead(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	head := make([]byte, len(goCacheReadme))
	n, _ := io.ReadFull(file, head)
	return slices.Clip(head[:n])
}
