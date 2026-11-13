package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The result entries (g-b2): the output of a passing run of a module's
// test executable, kept in Root/results/MODULE/KEY, where MODULE is the
// emitted module's key and the caller's KEY names everything else the run
// read (its arguments, the toolchain, the environment, the go command). A
// warm `ergo test` of an unchanged package finds its result before the
// link, so it neither links nor runs: go test's cache of a passing result.
// The trim removes a module's results when no run used them for
// unusedLimit, as it removes module entries, and a hit marks them used.
//
// No lock guards a result: it is written to a temporary file and renamed,
// so a reader sees a whole result or none, and a trim that removes the
// entry under an open reader leaves the reader its file.

// ErrNoResultCache reports a module whose results are not kept: a private
// module, whose root no trim bounds, or a platform without flock.
var ErrNoResultCache = errors.New("build: no result cache for this module")

// resultDir is the directory of module m's results.
func (c Cache) resultDir(m Module) string {
	return filepath.Join(c.Root, "results", m.Key)
}

// Result returns the stored result of key, a file name such as a hex
// digest, for module m, and whether there is one.
func (c Cache) Result(m Module, key string) ([]byte, bool) {
	if m.Private || !leasesSupported {
		return nil, false
	}
	dir := c.resultDir(m)
	data, err := os.ReadFile(filepath.Join(dir, key))
	if err != nil {
		return nil, false
	}
	if info, err := os.Stat(dir); err == nil {
		mark(dir, info, time.Now())
	}
	return data, true
}

// PutResult stores data as key's result for module m.
func (c Cache) PutResult(m Module, key string, data []byte) error {
	if m.Private || !leasesSupported {
		return ErrNoResultCache
	}
	dir := c.resultDir(m)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	path := filepath.Join(dir, key)
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
