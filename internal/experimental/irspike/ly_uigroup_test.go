package irspike

import (
	"testing"

	"ergo.local/prototype/internal/sketch/uiskipgen"
)

// TestIRUISkipMatchesSource is the skipping property (P-ui; lower's
// TestUISkipAgrees) through the IR build (IRG-MAIN): uiskipgen's program
// of a seed prints the same under the source build, whose component calls
// go through std ui's group, and the IR build, its counted work included
// (the components each pass runs, growth, elsewhere and nested), so the
// IR skips exactly the calls the source skips. The views that make
// component calls have IR bodies, and the IR calls group and groupBy. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because what a pass skips shows only when the compiled
// program runs its passes.
//
// Mutants (testdata/mutants.json): ly-irgroup-off, ly-ns2-ir-preset-unread,
// ly-ns2-ir-reads-whole, ly-ns2-ir-keep-aliases.
func TestIRUISkipMatchesSource(t *testing.T) {
	lyProperty(t, "TestIRUISkipMatchesSource", func(seed uint64) string { return uiskipgen.Program([]uint64{seed}) },
		[]string{"group", "groupBy"}, "card", "list", "framed", "headed", "talliedView", "wideView", "cardsView", "bagged")
}
