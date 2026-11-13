package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// goWorkLine is the line `go build -x` prints first, once per invocation,
// also when the target is up to date.
var goWorkLine = regexp.MustCompile(`(?m)^WORK=`)

// TestTestResultCache is the counted receipt of fu-test-bins and g-b2, and
// the law that a cached verdict is the verdict of a rerun. `ergo test
// ./...` over a module of passing packages and one failing package:
//
//   - cold, each package's test binary is linked once;
//   - warm, each passing package replays its cached result, so its run
//     neither invokes the go command nor runs the binary; the failing one,
//     never cached, runs again with no link;
//   - --count 1 runs every package again, with no link, and its output
//     and exit status are the cached run's.
//
// A run that differs in what it reads (a --run name, a GO* variable, the go
// command) is no hit. It kills test-bins-temp-link and the g-b2 cache
// mutants (cache-*), which no other test reaches: none runs a package
// twice.
// It builds Go because the claim is about the go command's invocations and
// link actions, which only the -x trace of a real build shows.
func TestTestResultCache(t *testing.T) {
	if testing.Short() {
		t.Skip("the counts of real test builds run in the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module links\n")
	const passing = 3
	for i := range passing {
		smokeWrite(t, root, fmt.Sprintf("p%d/p.ergo", i), fmt.Sprintf("test A%d { assert %d == %d }\ntest B%d { assert true }\n", i, i, i, i))
	}
	smokeWrite(t, root, "q/q.ergo", "test Fails { assert 1 == 2 }\n")
	type outcome struct {
		stdout                     string
		code, links, goRuns, notes int
	}
	run := func(env []string, args ...string) outcome {
		t.Helper()
		command := exec.Command(bin, append([]string{"test"}, args...)...)
		command.Dir = root
		command.Env = append(append(os.Environ(), "GOFLAGS=-x"), env...)
		out, stderr, code := runSmokeCommand(t, command)
		e := string(stderr)
		return outcome{
			stdout: string(out), code: code,
			links:  strings.Count(e, "/link -o "),
			goRuns: len(goWorkLine.FindAllString(e, -1)),
			notes:  strings.Count(e, cachedNote),
		}
	}
	cold := run(nil, "./...")
	if cold.code != 1 || cold.links != passing+1 || cold.notes != 0 || strings.Count(cold.stdout, "PASS (2 tests)") != passing {
		t.Fatalf("cold run: %+v", cold)
	}
	warm := run(nil, "./...")
	if warm.links != 0 || warm.goRuns != 1 || warm.notes != passing {
		t.Fatalf("warm run: %d links, %d go commands, %d replays; want 0, 1 (the failing package), %d", warm.links, warm.goRuns, warm.notes, passing)
	}
	again := run(nil, "./...", "--count", "1")
	if again.links != 0 || again.notes != 0 || again.goRuns != passing+1 {
		t.Fatalf("--count 1 run: %d links, %d replays, %d go commands; want 0, 0, %d", again.links, again.notes, again.goRuns, passing+1)
	}
	for _, o := range []outcome{warm, again} {
		if o.stdout != cold.stdout || o.code != cold.code {
			t.Fatalf("a cached verdict differs from a run:\nexit %d\n%s\nwant exit %d\n%s", o.code, o.stdout, cold.code, cold.stdout)
		}
	}

	// What the run reads keys its result.
	if one := run(nil, "./p0"); one.code != 0 || one.notes != 0 {
		t.Fatalf("./p0: %+v", one)
	}
	if picked := run(nil, "./p0", "--run", "A0"); picked.notes != 0 || strings.Contains(picked.stdout, "B0") || !strings.Contains(picked.stdout, "ok A0") {
		t.Fatalf("--run A0 after a cached ./p0: %+v", picked)
	}
	if o := run([]string{"GODEBUG=madvdontneed=1"}, "./p0"); o.notes != 0 || o.code != 0 {
		t.Fatalf("another GODEBUG replayed: %+v", o)
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	if err := os.WriteFile(filepath.Join(wrapper, "go"), []byte("#!/bin/sh\nexec "+goCommand+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if o := run([]string{"PATH=" + wrapper + string(os.PathListSeparator) + os.Getenv("PATH")}, "./p0"); o.notes != 0 || o.code != 0 {
		t.Fatalf("another go command replayed: %+v", o)
	}
}
