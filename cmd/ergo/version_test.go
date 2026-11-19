package main

import (
	"encoding/json"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/toolchain"
)

// TestVersionNamesErgo (SPLIT E1): `ergo version` prints ergo's own
// version first, and --json carries it as "version", beside the Go release.
// Mutant (testdata/mutants.json): version-reads-go-release.
func TestVersionNamesErgo(t *testing.T) {
	out, code := commandInProcess(t, "version", "--json")
	var report struct{ Version, Go string }
	if err := json.Unmarshal(out, &report); code != 0 || err != nil {
		t.Fatalf("ergo version --json: exit %d, %v:\n%s", code, err, out)
	}
	if report.Version != ergoversion.Current() || report.Go != toolchain.Version || report.Version == report.Go {
		t.Fatalf("ergo version --json = %s, want version %q and go %q", out, ergoversion.Current(), toolchain.Version)
	}
	out, code = commandInProcess(t, "version")
	if first, _, _ := strings.Cut(string(out), "\n"); code != 0 || first != "ergo "+report.Version+", Go "+toolchain.Version {
		t.Fatalf("ergo version: exit %d, first line %q", code, first)
	}
}
