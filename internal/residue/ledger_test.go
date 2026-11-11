package residue_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLedgerEditsApplyOnce checks the public ledger contract against the
// checkout: each edit of every tracked package ledger applies once, so the
// mutant runner can judge it. It catches embed-duplicate-ledger-anchor;
// the runner's own tests use generated ledgers and cannot catch a stale
// edit in the repository's ledgers. It lives in residue, which reads every
// tracked file by design, so scripts/mutants' tests keep to their closure.
func TestLedgerEditsApplyOnce(t *testing.T) {
	out, err := exec.Command("git", "--no-optional-locks", "ls-files", "--full-name", "--", ":(top,glob)**/testdata/mutants.json").Output()
	if err != nil {
		t.Fatalf("list the mutant ledgers: %v", err)
	}
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find the checkout: %v", err)
	}
	top := strings.TrimSpace(string(root)) + "/"
	ledgers := strings.Fields(string(out))
	if len(ledgers) == 0 {
		t.Fatal("no tracked */testdata/mutants.json")
	}
	for _, path := range ledgers {
		data, err := os.ReadFile(top + path)
		if err != nil {
			t.Fatal(err)
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
			continue
		}
		for _, m := range ledger.Mutants {
			src, err := os.ReadFile(top + m.File)
			if err != nil {
				t.Errorf("%s: %s: %v", path, m.Name, err)
				continue
			}
			if n := strings.Count(string(src), m.From); n != 1 {
				t.Errorf("%s: %s: from occurs %d times in %s, want exactly once", path, m.Name, n, m.File)
			}
		}
	}
}
