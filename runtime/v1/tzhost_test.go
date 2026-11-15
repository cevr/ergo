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

// TestTzLocalBodies holds tz/host's Local bodies to their contract. TzEnv
// reads TZ at each call, so t.Setenv is its input: set, set to "", and
// unset (no child process is needed). tzLocalAt, the body of TzHostLocal
// over a path, reads a link into a zoneinfo directory as the zone's name,
// a copied zone file as its bytes, and a missing file as nothing.
// Mutants t3-tzenv-unset-is-set (unset read as "", which is UTC) and
// t3-local-link-whole-path (the link's whole target as the name).
func TestTzLocalBodies(t *testing.T) {
	t.Setenv("TZ", ":America/Toronto")
	if set, v := TzEnv(); !set || v != ":America/Toronto" {
		t.Errorf("TZ=:America/Toronto: TzEnv() = %v, %q", set, v)
	}
	t.Setenv("TZ", "")
	if set, v := TzEnv(); !set || v != "" {
		t.Errorf("TZ=\"\": TzEnv() = %v, %q", set, v)
	}
	if err := os.Unsetenv("TZ"); err != nil {
		t.Fatal(err)
	}
	if set, v := TzEnv(); set || v != "" {
		t.Errorf("TZ unset: TzEnv() = %v, %q", set, v)
	}
	root := t.TempDir()
	zone := filepath.Join(root, "share", "zoneinfo", "America", "Toronto")
	toronto := tzdata.Zone("America/Toronto")
	if err := os.MkdirAll(filepath.Dir(zone), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zone, []byte(toronto), 0o644); err != nil {
		t.Fatal(err)
	}
	link, copied := filepath.Join(root, "link"), filepath.Join(root, "copied")
	if err := os.Symlink(zone, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, []byte(toronto), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, name, data string }{
		{link, "America/Toronto", ""},
		{copied, "", toronto},
		{filepath.Join(root, "missing"), "", ""},
	} {
		if name, data := tzLocalAt(c.path); name != c.name || data != c.data {
			t.Errorf("tzLocalAt(%s) = %q and %d bytes, want %q and %d bytes", filepath.Base(c.path), name, len(data), c.name, len(c.data))
		}
	}
}
