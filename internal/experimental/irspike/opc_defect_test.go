package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRDefectsMatchSource is the seeded property over the prelude's
// Unreachable (FLIP OPC-unreach, GenerateDefect): each program prints the
// same, exits the same and reports the same defect (its reason and its
// site) under both lowerings, and every function that calls Unreachable
// has an IR body. Mutants: opc-unreach-site-of-argument (the defect names
// the reason's position, not the call's), opc-unreach-catch-arm (a catch
// arm that ends in Unreachable keeps the source body). A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the defect's text and the exit come from the
// compiled program's panic; no in-process check runs it.
func TestIRDefectsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 8)
	sites := map[int]bool{}
	for seed := first; seed < first+count; seed++ {
		text, site := GenerateDefect(uint64(seed))
		sites[site] = true
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRDefectsMatchSource ./internal/experimental/irspike"
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
		const reason = "runtime leaf's generic function in a generic body"
		if n := rep.Fallback[reason]; n != 0 {
			t.Errorf("seed %d: %d bodies fallback with %q\n%s\n%s", seed, n, reason, replay, text)
		}
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if site == 6 && (a.exit == 0 || !strings.Contains(a.stderr, "lost at the leaf")) {
			t.Errorf("seed %d did not report the leaf defect: %+v\n%s", seed, a, replay)
		}
		for _, f := range []string{"classify", "half", "guard", "capped", "caught", "keep"} {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
	if count >= 8 && len(sites) < 3 {
		t.Fatalf("the seeds reached %d defect sites: the property does not reach the forms", len(sites))
	}
}
