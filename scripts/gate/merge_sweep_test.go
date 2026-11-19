package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
)

// The merge tier's uncached run replays a closure sweep's pass of an earlier
// merge run while the sweep's key is unchanged, and runs every other step
// uncached (GATE2 GT5). Its sweep keys name the merge tier, so its first run
// sweeps though the inner (full) tier recorded a pass at the same inputs; an
// edit outside the sweep's closure replays it; an edit inside reruns it. The
// counts are of real runs of fixture steps. Mutants: gt5-merge-sweeps-uncached
// (the run replays no sweep), gt5-merge-takes-full (merge keys name the full
// tier), gt5-end-facts-whole (the end facts come from the key of the whole
// gate, which the run does not have).
func TestMergeTierReplaysUnchangedSweeps(t *testing.T) {
	root := fixture(t)
	outside := t.TempDir()
	swept, plain := filepath.Join(outside, "swept"), filepath.Join(outside, "plain")
	steps := []*Step{
		{Name: "closure sweep ./src", Sweep: "src", Package: &replay.Closure{Dir: "src", Deps: []string{"src"}}, Argv: outsideCount(swept)},
		{Name: "plain", Argv: outsideCount(plain)},
	}
	g := wholeFixture(t, root, steps)
	g.tier = fullTier
	g.openSandbox = unsandboxed
	merge := func(step string) {
		t.Helper()
		var out bytes.Buffer
		if code := g.mergeTier(&out); code != 0 {
			t.Fatalf("%s: merge exited %d:\n%s", step, code, out.String())
		}
		if !strings.Contains(out.String(), "merge: verdicts equal (2 steps)\n") {
			t.Fatalf("%s: the verdicts differ:\n%s", step, out.String())
		}
	}
	counts := func(step string, sweeps, plains int) {
		t.Helper()
		if s, p := countIn(t, swept), countIn(t, plain); s != sweeps || p != plains {
			t.Fatalf("%s: the sweep ran %d times and the plain step %d, want %d and %d", step, s, p, sweeps, plains)
		}
	}
	merge("first merge")
	counts("first merge (the inner tier and the uncached run each sweep)", 2, 2)
	merge("second merge")
	counts("second merge (the sweep replays, the plain step runs)", 2, 3)
	write(t, root, "docs/notes.md", "edited\n")
	merge("after an edit outside the sweep's closure")
	counts("after an edit outside the sweep's closure (the inner tier runs, no sweep)", 2, 5)
	write(t, root, "src/input.txt", "edited\n")
	merge("after an edit inside the sweep's closure")
	counts("after an edit inside the sweep's closure (both sweep again)", 4, 7)
	// --merge --no-cache is the reference: its uncached run sweeps though
	// the merge-tier key holds (the inner tier replays its record).
	g.noCache = true
	merge("the reference, --no-cache")
	counts("the reference, --no-cache (the uncached run sweeps)", 5, 8)
}
