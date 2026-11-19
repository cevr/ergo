package irspike

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIRSelectsMatchSource is the seeded property over qualified names
// (F7, GenerateSelect): an imported package's lets and its records with
// no fields, read as lib.Name. Each program prints the same under both
// lowerings, and the functions that read them have IR bodies. A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because a global's Go name is resolved only when the
// patched compile checks the IR body against the package's declarations,
// and the value a name reads shows only in the compiled program.
func TestIRSelectsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		files := GenerateSelect(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRSelectsMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		mod := filepath.Join(dir, "p")
		for name, text := range files {
			path := filepath.Join(mod, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		text := files["lib/lib.ergo"] + "\n" + files["cmd/p/main.ergo"]
		u, err := Load(filepath.Join(mod, "cmd", "p"))
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
		for _, f := range []string{"dir", "sum", "span", "label"} {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, text)
			}
		}
	}
}
