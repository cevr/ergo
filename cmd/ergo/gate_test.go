package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

// gateWork are the stage counters of work a replayed verdict must not
// start: a parse, a check, a lowering, a compile, a link, a go command, a
// process or a written file.
var gateWork = []string{
	"files_parsed", "packages_checked", "modules_lowered", "go_compiles",
	"links", "go_commands", "processes", "bytes_written",
}

// TestGateReplaysWhatHolds runs the gate over a module of a contract
// package and the hub ergo gen api writes for it (its laws run at check
// time, so nothing is built; a transport with a native law would build). A second run on the
// unchanged module starts no work: every work counter stays, and every
// verdict is replayed. A new file the packages do not read misses the
// module's verdict, and fmt, lint and gen replay from their stores. A
// changed contract fails gen --check, and a file out of format fails fmt
// --check, though both passed before. It runs in process: the stores keep
// their verdicts in memory under the test's process identity, and no
// package has a test or a law, so nothing is built.
//
// It kills gate-module-not-replayed, gate-module-key-omits-tree,
// gate-fmt-not-stored, gate-lint-not-stored, gate-gen-not-stored,
// gate-gen-key-omits-contract, gate-fmt-key-omits-bytes and
// ledger-library-resolves-toolchain.
func TestGateReplaysWhatHolds(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv("ERGO_LAWCACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	mod := t.TempDir()
	smokeWrite(t, mod, "ergo.mod", "module mod\n")
	smokeWrite(t, mod, "api/api.ergo", "// Package api is the gate test's contract.\n\n// Item is what Read answers.\ntype Item { Count int }\n\n// API reads items.\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	if err := genCommand([]string{"api", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if err := formatCommand([]string{"./..."}); err != nil {
		t.Fatal(err)
	}

	before := stage.Snapshot()
	first := runGate(t, mod)
	after := stage.Snapshot()
	if len(first.Failed) > 0 {
		t.Fatalf("first run failed: %v", first.Failed)
	}
	// No package has a test, a law or a main, so the first run builds
	// nothing either: not even the patched compile a build would take
	// (GATE-CORE-COST: the law ledger's write resolved the toolchain for a
	// library, a go build of cmd/compile into a cold cache).
	for _, name := range []string{"go_commands", "processes"} {
		if after[name] != before[name] {
			t.Errorf("first run: %s went from %d to %d, want no build", name, before[name], after[name])
		}
	}
	for _, verb := range []string{"check", "fmt", "lint", "test", "gen"} {
		if first.Computed[verb] == 0 {
			t.Errorf("first run computed no %s verdict: %v", verb, first.Computed)
		}
	}

	before = stage.Snapshot()
	second := runGate(t, mod)
	after = stage.Snapshot()
	if len(second.Failed) > 0 {
		t.Fatalf("second run failed: %v", second.Failed)
	}
	for _, name := range gateWork {
		if after[name] != before[name] {
			t.Errorf("second run on an unchanged module: %s went from %d to %d", name, before[name], after[name])
		}
	}
	if total(second.Computed) != 0 {
		t.Errorf("second run computed %v, want nothing", second.Computed)
	}
	if total(second.Replayed) != total(first.Computed) {
		t.Errorf("second run replayed %v, want each of the first run's %v", second.Replayed, first.Computed)
	}

	// A file no package reads: the module's verdict misses, and the
	// verdicts of the unchanged packages come from their stores.
	smokeWrite(t, mod, "NOTES.md", "notes\n")
	third := runGate(t, mod)
	if len(third.Failed) > 0 {
		t.Fatalf("third run failed: %v", third.Failed)
	}
	if third.Computed["check"] == 0 {
		t.Errorf("a new file did not miss the module's verdict: computed %v", third.Computed)
	}
	for _, verb := range []string{"fmt", "lint", "gen"} {
		if third.Computed[verb] != 0 || third.Replayed[verb] != first.Computed[verb] {
			t.Errorf("%s with unchanged packages: computed %d, replayed %d, want 0 and %d", verb, third.Computed[verb], third.Replayed[verb], first.Computed[verb])
		}
	}

	// A marker edit must miss the cached generation verdict even when
	// every Ergo source is byte-for-byte unchanged.
	marker := filepath.Join(mod, "api", "apiapi", "ergo.gen")
	markerBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(markerBytes), "generator ergo gen api", "generator stale", 1)
	if err := os.WriteFile(marker, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if failed := runGate(t, mod).Failed; !hasFailure(failed, "gen", "ergo.gen") {
		t.Errorf("a changed marker with unchanged sources: failures %v, want gen --check of ergo.gen", failed)
	}
	if err := os.WriteFile(marker, markerBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// A changed contract: the hub is stale.
	smokeWrite(t, mod, "api/api.ergo", "// Package api is the gate test's contract.\n\n// Item is what Read answers.\ntype Item { Count int, Note string }\n\n// API reads items.\ntype API { Read(key string) Item }\n")
	if err := formatCommand([]string{"./..."}); err != nil {
		t.Fatal(err)
	}
	if failed := runGate(t, mod).Failed; !hasFailure(failed, "gen", "apiapi") {
		t.Errorf("a changed contract: failures %v, want gen --check of apiapi", failed)
	}
	if err := genCommand([]string{"api", "api.API"}); err != nil {
		t.Fatal(err)
	}

	// A file out of format.
	path := filepath.Join(mod, "api", "api.ergo")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "Item {", "Item  {", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if failed := runGate(t, mod).Failed; !hasFailure(failed, "fmt", "api.ergo") {
		t.Errorf("a file out of format: failures %v, want fmt --check of api.ergo", failed)
	}
}

// runGate runs the gate over the module at dir.
func runGate(t *testing.T, dir string) gateRun {
	t.Helper()
	before := stage.Snapshot()
	run, err := gateModule(dir)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	after := stage.Snapshot()
	var work []string
	for _, name := range gateWork {
		work = append(work, fmt.Sprintf("%s %d", name, after[name]-before[name]))
	}
	t.Logf("gate run: computed %v, replayed %v; work: %s", run.Computed, run.Replayed, strings.Join(work, ", "))
	return run
}

func total(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// hasFailure reports whether a failure of verb names a path containing
// what.
func hasFailure(failed []string, verb, what string) bool {
	for _, f := range failed {
		if strings.HasPrefix(f, verb+" ") && strings.Contains(f, what) {
			return true
		}
	}
	return false
}
