package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The result entries (g-b2): the output of a passing run of a module's
// executable, kept in the module's executable entry (executable.go) under
// results/KEY, where the caller's key names everything else the run read
// (its arguments, the environment, the go command). A warm `ergo test` of
// an unchanged package finds its result before the link, so it neither
// links nor runs: go test's cache of a passing result. A result lives and
// dies with its entry: the trim that removes an unused entry removes its
// results, and a hit marks the entry used.
//
// No lock guards a result: it is written to a temporary file and renamed,
// so a reader sees a whole result or none, and a trim that removes the
// entry under an open reader leaves the reader its file.

// resultPath is the file of key's result in m's executable entry for t.
func (c Cache) resultPath(m Module, t Toolchain, key string) string {
	return filepath.Join(c.Root, "bins", m.Key+"-"+executableVariant(t), "results", key)
}

// Result returns the stored result of key, a file name such as a hex
// digest, for module m linked by t, and whether there is one. It returns
// none for a module without an executable entry (LinkCached).
func (c Cache) Result(m Module, t Toolchain, key string) ([]byte, bool) {
	if m.Private || !leasesSupported {
		return nil, false
	}
	path := c.resultPath(m, t, key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	entry := filepath.Dir(filepath.Dir(path))
	if info, err := os.Stat(entry); err == nil {
		mark(entry, info, time.Now())
	}
	return data, true
}

// PutResult stores data as key's result for module m linked by t. Call it
// while m.Executable is held (LinkCached), so no trim removes the entry
// while the result is written.
func (c Cache) PutResult(m Module, t Toolchain, key string, data []byte) error {
	if m.Private || !leasesSupported {
		return ErrNoExecutableCache
	}
	path := c.resultPath(m, t, key)
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	staged := path + ".tmp-" + hex.EncodeToString(nonce[:])
	if err := os.WriteFile(staged, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		os.Remove(staged)
		return err
	}
	return nil
}
