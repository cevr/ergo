package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIREqualityMatchesModel is the seeded property over == and != on
// types that are not basic (FLIP F7, GenerateEqual): each program prints,
// under both lowerings, what the reference model (Go's slices.Equal and
// maps.Equal on the same values) expects, and every comparing function
// has an IR body. The model, not the other path, is the reference: both
// paths call one equality function per type (equal.go), so a fault in it
// shows only against the model (mutant f7cs-equal-slice-length-skipped). A
// failing seed prints the Ergo source, the outputs and its replay command.
//
// It builds Go because the IR body's call of the equality function is
// resolved and typed by the patched compile, and its result shows only in
// the compiled program.
func TestIREqualityMatchesModel(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text, want := GenerateEqual(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIREqualityMatchesModel ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a.stdout != want || b.stdout != want || a.exit != 0 || b.exit != 0 {
			t.Fatalf("seed %d: the model expects\n%s\nsource: %+v\nir:     %+v\n%s\n%s", seed, want, a, b, replay, text)
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f[strings.LastIndex(f, ".")+1:]] = true
		}
		for _, f := range EqualFuncs {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (fallbacks: %v)\n%s", seed, f, rep.Fallback, text)
			}
		}
	}
}
