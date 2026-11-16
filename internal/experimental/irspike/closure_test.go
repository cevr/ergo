package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRClosuresMatchSource is IRG6's agreement property for function
// values: for generated programs of closures (GenerateClosure), the IR
// path and the source path print the same, exit the same and fail with
// the same message. Every function but main gets an IR body in every
// seed, and the run reaches a function literal ("func") and a call of a
// function value ("vcall"), bound with its error ("def2"). A failing seed
// prints the Ergo source, both outcomes and its replay command. It builds
// Go for the reason needsBuild gives.
func TestIRClosuresMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	seen := map[string]bool{}
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateClosure(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRClosuresMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var missing []string
		for _, f := range closureFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
		irKinds(t, filepath.Join(dir, "ir", lower.IRFileName), seen)
	}
	if count < 6 {
		return // a replay: the coverage needs the whole run
	}
	var missing []string
	for _, k := range []string{"func", "vcall", "def2"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the run did not reach: kinds %v", missing)
	}
}
