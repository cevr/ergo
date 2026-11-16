package irspike

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIRIndexesMatchSource is the seeded property over indexes of values
// that are no slice (F7, GenerateIndex): a string's byte and an array's
// element, by index and by a for-in. Each program prints the same under
// both lowerings, and the functions that index have IR bodies. A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the decoder's element type of the index (a byte,
// an array's element) is checked only by the patched compile, and a
// position read wrong shows only in the compiled program.
func TestIRIndexesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateIndex(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRIndexesMatchSource ./internal/experimental/irspike"
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
		if a != b {
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f] = true
		}
		for _, f := range []string{"code", "same", "at", "total"} {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, text)
			}
		}
	}
}
