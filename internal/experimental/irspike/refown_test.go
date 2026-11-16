package irspike

import (
	"slices"
	"testing"
)

// TestIRRefOwnedMatchSource is the seeded property over the Ref's owned
// sites (GenerateRefOwned, IR-OWN): the IR's owned form copies the held
// Map exactly when a value outside the cell may reach it (after Get, after
// an Update that stored a Map the program holds), so each program prints
// the same under both lowerings, and tally and keep have IR bodies. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because an in-place write that reaches a Map outside the
// cell shows only when the program runs.
func TestIRRefOwnedMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateRefOwned(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRRefOwnedMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		u := loadText(t, dir, text)
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		for _, f := range []string{"tally", "keep"} {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v, fallbacks: %v)\n%s", seed, f, rep.IR, rep.Fallback, text)
			}
		}
	}
}

// TestIRRefSitesTakeTheValueOver checks the fact an owned site promises
// (V6, IR-OWN): tally's Update and Modify literals take the held Map over,
// so the IR calls the owned forms, which copy the Map only when it is
// shared, never the plain Update and Modify, which copy it on every call
// (the cost of std cache's Modify, 374.7M instructions against hand Go's
// 3.37M). Both print the same, so only the IR tells them apart. It reads
// LowerIR's IR in process. Mutant ir-own-ref-plain: the site's call is the
// plain operation.
func TestIRRefSitesTakeTheValueOver(t *testing.T) {
	text := GenerateRefOwned(1)
	got := irCalls(t, text, "tally", "pcall", "mcall")
	for _, form := range []string{"RefUpdateOwned", "RefModifyOwned"} {
		if !got[form] {
			t.Errorf("tally's IR does not call %s (its calls: %v)\n%s", form, got, text)
		}
	}
}
