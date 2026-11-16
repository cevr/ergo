//go:build linux

package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// testRoots reports whether root is the user's cache of a Go test that
// runs this process: whether this process or an ancestor is a Go test
// binary (testBinary) whose environment as it was started, which
// /proc/PID/environ keeps whatever the process sets later, resolves to
// root without ERGO_CACHE. It names that test, and stops at the first
// process it cannot read. Prior art: Bazel's test runner and cmd/go's
// script tests give each test a cache of its own (TEST_TMPDIR; GOCACHE
// in the script's environment); this rule refuses the escape instead of
// trusting each test to set it.
func testRoots(root string) (string, bool) { return testRootsIn("/proc", os.Getpid(), root) }

// testRootsIn is testRoots from the process pid, with the proc file system
// at proc.
func testRootsIn(proc string, pid int, root string) (string, bool) {
	for range 256 {
		if pid <= 1 {
			return "", false
		}
		dir := filepath.Join(proc, strconv.Itoa(pid))
		if testBinary(dir) {
			if environ, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
				if user, err := resolveRoot(lookupIn(environ), false); err == nil && user == root {
					return "pid " + strconv.Itoa(pid), true
				}
			}
		}
		if pid = parentOf(dir); pid == 0 {
			return "", false
		}
	}
	return "", false
}

// testBinary reports whether the process at dir is a Go test binary: its
// program's name ends in .test, as go test names it, or an argument is a
// testing flag (-test.…).
func testBinary(dir string) bool {
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	if strings.HasSuffix(filepath.Base(args[0]), ".test") {
		return true
	}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-test.") {
			return true
		}
	}
	return false
}

// parentOf is the parent's pid of the process at dir, from its stat line
// (the field after the state, which follows the last ')'), or 0.
func parentOf(dir string) int {
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return 0
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// lookupIn is a getenv over a NUL-separated environment block.
func lookupIn(environ []byte) func(string) string {
	return func(name string) string {
		for _, entry := range strings.Split(string(environ), "\x00") {
			if key, value, ok := strings.Cut(entry, "="); ok && key == name {
				return value
			}
		}
		return ""
	}
}
