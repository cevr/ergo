package lint_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	sdk "ergo.local/prototype/lint"
)

type paritySnapshot struct {
	AllRules []lint.Result   `json:"allRules"`
	Facts    []*sdk.Snapshot `json:"facts"`
}

func computeParitySnapshot(t *testing.T) []byte {
	t.Helper()
	inputs := loadBenchCorpus(t)
	conf := benchAllRulesConfig(t)
	families := sdk.Families()
	snap := paritySnapshot{
		AllRules: make([]lint.Result, len(inputs)),
		Facts:    make([]*sdk.Snapshot, len(inputs)),
	}
	for i, in := range inputs {
		snap.AllRules[i] = conf.Run(in)
		snap.Facts[i] = lint.Facts(in, families)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestLintCorpusParity checks that running all built-in rules and Facts over
// the multi-module benchmark corpus is deterministic and, when
// ERGO_LINT_PARITY_REF is set, matches the captured baseline byte-for-byte.
func TestLintCorpusParity(t *testing.T) {
	got := computeParitySnapshot(t)
	if writePath := os.Getenv("ERGO_WRITE_LINT_PARITY"); writePath != "" {
		if err := os.WriteFile(writePath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d bytes to %s", len(got), writePath)
		return
	}
	if refPath := os.Getenv("ERGO_LINT_PARITY_REF"); refPath != "" {
		want, err := os.ReadFile(refPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			var g, w paritySnapshot
			_ = json.Unmarshal(got, &g)
			_ = json.Unmarshal(want, &w)
			for i := range max(len(g.AllRules), len(w.AllRules)) {
				gb, _ := json.Marshal(g.AllRules[i])
				wb, _ := json.Marshal(w.AllRules[i])
				if !bytes.Equal(gb, wb) {
					t.Errorf("AllRules[%d] mismatch:\ngot:  %s\nwant: %s", i, truncate(string(gb), 600), truncate(string(wb), 600))
				}
			}
			for i := range max(len(g.Facts), len(w.Facts)) {
				gb, _ := json.Marshal(g.Facts[i])
				wb, _ := json.Marshal(w.Facts[i])
				if !bytes.Equal(gb, wb) {
					t.Errorf("Facts[%d] mismatch:\ngot:  %s\nwant: %s", i, truncate(string(gb), 600), truncate(string(wb), 600))
				}
			}
			t.Fatalf("parity snapshot differed from baseline %s (%d vs %d bytes)", refPath, len(got), len(want))
		}
	}
	// Always assert determinism across repeated runs in the same process.
	again := computeParitySnapshot(t)
	if !bytes.Equal(got, again) {
		t.Fatal("lint output is not deterministic across repeated runs")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
