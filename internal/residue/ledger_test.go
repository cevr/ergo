package residue_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: each edit of every package ledger applies once, so the mutant
// runner can judge it. It catches embed-duplicate-ledger-anchor; the
// runner's own tests use generated ledgers and cannot catch a stale edit in
// the repository's ledgers. It lives in residue, which reads every file by
// design, so scripts/mutants' tests keep to their closure. It walks the
// tree rather than asking git, so it runs in the mutant runner's copy too.
func TestLedgerEditsApplyOnce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no module root at %s: %v", root, err)
	}
	ledgers := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}
		ledgers++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var ledger struct {
			Mutants []struct {
				Name string `json:"name"`
				File string `json:"file"`
				From string `json:"from"`
			} `json:"mutants"`
		}
		if err := json.Unmarshal(data, &ledger); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		for _, m := range ledger.Mutants {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.File)))
			if err != nil {
				t.Errorf("%s: %s: %v", path, m.Name, err)
				continue
			}
			if n := strings.Count(string(src), m.From); n != 1 {
				t.Errorf("%s: %s: from occurs %d times in %s, want exactly once", path, m.Name, n, m.File)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ledgers == 0 {
		t.Fatal("no testdata/mutants.json below the module root")
	}
}
