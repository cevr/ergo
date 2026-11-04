package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// kernelFacts are the kernel release and version string (uname).
func kernelFacts() (release, version string, err error) {
	var name syscall.Utsname
	if err := syscall.Uname(&name); err != nil {
		return "", "", err
	}
	return cString(name.Release[:]), cString(name.Version[:]), nil
}

// openFileLimit is RLIMIT_NOFILE, soft and hard.
func openFileLimit() (string, error) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %d", limit.Cur, limit.Max), nil
}

// fileSystemType is the statfs(2) magic of the file system that holds name,
// or of its nearest existing ancestor: a directory the gate creates later
// (its cache, the spool directory) gets that file system.
func fileSystemType(name string) (string, error) {
	for {
		var stat syscall.Statfs_t
		err := syscall.Statfs(name, &stat)
		if err == nil {
			return fmt.Sprintf("%#x", uint32(stat.Type)), nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(name) == name {
			return "", err
		}
		name = filepath.Dir(name)
	}
}

// cgroupFacts are the cgroup v2 facts a step's verdict can depend on: the
// file system of the hierarchy, and the controllers the user's service
// manager has and delegates (its cgroup.controllers and
// cgroup.subtree_control). A file that does not exist is the value
// "absent"; a file that cannot be read otherwise is a problem.
func cgroupFacts() (facts [][2]string, problems []string) {
	kind, err := fileSystemType(cgroupRoot)
	if err != nil {
		problems = append(problems, fmt.Sprintf("the file system of %s cannot be read: %v", cgroupRoot, err))
	}
	facts = append(facts, [2]string{"cgroup file system", kind})
	uid := os.Getuid()
	manager := filepath.Join(cgroupRoot, "user.slice", fmt.Sprintf("user-%d.slice", uid), fmt.Sprintf("user@%d.service", uid))
	for _, file := range []string{"cgroup.controllers", "cgroup.subtree_control"} {
		content, err := os.ReadFile(filepath.Join(manager, file))
		value := strings.TrimSpace(string(content))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			value = "absent"
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s cannot be read: %v", filepath.Join(manager, file), err))
		}
		facts = append(facts, [2]string{"user manager " + file, value})
	}
	return facts, problems
}
