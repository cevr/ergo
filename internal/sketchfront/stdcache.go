package sketchfront

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
)

// The checked std cache (CHK1b): a process installs the snapshot of the
// checked prelude and std (check.ExportStd, check.InstallStd) before its
// first check, so it checks no std package from source; it reads and
// decodes a std package only on the package's first import
// (check/export.go; CHK1c reads the file by offset). The snapshot is a
// file under the cache root, named by the key of the compiler's identity,
// the digest of the compiler's bytes, which embed the std sources
// (internal/sketch/std): a std edit is a new compiler and a new key, and a
// process never reads its 2.3 MB of std sources to name the snapshot
// (CHK1c: hashing them cost a check 3 MB of peak memory and 4 M
// instructions, measured). A miss checks every std package from source once,
// writes the snapshot and goes on with that state, which is the snapshot's.
// A file that does not decode is a miss. The compiler enables the cache
// (UseStdCache); a library caller and the tests check from source.
//
// Prior art: Gleam keeps each checked module's interface in build/ as a
// cache file keyed by the source hash; OCaml and ReScript ship the
// compiled .cmi of their std; Go's compiler reads std's export data from
// the build cache, keyed by the toolchain.

var stdCache struct {
	mu       sync.Mutex
	root     string
	compiler string
	once     sync.Once
}

// UseStdCache makes this process read and write the checked std snapshot
// under root (the compiler's cache root, build.DefaultRoot). It takes
// effect at the first check of the process.
func UseStdCache(root string) {
	stdCache.mu.Lock()
	stdCache.root = root
	stdCache.mu.Unlock()
}

// prepareStd installs the snapshot once per process, before the first
// check, when UseStdCache set a root. Every failure falls back to checking
// std from source: the cache only ever saves work.
func prepareStd() {
	stdCache.mu.Lock()
	root := stdCache.root
	stdCache.mu.Unlock()
	if root == "" {
		return
	}
	stdCache.once.Do(func() {
		compiler, err := compilerIdentity()
		if err != nil {
			return
		}
		stdCache.mu.Lock()
		stdCache.compiler = compiler
		stdCache.mu.Unlock()
		if !reusable(compiler) {
			return
		}
		path := filepath.Join(root, "std", stdSnapshotKey(compiler)+".snap")
		if installSnapshot(path) {
			return
		}
		data, err := check.ExportStd()
		if err != nil {
			return
		}
		writeSnapshot(path, data)
	})
}

// installSnapshot installs the snapshot at path and reports whether it
// did. The open file stays with the installed snapshot, which reads a std
// package's segment from it on the package's first import (CHK1c): the
// process holds only what its imports reach, never the whole file. A
// rename that replaces the cache file later leaves this file unchanged.
func installSnapshot(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	info, err := f.Stat()
	if err == nil && check.InstallStd(f, info.Size()) == nil {
		return true
	}
	_ = f.Close()
	return false
}

// preparedCompilerIdentity shares the producer already acquired for the std
// key with the emission key. With the cache off, Build keeps its lazy acquire.
func preparedCompilerIdentity() string {
	stdCache.mu.Lock()
	defer stdCache.mu.Unlock()
	if stdCache.root == "" {
		return ""
	}
	return stdCache.compiler
}

// reusable reports a compiler identity that names the compiler's bytes; a
// process-scoped identity names one process, whose snapshot no later
// process may read.
func reusable(compiler string) bool {
	return len(compiler) > 7 && compiler[:7] == "sha256:"
}

// stdSnapshotKey is the file name of the snapshot: the digest of the
// snapshot format and the compiler's identity. The identity is reusable
// (reusable) only when it is the digest of the compiler's bytes, and those
// bytes embed every std package's sources, so the identity keys std too.
func stdSnapshotKey(compiler string) string {
	h := sha256.New()
	fmt.Fprintf(h, "ergo-std-snapshot-2\x00%d:%s", len(compiler), compiler)
	return hex.EncodeToString(h.Sum(nil))
}

// writeSnapshot writes data to path through a temporary file and a rename,
// so a reader sees the whole snapshot or none. A failure leaves no file.
func writeSnapshot(path string, data []byte) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".snap-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}
