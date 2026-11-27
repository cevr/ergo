package v1

import (
	"io/fs"
	"os"
)

// FSEntry holds non-following lstat metadata for fs.Entries.ReadEntry:
// Kind encodes 0=FileEntry, 1=DirEntry, 2=SymlinkEntry, 3=OtherEntry;
// AllocatedBytes is 512 * blocks (0 when blocks is non-positive).
type FSEntry struct {
	Kind           int
	Size           int
	AllocatedBytes int
	ModSec         int64
	ModNsec        int64
	AccessSec      int64
	AccessNsec     int64
	Nlink          int
	Dev            uint64
	Ino            uint64
}

// FSLstatEntry is the body of fs.Host's ReadEntry: Lstat metadata of path
// itself, never following a trailing symbolic link.
//
//ergo:host
//ergo:wait none
func FSLstatEntry(path string) (FSEntry, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FSEntry{}, fsErr(path, err)
	}
	mode := fi.Mode()
	kind := 3
	switch {
	case mode&fs.ModeSymlink != 0:
		kind = 2
	case mode.IsDir():
		kind = 1
	case mode.IsRegular():
		kind = 0
	}
	mod := fi.ModTime()
	entry := FSEntry{
		Kind:       kind,
		Size:       int(fi.Size()),
		ModSec:     mod.Unix(),
		ModNsec:    int64(mod.Nanosecond()),
		AccessSec:  mod.Unix(),
		AccessNsec: int64(mod.Nanosecond()),
		Nlink:      1,
	}
	fillFSEntryStat(fi, &entry)
	return entry, nil
}
