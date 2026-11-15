package v1

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/runtime/tzdata"
)

// TestTzHostZone holds tz/host's body to its contract over a $ZONEINFO
// directory the test makes: a zone file of the directory reads as its
// bytes, and a name that leaves the directory (a .. element, an absolute
// path, a backslash) or that no directory holds reads as "", even when a
// file is there. Mutant tz-host-no-name-check (the name check dropped)
// reads the file beside the directory.
func TestTzHostZone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "zoneinfo")
	toronto := tzdata.Zone("America/Toronto")
	if err := os.MkdirAll(filepath.Join(dir, "Test"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{filepath.Join(dir, "Test", "Zone"): toronto, filepath.Join(root, "outside"): "not a zone"} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ZONEINFO", dir)
	if got := TzHostZone("Test/Zone"); got != toronto {
		t.Errorf("TzHostZone(Test/Zone) = %d bytes, want the %d of the file", len(got), len(toronto))
	}
	for _, name := range []string{"", "../outside", "Test/../../outside", filepath.Join(root, "outside"), `Test\Zone`, "No/Such"} {
		if got := TzHostZone(name); got != "" {
			t.Errorf("TzHostZone(%q) = %q, want \"\"", name, got)
		}
	}
}
