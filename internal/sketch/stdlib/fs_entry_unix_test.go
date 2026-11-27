//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package stdlib_test

import (
	iofs "io/fs"
	"os"
	"syscall"
	"testing"
	"time"

	v1 "ergo.local/prototype/runtime/v1"
)

func mkfifoForFSEntryTest(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func unixTime(sec, nsec int64) time.Time {
	return time.Unix(sec, nsec)
}

func oracleEntryFields(t *testing.T, path string, fi os.FileInfo) (kind string, size, alloc int, modSec, modNsec, accSec, accNsec int64, nlink int) {
	t.Helper()
	mode := fi.Mode()
	switch {
	case mode&iofs.ModeSymlink != 0:
		kind = "symlink"
	case mode.IsDir():
		kind = "dir"
	case mode.IsRegular():
		kind = "file"
	default:
		kind = "other"
	}
	size = int(fi.Size())
	mod := fi.ModTime()
	modSec, modNsec = mod.Unix(), int64(mod.Nanosecond())
	nlink = 1
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		nlink = int(st.Nlink)
		if st.Blocks > 0 {
			alloc = int(int64(st.Blocks) * 512)
		}
	}
	raw, err := v1.FSLstatEntry(path)
	if err != nil {
		t.Fatalf("FSLstatEntry(%s): %v", path, err)
	}
	return kind, size, alloc, modSec, modNsec, raw.AccessSec, raw.AccessNsec, nlink
}
