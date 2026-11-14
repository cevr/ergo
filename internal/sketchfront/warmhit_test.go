package sketchfront

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStatSumsReadOnlyWhatChanged holds ergo dev's input hashes to their
// rule (StatSums): a file whose stat is the one it was hashed with, and
// whose last change was settle before that hash, is not read again; a
// changed stat, or a change within settle of the hash, reads the file. The
// stat and the clock are the test's, so a write the stat does not show
// tells a kept hash from a read one.
func TestStatSumsReadOnlyWhatChanged(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a.ergo")
	stat := FileStat{Size: 2, Mtime: 10e9, Ctime: 10e9, Dev: 1, Ino: 7}
	s := &StatSums{
		Stat: func(string) (FileStat, bool) { return stat, true },
		Now:  func() time.Time { return time.Unix(100, 0) },
	}
	write := func(text string) {
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := func(step, text string) {
		t.Helper()
		sum, size, ok := s.Sum(name)
		h := sha256.Sum256([]byte(text))
		if !ok || size != int64(len(text)) || sum != hex.EncodeToString(h[:]) {
			t.Fatalf("%s: got %s %d %v, want the hash of %q", step, sum, size, ok, text)
		}
	}
	write("a1")
	want("first read", "a1")
	write("z9")
	want("same stat: the kept hash", "a1")
	stat.Ctime = 20e9
	want("changed stat: read again", "z9")
	stat.Ctime = 99.5e9
	write("c3")
	want("changed stat, within settle of the read", "c3")
	write("d4")
	want("same stat within settle: read again", "d4")
}
