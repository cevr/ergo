package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRGetMatchesSource is the seeded property over matches on m.Get(k)
// (F7 lane B, GenerateGetMatch): each program prints the same under both
// lowerings, and every function whose match the lowering reads with the
// comma-ok form has an IR body, as have the prelude's Append, Put, Get
// and Take, which its main calls (CENSUS-TAIL-2, ir_intrinsic.go). A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the IR's comma-ok read is typed by the patched
// compile (the decoder's "mapget"), and which arm runs shows only in the
// compiled program.
func TestIRGetMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text, shapes := GenerateGetMatch(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRGetMatchesSource ./internal/experimental/irspike"
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
		for _, f := range []string{"Append", "Put", "Get", "Take"} {
			if !slices.ContainsFunc(rep.IR, func(n string) bool { return n == f || strings.HasPrefix(n, f+"_") }) {
				t.Errorf("seed %d: the prelude's %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, replay)
			}
		}
		for i, s := range shapes {
			if f := "f" + itoa(uint64(i)); s.Fused() && !got[f] {
				t.Errorf("seed %d: %s (%+v) has no IR body (IR bodies: %v)\n%s", seed, f, s, rep.IR, replay)
			}
		}
	}
}
