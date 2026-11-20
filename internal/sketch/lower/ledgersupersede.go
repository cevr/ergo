package lower

import (
	"slices"
	"strings"
)

// DropSuperseded deletes from entries, the ledger the writer is making, each
// entry this run's fresh entries supersede, and returns how many it deleted
// (LEDGER-SUPERSEDE). here are this build's tags: its platform (linux/amd64)
// and its experiments (goexperiment.simd), the names a key's build part is
// made of (ForeignKey.Build).
//
// An old entry of a fresh entry's family (the same law, provider and
// instance) under another build part is superseded when no build can read
// it any more. The writer can tell only from what this run computed:
//   - the old entry names this build's experiments exactly (none in a stock
//     build), so it was computed by a build of this run's experiment set,
//     the only one this run compared;
//   - and either its platform is this one or none, so this build's key is
//     the one recomputed (a shared entry when the fresh one names this
//     platform, an entry under this platform when the fresh one is
//     shared), or the fresh entry names no platform: every platform of
//     this experiment set compiles the code alike, so an entry under
//     another platform (darwin/arm64 in a linux/amd64 run) is read by
//     none.
//
// Every other entry stays. An entry under another platform, when the fresh
// one names this platform, is that platform's to recompute; an entry of
// another experiment set (a stock build's, in a GOEXPERIMENT=simd run) is
// that set's, which this run did not compare, so dropping it would make
// two builds take turns rewriting one law's entries.
func DropSuperseded(entries map[string]LedgerEntry, fresh []LedgerEntry, here []string) int {
	plat, exps := splitBuild(strings.Join(here, ","))
	byFamily := map[string][]string{}
	for k, e := range entries {
		if e.Fact == "" {
			f := ledgerKey(e.Law, e.Provider, e.Instance, "")
			byFamily[f] = append(byFamily[f], k)
		}
	}
	written := map[string]bool{}
	for _, e := range fresh {
		written[e.Key()] = true
	}
	dropped := 0
	for _, e := range fresh {
		if e.Fact != "" {
			continue
		}
		freshPlat, _ := splitBuild(e.Build)
		for _, k := range byFamily[ledgerKey(e.Law, e.Provider, e.Instance, "")] {
			old, ok := entries[k]
			if !ok || written[k] {
				continue
			}
			oldPlat, oldExps := splitBuild(old.Build)
			if !slices.Equal(oldExps, exps) {
				continue
			}
			if oldPlat == "" || oldPlat == plat || freshPlat == "" {
				delete(entries, k)
				dropped++
			}
		}
	}
	return dropped
}

// splitBuild is a build part's platform tag ("" for none) and its
// experiment tags, sorted.
func splitBuild(build string) (string, []string) {
	plat := ""
	exps := []string{}
	if build == "" {
		return plat, exps
	}
	for _, tag := range strings.Split(build, ",") {
		if strings.Contains(tag, "/") {
			plat = tag
		} else {
			exps = append(exps, tag)
		}
	}
	slices.Sort(exps)
	return plat, exps
}
