package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: each edit applies once, so the runner can judge it. It catches
// embed-missing-directive's duplicate anchor; the runner model only tests a
// generated ledger and cannot catch stale edits in the repository ledgers.
// It also resolves each ledger's killer and anchor ids as the runner does
// (suitesFor): an id that names no suite or no test fails here, where the
// runner once skipped it (MUT-names: v1:, sched:, rt-explore: …).
func TestLedgerEditsApplyOnce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}
		ledger, err := readLedger(path)
		if err != nil {
			return err
		}
		if _, _, _, err := suitesFor(root, []Job{{Path: path, Ledger: ledger}}); err != nil {
			t.Error(err)
		}
		for _, m := range ledger.Mutants {
			data, err := os.ReadFile(filepath.Join(root, m.File))
			if err != nil {
				t.Errorf("%s: %s: %v", path, m.Name, err)
				continue
			}
			if n := strings.Count(string(data), m.From); n != 1 {
				t.Errorf("%s: %s: from occurs %d times in %s, want exactly once", path, m.Name, n, m.File)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
