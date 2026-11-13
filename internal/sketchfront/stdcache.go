package sketchfront

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// NP5 (std is the toolchain's module): a required module is read-only and
// named by its content hash, as std is, so its packages get the same
// snapshot. A program whose packages import packages of required modules
// (loader.moduleSegments) installs the snapshot of std and those packages,
// named by the compiler's identity and the packages' segment keys (each
// names its module's go.sum hash and its imports' keys), when the cache
// holds it: the check then decodes them and checks none from source
// (Work.ModulePackagesDecoded). A miss installs std's snapshot as above;
// the loader checks the module packages first and writes the snapshot of
// std and them before it checks a package of the program's own module, so
// the snapshot holds nothing of the program. One snapshot is installed per
// process, before its first check, so the decoded objects' ids never meet
// ids this process gave out.
//
// Prior art: Gleam keeps each checked module's interface in build/ as a
// cache file keyed by the source hash, for its dependencies as for the
// project; OCaml and ReScript ship the compiled .cmi of their std; Go's
// compiler reads std's and every dependency's export data from the build
// cache, keyed by the toolchain and the package's inputs.

var stdCache struct {
	mu       sync.Mutex
	root     string
	compiler string
	// modules is the path of the module snapshot the first check of the
	// process looked for: the only one this process may write.
	modules string
	// taken counts the module packages the loader took from the snapshot
	// instead of checking them from source (a warm check's hits).
	taken int64
	once  sync.Once
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
// check, when UseStdCache set a root: the snapshot of std and the module
// packages of segment keys modules when the cache holds it, else std's.
// Every failure falls back to checking from source: the cache only ever
// saves work.
func prepareStd(modules []string) {
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
		if len(modules) > 0 {
			path := filepath.Join(root, "std", moduleSnapshotKey(compiler, modules)+".snap")
			stdCache.mu.Lock()
			stdCache.modules = path
			stdCache.mu.Unlock()
			if installSnapshot(path) {
				return
			}
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

// moduleSnapshotKey is the file name of the snapshot of std and the module
// packages of segment keys modules, in any order.
func moduleSnapshotKey(compiler string, modules []string) string {
	sorted := slices.Sorted(slices.Values(modules))
	h := sha256.New()
	fmt.Fprintf(h, "ergo-module-snapshot-1\x00%d:%s", len(compiler), compiler)
	for _, m := range sorted {
		fmt.Fprintf(h, "\x00%d:%s", len(m), m)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeModuleSnapshot writes the snapshot of std and the checked module
// packages pkgs (segment keys keys, in dependency order) as the snapshot of
// the program whose segment keys are modules, when the first check of this
// process looked for that snapshot and missed it.
func writeModuleSnapshot(modules []string, pkgs []*check.Package, keys []string) {
	stdCache.mu.Lock()
	root, compiler, want := stdCache.root, stdCache.compiler, stdCache.modules
	stdCache.mu.Unlock()
	if root == "" || !reusable(compiler) {
		return
	}
	path := filepath.Join(root, "std", moduleSnapshotKey(compiler, modules)+".snap")
	if path != want {
		return
	}
	data, err := check.ExportSnapshot(pkgs, keys)
	if err != nil {
		return
	}
	writeSnapshot(path, data)
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
