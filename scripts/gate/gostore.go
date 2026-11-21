package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// testGoStoreVariable names, to the test processes of a GoStore step, a Go
// action store (the direct build path's content-keyed plans, actions and
// blobs, internal/sketch/build/direct.go) that their emission caches use
// in place of their own (cmd/ergo build_test.go, goStoreVariable). The
// store outlives the run, so a gate's program builds reuse the compiles of
// the gate before it (GS26c).
const testGoStoreVariable = "ERGO_CMD_TEST_GO_STORE"

// goStorePackages are the test packages whose steps get the store.
var goStorePackages = []string{"cmd/ergo"}

// goStoreDir is the stores' directory in the gate cache; each toolchain
// has its own store below it (goStore).
const goStoreDir = "test-go-store"

// goStoreBound is the size the stores are trimmed to after each run
// (trimGoStores).
const goStoreBound int64 = 1 << 30

// goStore is the store of the run's GoStore steps, made once per run:
//
//   - a plain run's is persistent, in the gate cache, keyed by the go
//     command's fingerprint (its version, its go env, its installation:
//     toolVersion), so another toolchain never reads this one's archives;
//   - a --no-cache run's is fresh and removed when the run ends, so the
//     reference run builds from cold;
//   - a run in an enclosure (the merge tier's sandbox, where no host path
//     is writable) and a run without a gate cache get none, and the tests
//     keep a store per process.
//
// What the store holds cannot change a verdict: an action is keyed by its
// inputs and a blob by its digest, and a build that finds an entry gone
// makes it again (internal/sketch/build trim.go, trimFile). So the step key
// holds only that the step gets a store (key), not the path; the toolchain
// it is keyed by is the go fingerprint that the whole-gate key holds.
func (g *gate) goStore() string {
	g.goStoreOnce.Do(func() {
		if g.cacheDir == "" || g.enclosure != nil {
			return
		}
		root := filepath.Join(g.cacheDir, goStoreDir)
		if err := os.MkdirAll(root, 0o755); err != nil {
			g.goStoreProblem = err
			return
		}
		if g.noCache {
			g.goStorePath, g.goStoreProblem = os.MkdirTemp(root, "fresh-")
			g.goStoreFresh = g.goStorePath
			return
		}
		digest := sha256.Sum256([]byte(g.toolVersion("go")))
		path := filepath.Join(root, "go-"+hex.EncodeToString(digest[:])[:16])
		if err := os.MkdirAll(path, 0o755); err != nil {
			g.goStoreProblem = err
			return
		}
		g.goStorePath = path
	})
	return g.goStorePath
}

// closeGoStores ends the run's use of the stores: it removes a fresh store
// and trims the persistent ones to goStoreBound, and says what is left.
func (g *gate) closeGoStores(out io.Writer) {
	if g.goStoreProblem != nil {
		fmt.Fprintf(out, "gate: test go store: %v\n", g.goStoreProblem)
	}
	if g.goStoreFresh != "" {
		_ = os.RemoveAll(g.goStoreFresh)
	}
	if g.goStorePath == "" {
		return
	}
	trim, err := trimGoStores(filepath.Join(g.cacheDir, goStoreDir), goStoreBound)
	if err != nil {
		fmt.Fprintf(out, "gate: test go store: trim: %v\n", err)
		return
	}
	fmt.Fprintf(out, "gate: test go store: %d files, %d MiB (bound %d MiB); trimmed %d files\n", trim.kept, trim.size>>20, goStoreBound>>20, trim.removed)
}

// storeTrim counts a trim: the files it kept and their size, and the files
// it removed.
type storeTrim struct {
	kept, removed int
	size          int64
}

// trimGoStores removes the least recently used files of the stores below
// root, every toolchain's together, until what is left is at most bound
// bytes. A use is a build's mark (the file's modification time, which the
// direct path sets when it reuses an entry). It takes no lock: a build
// that finds a plan, action or blob gone makes it again, or falls back to
// go build when a link loses it (internal/sketch/build trim.go, trimFile).
// Directories stay: a build's emission cache links the store's root.
func trimGoStores(root string, bound int64) (storeTrim, error) {
	type file struct {
		path string
		size int64
		used int64
	}
	var files []file
	var t storeTrim
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			files = append(files, file{path, info.Size(), info.ModTime().UnixNano()})
			t.size += info.Size()
		}
		return nil
	})
	if err != nil {
		return t, err
	}
	slices.SortFunc(files, func(a, b file) int {
		return cmp.Or(cmp.Compare(a.used, b.used), strings.Compare(a.path, b.path))
	})
	i := 0
	for ; i < len(files) && t.size > bound; i++ {
		if err := os.Remove(files[i].path); err != nil && !os.IsNotExist(err) {
			return t, err
		}
		t.size -= files[i].size
		t.removed++
	}
	t.kept = len(files) - i
	return t, nil
}
