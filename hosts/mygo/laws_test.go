package mygo

import (
	"bytes"
	"testing"
)

// TestDesktopLawsHold runs the desktop package's own laws with `ergo test
// ./desktop`: CountIsLines and FitIsBroken (metrics.ergo), which hold the
// line breaker against a run-by-run reference. It builds Go because the
// laws run as the package's lowered test binary, which `ergo test` builds;
// the mutant runner has no suite for a host module's Ergo laws, so this
// test is how a ledger row names them. The command runs under the memory
// bound (bounded).
//
// Mutants it kills (testdata/mutants.json), each by one of the laws and by
// no Go test of the module: GPa-count-no-break and
// ru17-fit-width-no-space (CountIsLines refutes them first),
// ru17-fit-unspaced and ru17-fit-tab-spaced (FitIsBroken).
func TestDesktopLawsHold(t *testing.T) {
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	out, err := bounded(ergo, "test", "./desktop").CombinedOutput()
	if err != nil {
		t.Fatalf("ergo test ./desktop: %v\n%s", err, out)
	}
	for _, law := range []string{"CountIsLines", "FitIsBroken"} {
		if !bytes.Contains(out, []byte("ok law "+law)) && !bytes.Contains(out, []byte("ok law desktop."+law)) {
			t.Errorf("ergo test ./desktop ran no law %s:\n%s", law, out)
		}
	}
}
