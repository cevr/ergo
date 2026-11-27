//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package v1

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFSLstatEntryMatchesSyscall(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "payload.bin")
	payload := strings.Repeat("ergo-fs-entry-", 512)
	if err := os.WriteFile(regPath, []byte(payload), 0o640); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hardPath := filepath.Join(dir, "payload-hardlink.bin")
	if err := os.Link(regPath, hardPath); err != nil {
		t.Fatal(err)
	}
	symFile := filepath.Join(dir, "sym-file")
	if err := os.Symlink(regPath, symFile); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(dir, "sym-dir")
	if err := os.Symlink(subDir, symDir); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("no-such-target", dangling); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	// Set distinct mtime and atime on regular file to verify both timestamps are extracted faithfully.
	atime := time.Unix(1700000123, 456000000)
	mtime := time.Unix(1700000999, 123000000)
	if err := os.Chtimes(regPath, atime, mtime); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		kind int
	}{
		{regPath, 0},
		{emptyPath, 0},
		{hardPath, 0},
		{subDir, 1},
		{symFile, 2},
		{symDir, 2},
		{dangling, 2},
		{fifo, 3},
	}

	for _, tc := range cases {
		got, err := FSLstatEntry(tc.path)
		if err != nil {
			t.Fatalf("FSLstatEntry(%s): %v", tc.path, err)
		}
		if got.Kind != tc.kind {
			t.Errorf("%s: Kind = %d, want %d", tc.path, got.Kind, tc.kind)
		}
		fi, err := os.Lstat(tc.path)
		if err != nil {
			t.Fatalf("os.Lstat(%s): %v", tc.path, err)
		}
		if got.Size != int(fi.Size()) {
			t.Errorf("%s: Size = %d, want %d", tc.path, got.Size, fi.Size())
		}
		var raw FSEntry
		fillFSEntryStat(fi, &raw)
		if got.AllocatedBytes != raw.AllocatedBytes {
			t.Errorf("%s: AllocatedBytes = %d, want %d", tc.path, got.AllocatedBytes, raw.AllocatedBytes)
		}
		if got.Dev != raw.Dev || got.Ino != raw.Ino {
			t.Errorf("%s: ID = (%d,%d), want (%d,%d)", tc.path, got.Dev, got.Ino, raw.Dev, raw.Ino)
		}
		if got.Nlink != raw.Nlink {
			t.Errorf("%s: Nlink = %d, want %d", tc.path, got.Nlink, raw.Nlink)
		}
		if got.ModSec != raw.ModSec || got.ModNsec != raw.ModNsec {
			t.Errorf("%s: Mod = (%d,%d), want (%d,%d)", tc.path, got.ModSec, got.ModNsec, raw.ModSec, raw.ModNsec)
		}
		if got.AccessSec != raw.AccessSec || got.AccessNsec != raw.AccessNsec {
			t.Errorf("%s: Access = (%d,%d), want (%d,%d)", tc.path, got.AccessSec, got.AccessNsec, raw.AccessSec, raw.AccessNsec)
		}
	}

	regEntry, err := FSLstatEntry(regPath)
	if err != nil {
		t.Fatal(err)
	}
	hardEntry, err := FSLstatEntry(hardPath)
	if err != nil {
		t.Fatal(err)
	}
	emptyEntry, err := FSLstatEntry(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if regEntry.Nlink < 2 || hardEntry.Nlink < 2 {
		t.Errorf("hardlink Nlink = (%d, %d), want >= 2", regEntry.Nlink, hardEntry.Nlink)
	}
	if regEntry.Dev != hardEntry.Dev || regEntry.Ino != hardEntry.Ino {
		t.Errorf("hardlink ID mismatch: (%d,%d) vs (%d,%d)", regEntry.Dev, regEntry.Ino, hardEntry.Dev, hardEntry.Ino)
	}
	if emptyEntry.Dev == regEntry.Dev && emptyEntry.Ino == regEntry.Ino {
		t.Errorf("distinct files share ID (%d,%d)", emptyEntry.Dev, emptyEntry.Ino)
	}
	if regEntry.AllocatedBytes <= 0 || regEntry.AllocatedBytes%512 != 0 {
		t.Errorf("regular file AllocatedBytes = %d, want positive multiple of 512", regEntry.AllocatedBytes)
	}

	_, err = FSLstatEntry(filepath.Join(dir, "missing"))
	var nf NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("missing FSLstatEntry err = %v, want NotFound", err)
	}
}
