package irspike

import (
	"slices"
	"testing"
)

// TestIRRefOwnedMatchSource is the seeded property over the Ref's owned
// sites (GenerateRefOwned, IR-OWN): the IR's owned form copies the held
// Map exactly when a value outside the cell may reach it (after Get, after
// an Update that stored a Map the program holds), so each program prints
// the same under both lowerings, and each function has an IR body: stream's
// in-place literals (IR-OWN-MODIFY) write the cell's value through a
// pointer, so a write the IR misses, or one that reaches snap, changes
// the output. A
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
		for _, f := range []string{"tally", "keep", "fill", "generic", "stream"} {
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
// plain operation. stream's literals hold the value in one let mut, so
// its sites take the in-place forms, which copy no scan in or out (the
// emitter's inPlaceLit; json's Stream, IR-OWN-MODIFY); a ledger row for
// the IR taking the owned form there is owed (I-FLIP red 41, OWN1).
func TestIRRefSitesTakeTheValueOver(t *testing.T) {
	text := GenerateRefOwned(1)
	for _, want := range []struct{ fn, form string }{{"tally", "RefUpdateOwned"}, {"tally", "RefModifyOwned"}, {"fill", "RefUpdateOwned"}, {"stream", "RefModifyInPlace"}, {"stream", "RefUpdateInPlace"}} {
		if got := irCalls(t, text, want.fn, "pcall", "mcall"); !got[want.form] {
			t.Errorf("%s's IR does not call %s (its calls: %v)\n%s", want.fn, want.form, got, text)
		}
	}
}
