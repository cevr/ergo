package ot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestFetchChecksIntegrity serves a package tarball from a local server (no
// network) under a pinned integrity: Fetch writes its library and header;
// a well-formed tarball whose library differs by one byte is refused and
// writes nothing.
// Mutant: fetch skips the integrity check.
func TestFetchChecksIntegrity(t *testing.T) {
	const target = "linux-x64"
	lib, header := []byte("\x7fELF not a real library"), []byte("/* opentui.h */\n")
	good := tarball(t, map[string][]byte{"package/libopentui.so": lib, "package/opentui.h": header, "package/README.md": []byte("x")})
	// A well-formed tarball whose library has one changed byte: only the
	// integrity check can tell it from the pinned one.
	changed := bytes.Clone(lib)
	changed[len(changed)/2] ^= 1
	bad := tarball(t, map[string][]byte{"package/libopentui.so": changed, "package/opentui.h": header, "package/README.md": []byte("x")})

	saved := Integrity[target]
	t.Cleanup(func() { Integrity[target] = saved })
	sum := sha512.Sum512(good)
	Integrity[target] = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])

	serve := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@opentui/core-linux-x64/-/core-linux-x64-"+Version+".tgz" {
			http.NotFound(w, r)
			return
		}
		w.Write(serve)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "good")
	if err := Fetch(srv.Client(), srv.URL, target, dir); err != nil {
		t.Fatalf("Fetch of the pinned tarball: %v", err)
	}
	for name, want := range map[string][]byte{"libopentui.so": lib, "opentui.h": header} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %q, %v; want %q", name, got, err, want)
		}
	}

	serve = bad
	dir = filepath.Join(t.TempDir(), "bad")
	if err := Fetch(srv.Client(), srv.URL, target, dir); err == nil {
		t.Fatal("Fetch accepted a tarball with one changed byte")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused tarball left %s (%v)", dir, err)
	}
}

func tarball(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(b)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
