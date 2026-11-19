package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRSpawnsMatchSource is the seeded property over bodies that start
// tasks on their frame scope (GP-SPAWN, GenerateSpawn): the Task each
// owns is joined where its owner hands it off, releases it or ends, the
// same under both lowerings, a defect at a release included, and their
// functions have IR bodies. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because a release joins a task on its own goroutine at a
// scope's deferred End or a block's end; only the compiled program runs
// it.
//
// Mutants: gp-spawn-release-empty (a Task's release does nothing, so the
// scope's End does not join leave's task and its defect is lost),
// gp-spawn-block-release-dropped (a block's planned release is
// not written, so scoped's defect comes at the frame's End, after the line
// past the block).
func TestIRSpawnsMatchSource(t *testing.T) {
	f7Property(t, "TestIRSpawnsMatchSource", GenerateSpawn, spawnFuncs...)
}

// TestIRSpawnsHaveBodies checks in process, on LowerIR's report, that the
// bodies of GenerateSpawn that start a task on their frame and own it
// have IR bodies.
//
// Mutants: gp-spawn-task-gated (a call that starts a task keeps the
// source, the gate before GP-SPAWN), gp-spawn-list-off (frameListLowering
// "off": an owned local in a frame scope keeps the source),
// gp-spawn2-with-off (task.Go's block stays a closure over its captures).
func TestIRSpawnsHaveBodies(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateSpawn(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		files, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		for _, f := range spawnFuncs {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v; fallbacks: %v)\n%s", seed, f, rep.IR, rep.Fallback, text)
			}
		}
		// Each spawn function's one task.Go block reads a local of its
		// body, so each is GoWith of its captures (GP-SPAWN2).
		ir := ""
		for _, f := range files {
			if f.Path == lower.IRFileName {
				ir = string(f.Data)
			}
		}
		if n := strings.Count(ir, `"name":"GoWith"`); n != len(spawnFuncs) {
			t.Errorf("seed %d: %d task.Go blocks are GoWith of their captures; want %d, one per spawn function\n%s", seed, n, len(spawnFuncs), text)
		}
	}
}
