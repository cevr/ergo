package v1

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestFSLinkIsOneFile holds the bodies of fs.Host's Link and SameFile
// (G6): a link is the file itself, so a write through one name is read
// through the other and SameFile holds (mutant g6-link-copies); a copy of
// the same bytes is another file (g6-samefile-by-name: a second name of
// one file is the same file, a byte-equal copy is not); a file at to is
// Failed and stays; and a link onto another file system is CrossDevice
// (g6-crossdevice-failed), as a Rename there is (u3-rename-exdev-unmapped),
// when the host has one beside the temporary directory.
func TestFSLinkIsOneFile(t *testing.T) {
	dir := t.TempDir()
	at := func(n string) string { return filepath.Join(dir, n) }
	if err := os.WriteFile(at("entry"), []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := FSLink(at("entry"), at("out")); err != nil {
		t.Fatalf("Link(entry, out): %v", err)
	}
	if err := os.WriteFile(at("entry"), []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(at("out")); err != nil || string(got) != "v2" {
		t.Errorf("a write through entry reads %q, %v through out; want one file", got, err)
	}
	if same, err := FSSameFile(at("entry"), at("out")); err != nil || !same {
		t.Errorf("SameFile(entry, out) after Link is %v, %v", same, err)
	}
	if err := os.WriteFile(at("copy"), []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if same, err := FSSameFile(at("entry"), at("copy")); err != nil || same {
		t.Errorf("SameFile(entry, copy) of a byte-equal copy is %v, %v; want another file", same, err)
	}
	var f Failed
	if err := FSLink(at("copy"), at("out")); !errors.As(err, &f) || f.Path != at("out") {
		t.Errorf("Link over a file that is there is %v, want Failed at out", err)
	}
	if got, _ := os.ReadFile(at("out")); string(got) != "v2" {
		t.Errorf("a refused Link changed out to %q", got)
	}
	var nf NotFound
	if err := FSLink(at("missing"), at("new")); !errors.As(err, &nf) || nf.Path != at("missing") {
		t.Errorf("Link of a missing file is %v, want NotFound naming it", err)
	}
	if _, err := FSSameFile(at("entry"), at("missing")); !errors.As(err, &nf) || nf.Path != at("missing") {
		t.Errorf("SameFile with a missing path is %v, want NotFound naming it", err)
	}
	// Another file system: /dev/shm when it is not the temporary
	// directory's device.
	var a, b syscall.Stat_t
	if syscall.Stat(dir, &a) != nil || syscall.Stat("/dev/shm", &b) != nil || a.Dev == b.Dev {
		t.Logf("no second file system beside %s; the CrossDevice case is not reached here", dir)
		return
	}
	other, err := os.MkdirTemp("/dev/shm", "g6-link-")
	if err != nil {
		t.Logf("no folder on /dev/shm: %v", err)
		return
	}
	defer os.RemoveAll(other)
	var x CrossDevice
	if err := FSLink(at("entry"), filepath.Join(other, "out")); !errors.As(err, &x) || x.From != at("entry") {
		t.Errorf("Link onto another file system is %v, want CrossDevice", err)
	}
	// FS-RENAME-CROSSDEVICE: a Rename there is CrossDevice too, and moves
	// nothing (u3-rename-exdev-unmapped). Go's os.Rename never copies, so
	// the case needs a second file system, which no Ergo law under the
	// test host's folder reaches.
	if err := FSRename(at("entry"), filepath.Join(other, "moved")); !errors.As(err, &x) || x.From != at("entry") {
		t.Errorf("Rename onto another file system is %v, want CrossDevice", err)
	}
	if _, err := os.Stat(at("entry")); err != nil {
		t.Errorf("a CrossDevice Rename moved entry: %v", err)
	}
}
