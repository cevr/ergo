package irspike

import (
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRMapKeysHaveBodies checks in process that a store to a Map entry
// whose key runs statements (testdata/mapkey.ergo: a fallible call in a
// binary key, a call that needs Console in a record key) has an IR body
// (FLIP F7 PORT-TEMP). The key's statements had no list to hold them,
// so both bodies fell back with "operation call inside an expression"
// (mutant port-temp-key-uncollected). TestIRReprosMatchSource/mapkey
// runs the program by both paths for the order of the calls.
func TestIRMapKeysHaveBodies(t *testing.T) {
	u, err := Load(filepath.Join("testdata", "mapkey.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"keyed", "indexed"} {
		if !slices.Contains(rep.IR, f) {
			t.Errorf("%s has no IR body (IR bodies: %v; fallbacks: %v)", f, rep.IR, rep.Fallback)
		}
	}
}
