package sketchfront

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"sync"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
)

// The embedded std snapshot (compile-speed CS1). The compiler carries the
// checked prelude and std (check.ExportStd) in its own binary, so its first
// check of a process parses no std file and checks no std declaration,
// with every cache empty; a package's segment is decoded on its first
// import (check/export.go). The snapshot is made from the checker's source
// by TestStdSnapshotIsCurrent -update-std-snapshot, which also writes
// stdsnap_gen.go: the digest of the std sources it checked, and the digest
// of every source file of the checker's import closure. The test fails at
// a head whose std or checker no longer matches those digests, so the gate
// refuses a stale snapshot before a binary is built from it. At run time
// the compiler refuses the snapshot when its std sources differ from the
// ones the binary embeds (a defence in depth: the checker's own source is
// not in the binary), and then falls back to the disk cache (stdcache.go)
// and to source.
//
// ERGO_STD_SNAPSHOT=off selects the reference, std checked from source (or
// the disk cache), for the agreement test and for diagnosis.
//
// Prior art: OCaml and ReScript ship their std's compiled interfaces
// (.cmi) with the compiler; Go ships std's export data in its toolchain's
// build cache on first use; Gleam caches each checked module's interface
// in build/. Ergo embeds the whole checked graph, since its lowering and
// evaluator read std bodies too.

//go:embed stdsnap/std.snap
var embeddedStd []byte

// StdSnapshotEnv is the variable that turns the embedded snapshot off.
const StdSnapshotEnv = "ERGO_STD_SNAPSHOT"

var embedded struct {
	mu sync.Mutex
	on bool
}

// UseEmbeddedStd makes this process install the embedded snapshot before
// its first check, unless ERGO_STD_SNAPSHOT is "off". The compiler enables
// it; a library caller and the tests check from source unless they enable
// it too.
func UseEmbeddedStd(on bool) {
	embedded.mu.Lock()
	embedded.on = on
	embedded.mu.Unlock()
}

// embeddedEnabled reports whether this process uses the embedded snapshot.
func embeddedEnabled() bool {
	embedded.mu.Lock()
	defer embedded.mu.Unlock()
	return embedded.on && os.Getenv(StdSnapshotEnv) != "off"
}

// embeddedSnapshot is the embedded snapshot when it was made from the std
// sources deps holds, nil otherwise.
func embeddedSnapshot(deps []build.Dependency) []byte {
	if len(embeddedStd) == 0 || stdSourcesDigest(deps) != embeddedStdSources {
		return nil
	}
	return embeddedStd
}

// installEmbedded installs the embedded snapshot, and reports whether it
// did. A snapshot that does not decode is refused, never half installed.
func installEmbedded() bool {
	data := embeddedSnapshot(stdDependencies())
	return data != nil && check.InstallStd(data) == nil
}

// stdSourcesDigest is the digest of the std sources: every package's name
// and the digest of its files.
func stdSourcesDigest(deps []build.Dependency) string {
	h := sha256.New()
	fmt.Fprint(h, "ergo-std-sources-1")
	for _, d := range deps {
		fmt.Fprintf(h, "\x00%d:%s%d:%s", len(d.Name), d.Name, len(d.Implementation), d.Implementation)
	}
	return hex.EncodeToString(h.Sum(nil))
}
