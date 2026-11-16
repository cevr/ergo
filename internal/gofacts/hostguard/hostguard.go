// Package hostguard runs the guards of the reviewed tables that stay in
// runtime/v1 (LAWFFI LF2c, LF2d; docs/spec/interop.md, Foreign facts 7):
// v1.GoSyncCallbacks (sync.go) and v1.PureImports minus
// v1.PureImportsExcept (pure.go). Entries gives the ledger line of each
// row, which go run ./scripts/stdlaws writes into std's ledger; a row acts
// only on its line current and validated (gofacts.Acts). The guards call
// expvar, flag, go/ast and log/slog and change the process's working
// directory and environment for a moment, so only scripts/stdlaws and
// tests link this package, never the compiler.
package hostguard

import (
	"os"
	"path/filepath"
	"slices"
	"sync"

	"ergo.local/prototype/internal/gofacts"
	v1 "ergo.local/prototype/runtime/v1"
)

// Seed is the seed of the pure guard's drawn arguments.
const Seed = 1

var walkOnce sync.Once
var walkDir string

// walkRoot is a directory with one file, for the walk rows' guards.
func walkRoot() string {
	walkOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ergo-hostguard-walk-")
		if err == nil && os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644) == nil {
			walkDir = dir
		}
	})
	return walkDir
}

// Sync runs the guard of the sync row of target.
func Sync(target string) error {
	call, ok := syncCalls()[target]
	if !ok {
		return errNoCall(target)
	}
	return syncGuard(target, call)
}

type errNoCall string

func (e errNoCall) Error() string {
	return string(e) + ": a row of v1.GoSyncCallbacks with no guarded call in hostguard"
}

// Entries is the ledger line of every sync and pure row at toolchain,
// validated or refuted by its guard, sorted by key.
func Entries(toolchain string) []gofacts.Entry {
	var out []gofacts.Entry
	verdict := func(err error) string {
		if err != nil {
			return "refuted"
		}
		return "validated"
	}
	for target, reason := range v1.GoSyncCallbacks {
		out = append(out, gofacts.Entry{Key: gofacts.SyncKey(target), Verdict: verdict(Sync(target)), By: "guard", With: toolchain, Digest: gofacts.SyncDigest(target, reason, toolchain)})
	}
	diffs := Pure(members)
	for _, path := range v1.PureImports {
		var err error
		for _, d := range diffs {
			if d.Pkg == path {
				err = d
			}
		}
		out = append(out, gofacts.Entry{Key: gofacts.PureKey(path), Verdict: verdict(err), By: "guard", With: toolchain, Digest: gofacts.PureDigest(path, v1.PureImportsExcept[path], toolchain)})
	}
	slices.SortFunc(out, func(a, b gofacts.Entry) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return out
}
