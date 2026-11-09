//go:build linux

package producer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
)

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// memoIdentity acquires the identity of the file at path, through a memo in
// dir, as a fresh process would.
func memoIdentity(t *testing.T, path, dir string) Identity {
	t.Helper()
	open := func() (io.ReadCloser, error) { return os.Open(path) }
	info := func() (*debug.BuildInfo, bool) { return nil, false }
	nonce := func() (string, error) { return "nonce", nil }
	memo := func() *imageMemo { return &imageMemo{path: filepath.Join(dir, memoFile)} }
	return acquireOnce(open, info, nonce, memo)()
}

func memoLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, memoFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The memo recalls a digest by the opened file's key, and a changed compiler
// is hashed again: rewritten in place to the same size, or replaced.
func TestImageMemoRecallsByFileKeyAndRehashesAChangedFile(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(t.TempDir(), "ergo")
	if err := os.WriteFile(image, []byte("compiler A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if id := memoIdentity(t, image, dir); id.Digest != sha("compiler A") || id.Qualifier != id.Digest || id.ReuseScope != "artifact" {
		t.Fatal("first acquisition", id)
	}
	lines := memoLines(t, dir)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " "+sha("compiler A")) {
		t.Fatal("not recorded", lines)
	}

	// A planted digest under the file's key is recalled: the image is not
	// read again.
	planted := "sha256:" + strings.Repeat("ab", 32)
	key, _, _ := strings.Cut(lines[0], " ")
	if err := os.WriteFile(filepath.Join(dir, memoFile), []byte(key+" "+planted+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if id := memoIdentity(t, image, dir); id.Digest != planted {
		t.Fatal("not recalled", id)
	}

	// The same file rewritten in place to the same size, its modification
	// time set back: its change time moved, so it is hashed again.
	stat, err := os.Stat(image)
	if err != nil {
		t.Fatal(err)
	}
	ctime := func() syscall.Timespec {
		var st syscall.Stat_t
		if err := syscall.Stat(image, &st); err != nil {
			t.Fatal(err)
		}
		return st.Ctim
	}
	before := ctime()
	// The event is the change time moving past the recorded one: the
	// kernel's clock for it is coarse, so rewrite until it has.
	for ctime() == before {
		if err := os.WriteFile(image, []byte("compiler B"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(image, stat.ModTime(), stat.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	if id := memoIdentity(t, image, dir); id.Digest != sha("compiler B") {
		t.Fatal("in-place rewrite recalled a stale digest", id)
	}

	// A file renamed over it is another inode.
	next := image + ".new"
	if err := os.WriteFile(next, []byte("compiler C"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, image); err != nil {
		t.Fatal(err)
	}
	if id := memoIdentity(t, image, dir); id.Digest != sha("compiler C") {
		t.Fatal("replaced file recalled a stale digest", id)
	}
	if lines := memoLines(t, dir); len(lines) != 3 || !strings.HasSuffix(lines[0], " "+sha("compiler C")) {
		t.Fatal("newest entry is not first", lines)
	}
}

// A malformed memo is not an error, a reader that is not an open file has
// no key, and the memo stays bounded.
func TestImageMemoToleratesGarbageAndStaysBounded(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(t.TempDir(), "ergo")
	if err := os.WriteFile(image, []byte("compiler A"), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := "no-digest\n k sha256:zz\nkey sha256:" + strings.Repeat("0", 63) + "\n\x00\x01\n"
	if err := os.WriteFile(filepath.Join(dir, memoFile), []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}
	if id := memoIdentity(t, image, dir); id.Digest != sha("compiler A") {
		t.Fatal(id)
	}
	if lines := memoLines(t, dir); len(lines) != 1 {
		t.Fatal("garbage kept", lines)
	}

	empty := t.TempDir()
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("compiler A")), nil }
	id := acquireOnce(open, func() (*debug.BuildInfo, bool) { return nil, false }, func() (string, error) { return "n", nil }, func() *imageMemo { return &imageMemo{path: filepath.Join(empty, memoFile)} })()
	if id.Digest != sha("compiler A") {
		t.Fatal(id)
	}
	if _, err := os.Stat(filepath.Join(empty, memoFile)); err == nil {
		t.Fatal("a reader with no file key was recorded")
	}

	m := &imageMemo{path: filepath.Join(dir, memoFile)}
	for i := range 3 * memoEntries {
		m.record(fmt.Sprintf("k%d", i), sha(fmt.Sprint(i)))
	}
	if lines := memoLines(t, dir); len(lines) != memoEntries || lines[0] != fmt.Sprintf("k%d %s", 3*memoEntries-1, sha(fmt.Sprint(3*memoEntries-1))) {
		t.Fatal("unbounded or out of order", len(lines), lines[0])
	}

	Remember("relative/dir")
	if rememberedMemo() != nil {
		t.Fatal("a relative memo directory was used")
	}
	Remember("")
}
