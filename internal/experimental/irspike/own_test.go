package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIROwnedMatchSource is the FLIP F7 FORK agreement property for owned
// values in a static frame (GenerateOwn): for each seed the IR path and
// the source path print the same and exit the same, a defect in a Close
// included. Every owN, give and keep gets an IR body, so the lets that own
// a value, the hand-offs to a taking call, a returned value kept until the
// exit, the early returns and the deferred release (last acquired first,
// after a defect too) all run from the IR. A failing seed prints the Ergo
// source, both outcomes and its replay command. Mutant f7f-release-order
// (the release helper releases first acquired first) fails it.
//
// It builds Go for the reason needsBuild gives: the question is what the
// built program releases, in which order, on which exit, which only a run
// shows.
func TestIROwnedMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	inBody := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateOwn(seed)
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
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIROwnedMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var want []string
		for _, f := range ownFuncs {
			// A function no owN calls is not lowered: its declaration is
			// the one use of its name.
			if strings.Count(text, f[strings.LastIndex(f, ".")+1:]+"(") > 1 {
				want = append(want, f)
			}
		}
		for i := 0; strings.Contains(text, fmt.Sprintf("func ow%d(", i)); i++ {
			want = append(want, fmt.Sprintf("ow%d", i))
		}
		var missing []string
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
		// A release inside a body calls its slot's helper (ir_own.go
		// slotRelease), named <f>Free.
		if data, err := os.ReadFile(filepath.Join(dir, "ir", "ergoir.json")); err == nil && strings.Contains(string(data), `Free"`) {
			inBody++
		}
	}
	if count >= 6 && inBody == 0 {
		t.Fatalf("no seed of %d..%d releases a value inside a body", first, first+count-1)
	}
}
