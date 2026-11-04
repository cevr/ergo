package replay

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// TestKindOfKeysOnlyFilesDirectoriesAndLinks: the one rule of what a key
// can read takes a regular file, a directory and a symlink, whatever their
// permission bits, and refuses every other file type, each named by its
// kind. A device cannot be made without privilege, so the device types are
// checked here by their mode bits (TestKeyFailsClosed reaches a real one,
// os.DevNull).
func TestKindOfKeysOnlyFilesDirectoriesAndLinks(t *testing.T) {
	for _, perm := range []fs.FileMode{0, 0o644, 0o755, 0o4755} {
		for mode, want := range map[fs.FileMode]Kind{0: File, fs.ModeDir: Directory, fs.ModeSymlink: Symlink} {
			if kind, err := KindOf("p", mode|perm); kind != want || err != nil {
				t.Errorf("mode %v: %s, %v; want %s", mode|perm, kind, err, want)
			}
		}
	}
	for mode, name := range map[fs.FileMode]string{
		fs.ModeNamedPipe:                  "FIFO",
		fs.ModeSocket:                     "socket",
		fs.ModeDevice:                     "device",
		fs.ModeDevice | fs.ModeCharDevice: "character device",
		fs.ModeIrregular:                  "file of type ?---------",
	} {
		kind, err := KindOf("dir/p", mode|0o644)
		var unkeyable *Unkeyable
		if kind != Other || !errors.As(err, &unkeyable) {
			t.Errorf("mode %v: %s, %v; want Other and *Unkeyable", mode, kind, err)
			continue
		}
		if want := "dir/p is a " + name + ": "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("mode %v: %q; want it to start %q", mode, err, want)
		}
	}
}
