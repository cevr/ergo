// Package testinputs makes everything a test's process tree reads an input
// of Go's test cache.
//
// Go's test cache keys a package's result on the files and environment
// variables the test process consulted through package os, which the testing
// package records in a log for cmd/go (cmd/go/internal/test.computeTestInputsID).
// It cannot see what a child process reads: git, sh, a C compiler or a
// go build started by the test or by the code under test. Main closes that
// gap by observation rather than declaration. From TestMain, it runs the test
// binary again as a child that installs a seccomp filter. The filter
// suspends every file system call of interest the child and its descendants
// make until Main has read the call's path (seccomp user notification). When
// the child exits, Main appends to its log one entry per observed access:
//
//   - "open" for a file read or a program run, which cmd/go rechecks by size,
//     mode and modification time, and for a directory listed, which it
//     rechecks by the name and metadata of every entry;
//   - "stat" for a lookup, a failed one included, so a file added later is an
//     input too, for a working directory changed to, for an AF_UNIX socket
//     path, and for every link inside the root on the way to a recorded path;
//   - "getenv" for every variable in the environment of every program run.
//
// When it cannot observe (another operating system, no seccomp listener, a
// call that would hide later accesses, a path the log cannot carry, an
// observer variable inherited without an observer), Main
// leaves the log without its final newline. cmd/go then
// refuses to cache the result (saveOutput), so an unobserved result is never
// replayed, only rerun.
package testinputs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs/testenv"
)

// ToolDigest is the variable through which the gate passes a digest of the
// test tools, the installed dependencies and the test environment
// (testenv.ToolDigest). The parent records it for any test that runs a
// program.
const ToolDigest = testenv.ToolDigest

// Main runs the package's tests under observation and returns the exit code
// for TestMain: os.Exit(testinputs.Main(m)). Code before Main in TestMain
// runs twice, in the observing process and in the test process.
func Main(m *testing.M) int { return MainFunc(m, m.Run) }

// MainFunc is Main for a TestMain that prepares the test process: run does
// the preparation, calls m.Run and returns its exit code. It runs only in
// the process that runs the tests.
func MainFunc(m *testing.M, run func() int) int {
	raiseOOMScore()
	log := testLog(os.Args[1:])
	if observed || log == "" {
		return run() // the observed test process, or cmd/go is not caching
	}
	if impostor != "" {
		// An inherited observer variable with no observer behind it.
		return unobserved(log, run(), impostor)
	}
	root, err := moduleRoot()
	if err != nil {
		return unobserved(log, run(), err.Error())
	}
	code, rec, err := observe(root)
	if err != nil {
		// The child never started, so the tests run here, unobserved.
		return unobserved(log, run(), "tracing is unavailable: "+err.Error())
	}
	if reason := rec.reason(); reason != "" {
		return unobserved(log, code, reason)
	}
	if err := appendLog(log, strings.Join(rec.lines(), "")); err != nil {
		fmt.Fprintln(os.Stderr, "testinputs: record observed inputs:", err)
		return failed(code)
	}
	return code
}

// failed turns a passing exit code into a failing one. cmd/go never caches
// a failure, so a log that could not be completed or marked cannot be
// replayed.
func failed(code int) int {
	if code == 0 {
		return 1
	}
	return code
}

// testLog returns the -test.testlogfile cmd/go passed, or "".
func testLog(args []string) string {
	for i, arg := range args {
		if arg == "--" {
			return ""
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		if name != "-test.testlogfile" && name != "test.testlogfile" {
			continue
		}
		if hasValue {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// moduleRoot returns the directory holding the go.mod above the test's
// working directory, which cmd/go sets to the package directory.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the test directory")
		}
		dir = parent
	}
}

// unobserved leaves the test log without a final newline, so cmd/go does not
// cache the result, and returns the exit code. If the log cannot be marked,
// it still ends as the test process left it, which cmd/go would cache, so a
// pass becomes a failure.
func unobserved(log string, code int, reason string) int {
	reason = strings.ReplaceAll(reason, "\n", " ")
	fmt.Fprintln(os.Stderr, "testinputs: result not cacheable, inputs unobserved:", reason)
	if err := appendLog(log, "testinputs: unobserved: "+reason); err != nil {
		fmt.Fprintln(os.Stderr, "testinputs: mark the test log:", err)
		return failed(code)
	}
	return code
}

// appendLog appends to the log the test process wrote. A missing log was
// never written, and cmd/go caches nothing without one.
func appendLog(log, text string) error {
	file, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_, err = file.WriteString(text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
