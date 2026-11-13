package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// startInitBytes bounds what the module's own packages allocate before
// main runs: package-level values only, every table a command needs built
// on its first use (docs/tooling.md, "Process start"). Their inits make
// about 45 KiB today, against 1.1 MiB before the start receipt
// (docs/receipts/compile-speed.md, "Process start").
const startInitBytes = 128 << 10

// TestStartBuildsNoTables: `ergo version` starts with the module's
// packages allocating at most startInitBytes in their inits (Go's
// GODEBUG=inittrace=1 lines, one per package). Every ergo process pays its
// inits, a warm answer included, whose budget is a millisecond. It kills
// start-gomod-eager and start-bindings-eager. It builds Go because the
// question is a process's start: a test binary has already run every init
// before the test begins.
func TestStartBuildsNoTables(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	command := exec.Command(bin, "version")
	command.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	_, stderr, code := runSmokeCommand(t, command)
	if code != 0 {
		t.Fatalf("ergo version: exit %d: %s", code, stderr)
	}
	total, lines := initBytes(t, stderr)
	if lines == 0 {
		t.Fatalf("no inittrace line for the module's packages:\n%s", stderr)
	}
	if total > startInitBytes {
		t.Fatalf("the module's packages allocate %d bytes at init, more than %d:\n%s", total, startInitBytes, moduleInitLines(stderr))
	}
}

// initBytes sums the bytes of the inittrace lines of the module's
// packages ("init PKG @T ms, C ms clock, B bytes, A allocs").
func initBytes(t *testing.T, trace []byte) (int, int) {
	t.Helper()
	total, lines := 0, 0
	for _, line := range strings.Split(moduleInitLines(trace), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		for i := 1; i < len(fields); i++ {
			if strings.TrimSuffix(fields[i], ",") == "bytes" {
				n, err := strconv.Atoi(fields[i-1])
				if err != nil {
					t.Fatalf("inittrace line %q: %v", line, err)
				}
				total += n
				lines++
			}
		}
	}
	return total, lines
}

// moduleInitLines are the inittrace lines of the module's packages.
func moduleInitLines(trace []byte) string {
	var b strings.Builder
	s := bufio.NewScanner(bytes.NewReader(trace))
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "init ergo.local/prototype/") {
			b.WriteString(s.Text() + "\n")
		}
	}
	return b.String()
}
