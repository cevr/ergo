package lint

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var coChangeSeed = flag.Uint64("cochange.seed", 0, "run TestCoChangesModel on this seed alone")

// TestCoChangesModel is AS6's co-change property: for a seeded list of
// commits (each a list of paths from a pool, some listed twice, some
// commits past CoChangeMaxFiles), CoChanges equals a reference fold that
// counts each unordered pair of distinct paths once per commit of at most
// CoChangeMaxFiles distinct paths, sorted by count then paths. Mutants:
// cochange-no-skip (the skip of a commit past 50 files removed), cochange-twice (each pair
// counted in both orders), cochange-no-dedup (a path listed twice in one
// commit counted twice).
//
// Replay: go test -run '^TestCoChangesModel$' ./internal/sketch/lint -args -cochange.seed=N
func TestCoChangesModel(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(300) {
		seeds = append(seeds, s+1)
	}
	if *coChangeSeed != 0 {
		seeds = []uint64{*coChangeSeed}
	}
	var skipped, twice bool
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0xc0c4))
		pool := 4 + r.IntN(70)
		var commits [][]string
		for range r.IntN(6) {
			var paths []string
			for range r.IntN(pool + 1) {
				paths = append(paths, fmt.Sprintf("p%d/f%d.ergo", r.IntN(3), r.IntN(pool)))
			}
			if len(paths) > 0 && r.IntN(3) == 0 {
				paths = append(paths, paths[0])
				twice = true
			}
			commits = append(commits, paths)
		}
		got, want := CoChanges(commits), coChangesReference(commits, &skipped)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: CoChanges gives %v, the reference %v\ncommits: %q\nReplay: go test -run '^TestCoChangesModel$' ./internal/sketch/lint -args -cochange.seed=%d", seed, got, want, commits, seed)
		}
	}
	if *coChangeSeed == 0 && (!skipped || !twice) {
		t.Fatalf("the seeds reached no skipped commit (%v) or no repeated path (%v)", skipped, twice)
	}
}

// coChangesReference is the fold of TestCoChangesModel, written apart from
// CoChanges: a set per commit, every ordered pair a < b.
func coChangesReference(commits [][]string, skipped *bool) []CoChange {
	counts := map[string]int{}
	for _, c := range commits {
		set := map[string]bool{}
		for _, p := range c {
			set[p] = true
		}
		if len(set) > 50 {
			*skipped = true
			continue
		}
		for a := range set {
			for b := range set {
				if a < b {
					counts[a+"\n"+b]++
				}
			}
		}
	}
	var out []CoChange
	for k, n := range counts {
		a, b, _ := strings.Cut(k, "\n")
		out = append(out, CoChange{A: a, B: b, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}
