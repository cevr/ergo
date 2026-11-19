package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/toolchain"
)

// TestVersionNamesErgo (SPLIT E1): `ergo version` prints ergo's own
// version first, and --json carries it as "version", beside the Go release.
// --json also lists the features a driving tool requires (MODELCHECK T3d
// O1). Mutants (testdata/mutants.json): version-reads-go-release,
// version-features-dropped.
func TestVersionNamesErgo(t *testing.T) {
	out, code := commandInProcess(t, "version", "--json")
	var report struct {
		Version, Go string
		Features    []string
	}
	if err := json.Unmarshal(out, &report); code != 0 || err != nil {
		t.Fatalf("ergo version --json: exit %d, %v:\n%s", code, err, out)
	}
	// The features orrery requires (MODELCHECK T3d O1).
	for _, f := range []string{"check-strict", "machine-laws", "conform", "conform-exit-codes"} {
		if !slices.Contains(report.Features, f) {
			t.Errorf("ergo version --json lists features %v, without %s", report.Features, f)
		}
	}
	if report.Version != ergoversion.Current() || report.Go != toolchain.Version || report.Version == report.Go {
		t.Fatalf("ergo version --json = %s, want version %q and go %q", out, ergoversion.Current(), toolchain.Version)
	}
	// check's envelope lists the same features, so orrery reads them from
	// check's answer and runs no version probe (FEATURES-JSON).
	path := filepath.Join(t.TempDir(), "one.ergo")
	if err := os.WriteFile(path, []byte("func one() int { 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERGO_DEV", "off")
	checked, code := commandInProcess(t, "check", path)
	var envelope struct{ Features []string }
	if err := json.Unmarshal(checked, &envelope); code != 0 || err != nil || !slices.Equal(envelope.Features, report.Features) {
		t.Fatalf("ergo check: exit %d, %v, features %v, want %v:\n%s", code, err, envelope.Features, report.Features, checked)
	}
	out, code = commandInProcess(t, "version")
	if first, _, _ := strings.Cut(string(out), "\n"); code != 0 || first != "ergo "+report.Version+", Go "+toolchain.Version {
		t.Fatalf("ergo version: exit %d, first line %q", code, first)
	}
}
