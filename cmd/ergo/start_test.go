package main

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// startInitBytes bounds what the module's own packages allocate before
// main runs: package-level values only, every table a command needs built
// on its first use (docs/tooling.md, "Process start"). Their inits make
// 69,592 B today with package main, measured after merging main
// 09269406d, whose new eager tables in internal/sketch/lower, fix, lint
// and experimental/irspike raised it from 14,464 B (ERGO-STARTUP-2:
// 61,624 B before the maps were built on first use; the limit is the
// measurement plus a 2 KiB margin, rounded up to a KiB), against 1.1 MiB
// before the start receipt
// (docs/receipts/compile-speed.md, "Process start"); the std binding
// catalog alone, built eagerly, adds 41 KiB.
const startInitBytes = 70 << 10

// TestStartBuildsNoTables: `ergo version` starts with the module's
// packages allocating at most startInitBytes in their inits (Go's
// GODEBUG=inittrace=1 lines, one per package, cmd/ergo's own `init main`
// included: it held 18 KiB of tables, regexps among them, that the rule
// did not see before ERGO-STARTUP-2). Every ergo process pays its
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

// TestToolLinksNoCgo: the ergo tool is a static binary built without cgo
// (internal/ergobuild): its build settings say CGO_ENABLED=0, it links no
// runtime/cgo, and on Linux it has no program interpreter (the dynamic
// loader every start of a cgo ergo runs, 0.46 M of `ergo version`'s 3.10 M;
// ERGO-STARTUP). So a dependency that would bring cgo back fails here, not
// in a receipt. It counts symbols and headers, never time. It builds Go
// because the question is the linked tool. Mutant: the setting dropped
// (start-tool-built-with-cgo).
func TestToolLinksNoCgo(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	cgo := ""
	for _, s := range info.Settings {
		if s.Key == "CGO_ENABLED" {
			cgo = s.Value
		}
	}
	if cgo != "0" {
		t.Errorf("%s was built with CGO_ENABLED=%q, want 0 (internal/ergobuild)", bin, cgo)
	}
	for _, dep := range info.Deps {
		if dep.Path == "runtime/cgo" {
			t.Errorf("%s links runtime/cgo", bin)
		}
	}
	if runtime.GOOS != "linux" {
		return
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Errorf("%s is dynamically linked (it names a program interpreter); every start pays the dynamic loader", bin)
		}
	}
	if syms, err := f.Symbols(); err == nil {
		for _, s := range syms {
			if strings.HasPrefix(s.Name, "runtime/cgo.") || s.Name == "x_cgo_init" {
				t.Errorf("%s links cgo's runtime (%s)", bin, s.Name)
				break
			}
		}
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

// moduleInitLines are the inittrace lines of the module's packages, the
// command's package main among them.
func moduleInitLines(trace []byte) string {
	var b strings.Builder
	s := bufio.NewScanner(bytes.NewReader(trace))
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "init ergo.local/prototype/") || strings.HasPrefix(s.Text(), "init main ") {
			b.WriteString(s.Text() + "\n")
		}
	}
	return b.String()
}
