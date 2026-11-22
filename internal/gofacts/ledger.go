package gofacts

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strings"
	"sync"
)

// The guard lines of the reviewed tables that stay in runtime/v1 (LAWFFI
// LF2d): v1.GoSyncCallbacks (`sync`) and v1.PureImports with
// v1.PureImportsExcept (`pure`). Their guards live in
// internal/gofacts/hostguard, which only go run ./scripts/stdlaws and
// tests link: they call expvar, flag and go/ast, which the compiler must
// not. go run ./scripts/stdlaws writes a line for each row into std's
// ledger, and a row acts (the checker admits a non-escaping callback
// clause; ergo bind go writes the pure tier) only on its line current and
// validated (Acts).

// SyncKey is the ledger key of the sync row of target.
func SyncKey(target string) string { return "go " + target + " sync callbacks" }

// SyncDigest is the digest of the sync row of target with its reason at
// toolchain.
func SyncDigest(target, reason, toolchain string) string {
	return lineDigest("fact " + SyncKey(target) + "\nreason " + reason + "\ngo " + toolchain + "\n")
}

// PureKey is the ledger key of the pure row of the Go package path.
func PureKey(path string) string { return "go " + path + " pure members" }

// PureDigest is the digest of the pure row of path, whose members except
// are excluded, at toolchain.
func PureDigest(path string, except []string, toolchain string) string {
	except = slices.Sorted(slices.Values(except))
	return lineDigest("fact " + PureKey(path) + "\nexcept " + strings.Join(except, ",") + "\ngo " + toolchain + "\n")
}

func lineDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

// Entry is one fact line of a ledger: `fact <key> <verdict> [by <run>]
// [with <keys>] <digest>` (lower.LedgerEntry is the whole ledger's form).
type Entry struct {
	Key, Verdict, By, With, Digest string
}

// Line is the entry's line.
func (e Entry) Line() string {
	s := "fact " + e.Key + " " + e.Verdict
	if e.By != "" {
		s += " by " + e.By
	}
	if e.With != "" {
		s += " with " + e.With
	}
	return s + " " + e.Digest
}

// index is the guard lines of the last ledger text Find read, by key: a
// binding asks once per member, and the text is the same each time.
var index struct {
	sync.Mutex
	text  string
	lines map[string]Entry
}

// Find is the guard line of key (a sync, pure or effect key) in the ledger text,
// if it has one.
func Find(ledger, key string) (Entry, bool) {
	index.Lock()
	defer index.Unlock()
	if index.lines == nil || index.text != ledger {
		index.text, index.lines = ledger, parseGuardLines(ledger)
	}
	e, ok := index.lines[key]
	return e, ok
}

// parseGuardLines is every sync, pure and effect line of the ledger text,
// by key.
func parseGuardLines(ledger string) map[string]Entry {
	out := map[string]Entry{}
	for _, line := range strings.Split(ledger, "\n") {
		words := strings.Fields(line)
		// fact go <target> <fact> <param> <verdict> … <digest>
		if len(words) < 7 || words[0] != "fact" || !(words[3] == "sync" && words[4] == "callbacks" || words[3] == "pure" && words[4] == "members" || words[3] == "effect" && (words[4] == "none" || words[4] == "ambient")) {
			continue
		}
		e := Entry{Key: strings.Join(words[1:5], " "), Verdict: words[5], Digest: words[len(words)-1]}
		for i := 6; i+1 < len(words)-1; i += 2 {
			switch words[i] {
			case "by":
				e.By = words[i+1]
			case "with":
				e.With = words[i+1]
			}
		}
		out[e.Key] = e
	}
	return out
}

// Acts reports whether the ledger's line for key is current (its digest
// and toolchain those the row gives) and validated by a named run, and
// why not: refuted is true for a current refuted line, which no row may
// act past.
func Acts(ledger, key, digest, toolchain string) (ok, refuted bool, why string) {
	e, found := Find(ledger, key)
	switch {
	case !found:
		return false, false, "the std ledger has no guard line for " + key + ": run go run ./scripts/stdlaws"
	case e.Digest != digest || e.With != toolchain:
		return false, false, "the guard line for " + key + " is stale (the row or the toolchain changed): run go run ./scripts/stdlaws"
	case e.Verdict == "refuted":
		return false, true, "its guard refuted it (ergo.laws: fact " + key + " refuted by " + e.By + "): correct or remove the row"
	case e.Verdict != "validated" || e.By == "":
		return false, false, "the guard line for " + key + " is " + e.Verdict + ": only a line validated by a named run lets the row act"
	}
	return true, false, ""
}
