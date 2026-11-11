package residue_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ledgerFile is the schema of a package's testdata/mutants.json, as
// scripts/mutants (ledger.go, Ledger and Mutant) reads it. A field the
// runner does not know is an error there, so it is one here.
type ledgerFile struct {
	About   string `json:"about"`
	Mutants []struct {
		Name    string   `json:"name"`
		Origin  string   `json:"origin"`
		File    string   `json:"file"`
		From    string   `json:"from"`
		To      string   `json:"to"`
		Rule    string   `json:"rule"`
		Killers []string `json:"killers"`
		Gap     string   `json:"gap"`
		Anchor  *struct {
			File  string   `json:"file"`
			Text  string   `json:"text"`
			Tests []string `json:"tests"`
		} `json:"anchor"`
	} `json:"mutants"`
}

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: every package ledger has the runner's schema (readLedger: no
// unknown field, a unique name, a file and a from, a to that differs from
// the from, each test SUITE:NAME), and each edit applies once, so the
// mutant runner can judge it. It catches embed-duplicate-ledger-anchor; the
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
		var ledger ledgerFile
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&ledger); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		seen := map[string]bool{}
		for i, m := range ledger.Mutants {
			var tests []string
			tests = append(tests, m.Killers...)
			if m.Anchor != nil {
				tests = append(tests, m.Anchor.Tests...)
			}
			switch {
			case m.Name == "":
				t.Errorf("%s: mutant %d has no name", path, i)
			case seen[m.Name]:
				t.Errorf("%s: two mutants are named %q", path, m.Name)
			case m.File == "" || m.From == "":
				t.Errorf("%s: mutant %q needs a file and a from", path, m.Name)
			case m.From == m.To:
				t.Errorf("%s: mutant %q changes nothing", path, m.Name)
			}
			for _, k := range tests {
				if !strings.Contains(k, ":") {
					t.Errorf("%s: mutant %q: test %q is not SUITE:NAME", path, m.Name, k)
				}
			}
			seen[m.Name] = true
			if m.File == "" || m.From == "" {
				continue
			}
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
