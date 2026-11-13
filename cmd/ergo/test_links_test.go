package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
//   - --count 1 runs every package again with no compile and no link, and
//     its output and exit status are the cached run's.
//
// A run that differs in what it reads (a --run name, a GO* variable, the go
// command) is no hit. It kills the g-b2 cache mutants (cache-*), which no
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
		stdout                     string
		code, notes                int
		links, compiles, processes int64
	}
	run := func(env []string, args ...string) outcome {
		t.Helper()
		report := filepath.Join(t.TempDir(), "stage.json")
		command := exec.Command(bin, append([]string{"test"}, args...)...)
		command.Dir = root
		command.Env = append(append(os.Environ(), "ERGO_STAGE_REPORT="+report), env...)
		out, stderr, code := runSmokeCommand(t, command)
		counts := readStageReport(t, report)
		return outcome{
			stdout: string(out), code: code,
			notes: strings.Count(string(stderr), cachedNote),
			links: counts["links"], compiles: counts["go_compiles"], processes: counts["processes"],
		}
	}
	cold := run(nil, "./...")
	if cold.code != 1 || cold.links != passing+1 || cold.notes != 0 || strings.Count(cold.stdout, "PASS (2 tests)") != passing {
		t.Fatalf("cold run: %+v", cold)
	}
	warm := run(nil, "./...")
	if warm.links != 0 || warm.compiles != 0 || warm.processes != 1 || warm.notes != passing {
		t.Fatalf("warm run: %+v; want no link, no compile, 1 process (the failing package), %d replays", warm, passing)
	}
	again := run(nil, "./...", "--count", "1")
	if again.notes != 0 || again.links != 0 || again.compiles != 0 || again.processes != passing+1 {
		t.Fatalf("--count 1 run: %+v; want no replay, no link, no compile, %d processes", again, passing+1)
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
