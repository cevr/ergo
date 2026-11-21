package main

import (
	"maps"
	"os"
	"slices"
	"strconv"
	"testing"
)

// blameSeedVariable replays one seed of TestBlameHoldsTheCulprit.
const blameSeedVariable = "ERGO_GATE_BLAME_SEED"

// Blame's list holds the tip that broke a test: a seeded property over
// TestAffectedAgreesWithAFullRun's generated modules. The record is the
// model's segment at the base; a stack of tips applies one edit script
// each; the culprit of a test is the first tip at which its verdict
// differs from its parent's. For every test the record knows and that
// passed there, and that some tip moves, the list blame gives (each tip's
// change judged by tipChanges against the record) holds its culprit, and
// every later tip that moves it again. A failing seed prints the tips'
// edits, the culprits, the list and the replay command.
//
// Mutants (testdata/mutants.json): blame-first-tip-only,
// blame-record-ignored.
func TestBlameHoldsTheCulprit(t *testing.T) {
	seeds, first := 300, 0
	if s := os.Getenv(blameSeedVariable); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("%s=%q: %v", blameSeedVariable, s, err)
		}
		first, seeds = n, 1
	}
	culprits := 0
	for seed := first; seed < first+seeds; seed++ {
		m := newRoundModel(seed)
		base := maps.Clone(m.files)
		entry, seg := m.segment(base)
		if entry.Mode != "full" {
			continue // blame reads full records only (pickRound runs every test otherwise)
		}
		trees := []map[string]string{base}
		var edits [][]string
		for range 2 + m.r.IntN(4) {
			edits = append(edits, m.edit())
			trees = append(trees, maps.Clone(m.files))
		}
		get := func(tree map[string]string) func(string) ([]byte, bool) {
			return func(p string) ([]byte, bool) {
				s, ok := tree[p]
				return []byte(s), ok
			}
		}
		var tips []blameTip
		for i := 1; i < len(trees); i++ {
			var paths []string
			for p := range unionKeys(trees[i-1], trees[i]) {
				if trees[i-1][p] != trees[i][p] || hasKey(trees[i-1], p) != hasKey(trees[i], p) {
					paths = append(paths, p)
				}
			}
			slices.Sort(paths)
			tips = append(tips, blameTip{ID: strconv.Itoa(i), Changes: tipChanges(paths, get(base), get(trees[i-1]), get(trees[i]))})
		}
		for _, name := range slices.Sorted(maps.Keys(m.tests)) {
			known := seg.Lookup("a:" + name)
			if known == nil || known.Failed {
				continue
			}
			var moved []string
			for i := 1; i < len(trees); i++ {
				if m.verdict(trees[i-1], m.tests[name]) != m.verdict(trees[i], m.tests[name]) {
					moved = append(moved, strconv.Itoa(i))
				}
			}
			if len(moved) == 0 {
				continue
			}
			culprits++
			hits := blame(tips, name, func(changes []fileChange) roundPick {
				names := m.testNames(base)
				return pickRound(entry, seg, m.closure(), changes, names)
			})
			var listed []string
			for _, h := range hits {
				listed = append(listed, h.ID)
			}
			for _, tip := range moved {
				if !slices.Contains(listed, tip) {
					t.Fatalf("seed %d: %s: tip %s moved its verdict (tips %v), and blame lists %v\nedits per tip: %q\nreplay: %s=%d go test -count=1 -run '^TestBlameHoldsTheCulprit$' ./scripts/gate",
						seed, name, tip, moved, hits, edits, blameSeedVariable, seed)
				}
			}
		}
	}
	if seeds > 1 && culprits < seeds {
		t.Fatalf("only %d culprits over %d seeds: the property saw too few broken tests", culprits, seeds)
	}
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}
