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
// port-temp-key-uncollected, port-temp-elem-uncollected). A place before
// a value with statements fell back with "store to a place a value with
// statements may move": a slice's element (both, stepped), an
// op-assignment (added,
// port-temp-op-old-refused) and a field through or the target of a let
// mut pointer (Box.moved, port-temp-place-refused); and a float local's
// x *= v with "op-assignment of a value with statements" (scaledOwned,
// port-temp-float-product-refused); a *T function called on a T place
// whose operands run statements with "call on a receiver's address that
// operands with statements may move" (Outer.bumped,
// port-temp-recv-refused).
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
	for _, f := range []string{"keyed", "indexed", "slotted", "both", "Grid.gridded", "stepped", "added", "scaledOwned", "Box.moved", "Outer.bumped"} {
		if !slices.Contains(rep.IR, f) {
			t.Errorf("%s has no IR body (IR bodies: %v; fallbacks: %v)", f, rep.IR, rep.Fallback)
		}
	}
}
