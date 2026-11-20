//go:build linux

package build

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
//
// A process walks its ancestors once (testAncestors): their command lines
// and starting environments do not change while it runs, and every ergo
// command asks at least twice (main, then the command), each walk a few
// /proc reads per ancestor (ERGO-STARTUP).
func testRoots(root string) (string, bool) { return matchTestRoot(testAncestors(), root) }

// procWalks counts the walks of this process's ancestors, for the test
// that each process walks once.
var procWalks atomic.Int64

// testAncestors is the walk of this process's ancestors, made on first use.
var testAncestors = sync.OnceValue(func() []testAncestor {
	procWalks.Add(1)
	return testAncestorsIn("/proc", os.Getpid())
})

// testAncestor is a Go test binary among a process's ancestors (itself
// included): its pid, and the cache root its starting environment resolves
// to without ERGO_CACHE ("" when it resolves to none).
type testAncestor struct {
	pid  int
	user string
}

// testRootsIn is testRoots from the process pid, with the proc file system
// at proc.
func testRootsIn(proc string, pid int, root string) (string, bool) {
	return matchTestRoot(testAncestorsIn(proc, pid), root)
}

// matchTestRoot names the first of tests, nearest first, whose user root is
// root.
func matchTestRoot(tests []testAncestor, root string) (string, bool) {
	for _, t := range tests {
		if t.user != "" && t.user == root {
			return "pid " + strconv.Itoa(t.pid), true
		}
	}
	return "", false
}

// testAncestorsIn walks from the process pid up, at most 256 processes,
// to init or the first process it cannot read, and keeps the Go test
// binaries, nearest first.
func testAncestorsIn(proc string, pid int) []testAncestor {
	var tests []testAncestor
	for range 256 {
		if pid <= 1 {
			return tests
		}
		dir := proc + "/" + strconv.Itoa(pid)
		if testBinary(dir) {
			t := testAncestor{pid: pid}
			if environ, err := readProc(dir + "/environ"); err == nil {
				if user, err := resolveRoot(lookupIn(environ), false); err == nil {
					t.user = user
				}
			}
			tests = append(tests, t)
		}
		if pid = parentOf(dir); pid == 0 {
			return tests
		}
	}
	return tests
}

// testBinary reports whether the process at dir is a Go test binary: its
// program's name ends in .test, as go test names it, or an argument is a
// testing flag (-test.…).
func testBinary(dir string) bool {
	cmdline, err := readProc(dir + "/cmdline")
	if err != nil {
		return false
	}
	cmdline = bytes.TrimSuffix(cmdline, []byte{0})
	program, rest, _ := bytes.Cut(cmdline, []byte{0})
	if bytes.HasSuffix(program, []byte(".test")) {
		return true
	}
	for len(rest) > 0 {
		var arg []byte
		arg, rest, _ = bytes.Cut(rest, []byte{0})
		if bytes.HasPrefix(arg, []byte("-test.")) {
			return true
		}
	}
	return false
}

// parentOf is the parent's pid of the process at dir, from its stat line
// (the field after the state, which follows the last ')'), or 0.
func parentOf(dir string) int {
	stat, err := readProc(dir + "/stat")
	if err != nil {
		return 0
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	// " S PPID ...": skip the state field, then read the digits.
	rest := bytes.TrimLeft(stat[end+1:], " ")
	_, rest, ok := bytes.Cut(rest, []byte{' '})
	if !ok {
		return 0
	}
	rest = bytes.TrimLeft(rest, " ")
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	ppid, _ := strconv.Atoi(string(rest[:n]))
	return ppid
}

// readProc reads the /proc file at path with plain system calls: no
// *os.File, so no poller registration, finalizer or fcntl, which were most
// of the walk's 0.125 M of `ergo version`'s instructions (ERGO-STARTUP-2,
// measured, sampled). It allocates the returned buffer and the path's NUL-terminated copy
// (TestProcReadAllocatesItsBuffer).
func readProc(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	buf := make([]byte, 0, 512)
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := syscall.Read(fd, buf[len(buf):cap(buf)])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return buf, nil
		}
		buf = buf[:len(buf)+n]
	}
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
