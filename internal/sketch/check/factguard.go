package check

import (
	"slices"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/toolchain"
	v1 "ergo.local/prototype/runtime/v1"
)

// The guard lines of the reviewed tables in runtime/v1 (LAWFFI LF2d;
// interop.md, Foreign facts 7) and of the memory-only rows
// (gofacts.Effects; np4f-plan GBX6). A row of v1.GoSyncCallbacks(), a
// package of v1.PureImports or a memory-only row acts only on its line in
// the std ledger, current (the
// row's digest at the pinned toolchain) and validated by its guard
// (gofacts.Acts). The checker reads the ledger as an input, as ergo
// check's licence advice does (LF6c): the shipped one here, since the rows
// and their guards ship with the compiler.

// FactLedger is the text of the ledger the guard lines are read from:
// std's shipped ledger. go run ./scripts/stdlaws sets it to the shipped
// ledger with a fresh run's guard lines before it checks std, so a stale
// shipped ledger never stops the run that refreshes it.
var FactLedger = stdlib.Ledger

// SyncActs reports whether the sync row of target acts: v1.GoSyncCallbacks()
// has it with a reason, and its guard line is current and validated; else
// why not, and whether the line refutes it.
func SyncActs(target string) (ok, refuted bool, why string) {
	reason := v1.GoSyncCallbacks()[target]
	if reason == "" {
		return false, false, "v1.GoSyncCallbacks() has no reviewed row for it"
	}
	return gofacts.Acts(FactLedger(), gofacts.SyncKey(target), gofacts.SyncDigest(target, reason, toolchain.Version), toolchain.Version)
}

// EffectActs reports whether target's memory-only row acts (np4f-plan
// GBX6): gofacts.Effects has it, and its guard line is current and
// validated; else why not, and whether the line refutes it.
func EffectActs(target string) (ok, refuted bool, why string) {
	e, found := gofacts.EffectOf(target)
	if !found {
		return false, false, "the reviewed table gofacts.Effects has no memory-only row for it"
	}
	return gofacts.Acts(FactLedger(), gofacts.EffectKey(target), e.Digest(toolchain.Version), toolchain.Version)
}

// HostActs reports whether target's host row acts (HOST-ROW):
// gofacts.Hosts has it, and its guard line is current and validated.
func HostActs(target string) (ok, refuted bool, why string) {
	e, found := gofacts.HostOf(target)
	if !found {
		return false, false, "the reviewed table gofacts.Hosts has no host row for it"
	}
	return gofacts.Acts(FactLedger(), gofacts.HostKey(target), e.HostDigest(toolchain.Version), toolchain.Version)
}

// PureActs reports whether the Go package path's pure row acts: it is in
// v1.PureImports and its guard line is current and validated.
func PureActs(path string) bool {
	if !slices.Contains(v1.PureImports, path) {
		return false
	}
	ok, _, _ := gofacts.Acts(FactLedger(), gofacts.PureKey(path), gofacts.PureDigest(path, v1.PureImportsExcept()[path], toolchain.Version), toolchain.Version)
	return ok
}
