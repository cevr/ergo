package irspike

import (
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRStoreIndexesHaveBodies checks in process that a store whose
// index runs statements has an IR body (FLIP F7 PORT-TEMP,
// testdata/mapkey.ergo): a Map entry under a fallible call in a binary
// key or a call that needs Console in a record key (keyed, indexed), and
// a slice's element under a fallible index, with a value that runs
// statements too, and through a pointer (slotted, both, gridded). The
// index's statements had no list to hold them, so each body fell back
// with "operation call inside an expression" (mutants
// port-temp-key-uncollected, port-temp-elem-uncollected), and a slice
// element before a value with statements with "store to a place a value
// with statements may move" (port-temp-elem-unfixed).
// TestIRReprosMatchSource/mapkey runs the program by both paths for the
// order of the calls.
func TestIRStoreIndexesHaveBodies(t *testing.T) {
	u, err := Load(filepath.Join("testdata", "mapkey.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"keyed", "indexed", "slotted", "both", "Grid.gridded", "stepped"} {
		if !slices.Contains(rep.IR, f) {
			t.Errorf("%s has no IR body (IR bodies: %v; fallbacks: %v)", f, rep.IR, rep.Fallback)
		}
	}
}
