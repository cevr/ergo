package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRFunctionNamesMatchSource is the seeded property over declared
// functions used as values (F7-cs-fnname, GenerateFnName): bare and
// qualified, pure and needing Console, fallible with and without a value,
// and written as Go methods of a record and of a pointer. Each program
// prints the same under both lowerings, and FnNameFuncs have IR bodies. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the closure's Go type and its call of the function
// (its environment, its receiver, its error) are checked only when the
// patched compile reads the IR body, and the value it passes shows only in
// the compiled program.
func TestIRFunctionNamesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		files := GenerateFnName(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRFunctionNamesMatchSource ./internal/experimental/irspike"
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
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var missing []string
		for _, f := range FnNameFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
	}
}
