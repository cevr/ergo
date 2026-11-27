package v1

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// FSDirEntry is one directory child name paired with its non-following lstat
// metadata returned by FSScanDir and FSScanDirInto.
type FSDirEntry struct {
	Name  string
	Entry FSEntry
}

// FSDirScratch holds reusable caller-owned byte and entry buffers across
// FSScanDirInto calls. The zero value is ready to use.
type FSDirScratch struct {
	dirBuf  []byte
	attrBuf []byte
	entries []FSDirEntry
}

var fsDirScratchPool = sync.Pool{
	New: func() any { return &FSDirScratch{} },
}

func compareFSDirEntry(a, b FSDirEntry) int {
	return strings.Compare(a.Name, b.Name)
}

func clearFSDirEntries(entries []FSDirEntry) {
	clear(entries)
}

// FSScanDir enumerates the children of directory path with non-following
// lstat metadata, sorted by Name. It uses batched kernel directory scanning on
// Linux and macOS and falls back portably elsewhere.
//
//ergo:host
//ergo:wait none
func FSScanDir(path string) ([]FSDirEntry, error) {
	scratch := fsDirScratchPool.Get().(*FSDirScratch)
	defer func() {
		clearFSDirEntries(scratch.entries)
		scratch.entries = scratch.entries[:0]
		fsDirScratchPool.Put(scratch)
	}()

	entries, err := scanDirPlatform(path, scratch)
	if err != nil {
		return nil, err
	}
	out := make([]FSDirEntry, len(entries))
	copy(out, entries)
	return out, nil
}

// FSScanDirInto enumerates the children of directory path into scratch,
// reusing scratch's internal buffers and returned slice backing storage across
// calls. The returned slice is valid until the next call using scratch.
//
//ergo:host
//ergo:wait none
func FSScanDirInto(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	if scratch == nil {
		scratch = &FSDirScratch{}
	}
	clearFSDirEntries(scratch.entries)
	scratch.entries = scratch.entries[:0]
	entries, err := scanDirPlatform(path, scratch)
	if err != nil {
		clearFSDirEntries(scratch.entries)
		scratch.entries = scratch.entries[:0]
		return nil, err
	}
	return entries, nil
}

func readDirLstatFallback(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	dirEntries, err := FSReadDir(path)
	if err != nil {
		return nil, err
	}
	scratch.entries = scratch.entries[:0]
	for _, name := range dirEntries {
		entry, err := FSLstatEntry(filepath.Join(path, name))
		if err != nil {
			if _, ok := err.(NotFound); ok {
				continue
			}
			return nil, err
		}
		scratch.entries = append(scratch.entries, FSDirEntry{
			Name:  name,
			Entry: entry,
		})
	}
	slices.SortFunc(scratch.entries, compareFSDirEntry)
	return scratch.entries, nil
}
