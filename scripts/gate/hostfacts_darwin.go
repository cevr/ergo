package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
)

// kernelFacts are the kernel release and version string (sysctl).
func kernelFacts() (release, version string, err error) {
	if release, err = syscall.Sysctl("kern.osrelease"); err != nil {
		return "", "", err
	}
	if version, err = syscall.Sysctl("kern.version"); err != nil {
		return "", "", err
	}
	return release, version, nil
}

// fileSystemType is the statfs(2) type name of the file system that holds
// name, or of its nearest existing ancestor.
func fileSystemType(name string) (string, error) {
	for {
		var stat syscall.Statfs_t
		err := syscall.Statfs(name, &stat)
		if err == nil {
			return fmt.Sprintf("%s (%#x)", cString(stat.Fstypename[:]), stat.Type), nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(name) == name {
			return "", err
		}
		name = filepath.Dir(name)
	}
}

// openFileLimit is RLIMIT_NOFILE, soft and hard.
func openFileLimit() (string, error) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %d", limit.Cur, limit.Max), nil
}

// cgroupFacts: darwin has no cgroups.
func cgroupFacts() (facts [][2]string, problems []string) {
	return [][2]string{{"cgroup file system", "none on darwin"}}, nil
}
