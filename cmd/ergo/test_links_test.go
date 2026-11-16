package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// readStageReport reads the counters a command wrote to ERGO_STAGE_REPORT.
func readStageReport(t *testing.T, path string) map[string]int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	if err := json.Unmarshal(data, &counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

// TestTestResultCache is the counted receipt of g-b2, and the law that a
// cached verdict is the verdict of a rerun. `ergo test ./...` over a module
// of passing packages and one failing package, counted by its stage report
// (ERGO_STAGE_REPORT):
//
//   - cold, each package's test binary is linked once;
//   - warm, each passing package replays its cached result, so its run
//     neither builds nor runs anything; the failing one, never cached,
//     runs again from the binary the action store holds: no compile, no
//     link, one process;
//   - --count 1 runs every package again from the module's one test binary
//     (TEST-one), linked by the first such run; the next has no compile
//     and no link, and the output and exit status of each are the cached
//     run's.
//
// A run that differs in what it reads (a --run name, a GO* variable) is no
// hit; a go wrapper of the same release links with the same patched go,
// so it is one. It kills the g-b2 cache mutants (cache-*), which no
// other test reaches: none runs a package twice.
// It builds Go because the claim is about the compiles, links and
// processes of real test builds.
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
		stdout                             string
		code, notes                        int
		links, reused, compiles, processes int64
	}
	run := func(env []string, args ...string) outcome {
		t.Helper()
		report := filepath.Join(t.TempDir(), "stage.json")
		command := exec.Command(bin, append([]string{"test"}, args...)...)
		command.Dir = root
		// The counts are the direct path's, which a GOFLAGS of the
		// caller's (the mutant runner's -p) would turn away.
		command.Env = append(append(os.Environ(), "GOFLAGS=", "ERGO_STAGE_REPORT="+report), env...)
		out, stderr, code := runSmokeCommand(t, command)
		counts := readStageReport(t, report)
		return outcome{
			stdout: string(out), code: code,
			notes: strings.Count(string(stderr), cachedNote),
			links: counts["links"], reused: counts["links_reused"], compiles: counts["go_compiles"], processes: counts["processes"],
		}
	}
	cold := run(nil, "./...")
	if cold.code != 1 || cold.links != passing+1 || cold.notes != 0 || strings.Count(cold.stdout, "PASS (2 tests)") != passing {
		t.Fatalf("cold run: %+v", cold)
	}
	warm := run(nil, "./...")
	if warm.links != 0 || warm.reused != 1 || warm.compiles != 0 || warm.processes != 1 || warm.notes != passing {
		t.Fatalf("warm run: %+v; want no link, 1 reused link and 1 process (the failing package: a result is found before the link), no compile, %d replays", warm, passing)
	}
	// --count 1 keeps no warm answer, so it tests the module with one test
	// binary of every package (TEST-one, docs/testing.md), not the
	// packages' own: the first such run links that binary once, and the
	// next reuses it.
	first := run(nil, "./...", "--count", "1")
	if first.notes != 0 || first.links != 1 {
		t.Fatalf("first --count 1 run: %+v; want no replay and one link (the module's test binary)", first)
	}
	again := run(nil, "./...", "--count", "1")
	if again.notes != 0 || again.links != 0 || again.compiles != 0 || again.processes != passing+1 {
		t.Fatalf("--count 1 run: %+v; want no replay, no link, no compile, %d processes", again, passing+1)
	}
	for _, o := range []outcome{warm, first, again} {
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
	// The go command that links is the patched release of the selected Go
	// (buildAction, IRT0b), not the PATH entry: a wrapper that runs the
	// same release links with the same go, so its run replays.
	// TestTestResultKeysTheLinkingGo keys a reinstalled linking go.
	if o := run([]string{"PATH=" + wrapper + string(os.PathListSeparator) + os.Getenv("PATH")}, "./p0"); o.notes != 1 || o.code != 0 {
		t.Fatalf("a go wrapper of the same release did not replay: %+v", o)
	}
}

// TestTestResultKeysTheLinkingGo: a test result is keyed by the go command
// that links the binary (its path, size and modification time), so a
// reinstalled Go misses. It kills cache-go-unkeyed, which the process test
// cannot reach since IRT0b: the linking go is the patched release in
// ergo's cache, which no test may replace.
func TestTestResultKeysTheLinkingGo(t *testing.T) {
	goPath := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tc := build.Toolchain{Go: goPath, Root: "/nonexistent/goroot", GOOS: "linux", GOARCH: "amd64"}
	ledger := filepath.Join(t.TempDir(), "no-ledger")
	before := testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger)
	if before == "" || before != testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger) {
		t.Fatalf("the key of one run is %q, then another", before)
	}
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\n# reinstalled\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if after := testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger); after == before {
		t.Fatal("a reinstalled go command keeps the test result's key")
	}
}
