package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testLogHeader starts every test log (testing/internal/testdeps).
const testLogHeader = "# test log\n"

// finishTestLog runs in the observing process once the tests have run, and
// puts the complete test log in a fixed order (orderTestLog). cmd/go keys
// the package's result on the log's lines in the order they appear
// (computeTestInputsID), and the test process logs its own reads as they
// happen: the gate's tests run steps concurrently, so two runs with the
// same inputs log them in different orders, and the result never replays. A
// log that is absent or has no final newline is not cached by cmd/go
// (testinputs leaves it so when it cannot observe), so it stays as it is.
func finishTestLog(code int) int {
	name := testLogFile(os.Args[1:])
	if name == "" {
		return code
	}
	content, err := os.ReadFile(name)
	if err != nil || !strings.HasSuffix(string(content), "\n") {
		return code
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate tests: order the test log:", err)
		return 1
	}
	ordered := orderTestLog(string(content), dir)
	if ordered != string(content) {
		if err := os.WriteFile(name, []byte(ordered), 0); err != nil {
			fmt.Fprintln(os.Stderr, "gate tests: order the test log:", err)
			return 1
		}
	}
	return code
}

// orderTestLog returns a test log with its lines sorted and without
// repeats, relative names made absolute against dir, the test's working
// directory, as cmd/go makes them. Each getenv, stat or open line is an
// independent check, so the order changes nothing but the key. A log with a
// chdir line, where a relative name depends on the lines before it, or with
// a line cmd/go would refuse, is returned as it is.
func orderTestLog(log, dir string) string {
	body, ok := strings.CutPrefix(log, testLogHeader)
	if !ok {
		return log
	}
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		op, name, found := strings.Cut(line, " ")
		if !found || (op != "getenv" && op != "stat" && op != "open") {
			return log
		}
		if op != "getenv" && !filepath.IsAbs(name) {
			line = op + " " + filepath.Join(dir, name)
		}
		lines = append(lines, line)
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	return testLogHeader + strings.Join(lines, "\n") + "\n"
}

// testLogFile returns the -test.testlogfile cmd/go passed, or "".
func testLogFile(args []string) string {
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

// TestOrderTestLog checks that two logs of the same reads in different
// orders, with repeats and relative names, become one log, and that a log
// whose meaning depends on its order, or that cmd/go would refuse, is kept.
func TestOrderTestLog(t *testing.T) {
	dir := filepath.FromSlash("/m/scripts/gate")
	first := testLogHeader + strings.Join([]string{
		"getenv PATH",
		"open steps.go",
		"getenv TMPDIR",
		"stat " + filepath.FromSlash("/m/go.mod"),
		"getenv PATH",
	}, "\n") + "\n"
	second := testLogHeader + strings.Join([]string{
		"getenv TMPDIR",
		"stat " + filepath.FromSlash("/m/go.mod"),
		"getenv PATH",
		"open " + filepath.Join(dir, "steps.go"),
		"",
		"getenv TMPDIR",
	}, "\n") + "\n"
	want := testLogHeader + strings.Join([]string{
		"getenv PATH",
		"getenv TMPDIR",
		"open " + filepath.Join(dir, "steps.go"),
		"stat " + filepath.FromSlash("/m/go.mod"),
	}, "\n") + "\n"
	for _, log := range []string{first, second} {
		if got := orderTestLog(log, dir); got != want {
			t.Errorf("orderTestLog(%q) = %q, want %q", log, got, want)
		}
	}
	for _, kept := range []string{
		testLogHeader + "getenv PATH\nchdir /m\nopen go.mod\n",
		testLogHeader + "getenv PATH\nwrite x\n",
		testLogHeader + "getenv PATH\nmalformed\n",
		"getenv PATH\n",
	} {
		if got := orderTestLog(kept, dir); got != kept {
			t.Errorf("orderTestLog(%q) = %q, want it kept", kept, got)
		}
	}
}
